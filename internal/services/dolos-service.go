package services

import (
	"app/internal/models/dto"
	"app/internal/s3"
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/gommon/log"
	"go.uber.org/fx"
)

type DolosService struct {
	db      *sql.DB
	s3      *s3.S3
	client  *http.Client
	baseURL string
}

type DolosReport struct {
	Status    string `json:"status"`
	ReportURL string `json:"report_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

type dolosReportResponse struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	HTMLURL string `json:"html_url"`
	Status  string `json:"status"`
	Error   string `json:"error"`
}

type dolosSource struct{ id, userID, problemID, language string }

func NewDolosService(db *sql.DB, objectStore *s3.S3) *DolosService {
	base := strings.TrimRight(os.Getenv("DOLOS_API_URL"), "/")
	return &DolosService{db: db, s3: objectStore, baseURL: base, client: &http.Client{Timeout: 65 * time.Second}}
}

func (d *DolosService) GetReport(ctx context.Context, contestID string) (*DolosReport, error) {
	var report DolosReport
	err := d.db.QueryRowContext(ctx, `SELECT status, COALESCE(report_url,''), COALESCE(error,'') FROM contest_plagiarism_reports WHERE contest_id=$1`, contestID).Scan(&report.Status, &report.ReportURL, &report.Error)
	if err == sql.ErrNoRows {
		return &DolosReport{Status: "pending"}, nil
	}
	return &report, err
}

func (d *DolosService) GetPairs(ctx context.Context, contestID string) ([]dto.PlagiarismPair, error) {
	var reportID, status string
	if err := d.db.QueryRowContext(ctx, `SELECT status, COALESCE(dolos_report_id,'') FROM contest_plagiarism_reports WHERE contest_id=$1`, contestID).Scan(&status, &reportID); err != nil {
		return nil, err
	}
	if status != "finished" || reportID == "" {
		return nil, fmt.Errorf("Dolos report is not finished yet")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/reports/"+url.PathEscape(reportID)+"/data/pairs", nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Dolos pairs request returned HTTP %d", resp.StatusCode)
	}
	reader := csv.NewReader(io.LimitReader(resp.Body, 8*1024*1024))
	reader.TrimLeadingSpace = true
	headers, err := reader.Read()
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	pathCols := make([]int, 0, 2)
	for i, h := range headers {
		n := strings.ToLower(strings.TrimSpace(h))
		index[n] = i
		if strings.Contains(n, "file path") {
			pathCols = append(pathCols, i)
		}
	}
	field := func(row []string, names ...string) string {
		for _, n := range names {
			if i, ok := index[n]; ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
		}
		return ""
	}
	var pairs []dto.PlagiarismPair
	for {
		row, e := reader.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		var left, right string
		if len(pathCols) >= 2 && pathCols[0] < len(row) && pathCols[1] < len(row) {
			left, right = strings.TrimSpace(row[pathCols[0]]), strings.TrimSpace(row[pathCols[1]])
		}
		if left == "" || right == "" {
			continue
		}
		p := dto.PlagiarismPair{LeftFile: left, RightFile: right, ReviewStatus: "unreviewed"}
		p.LeftUserID = strings.Split(path.Clean(left), "/")[0]
		p.RightUserID = strings.Split(path.Clean(right), "/")[0]
		p.Similarity, _ = strconv.ParseFloat(field(row, "similarity"), 64)
		p.LongestFragment, _ = strconv.Atoi(field(row, "longest fragment", "longest_fragment"))
		p.TotalOverlap, _ = strconv.Atoi(field(row, "total overlap", "total_overlap"))
		p.Key = pairKey(left, right)
		if err := d.db.QueryRowContext(ctx, `SELECT status,note FROM contest_plagiarism_pair_reviews WHERE contest_id=$1 AND pair_key=$2`, contestID, p.Key).Scan(&p.ReviewStatus, &p.ReviewNote); err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	for i := range pairs {
		_ = d.db.QueryRowContext(ctx, `SELECT name FROM users WHERE id=$1`, pairs[i].LeftUserID).Scan(&pairs[i].LeftName)
		_ = d.db.QueryRowContext(ctx, `SELECT name FROM users WHERE id=$1`, pairs[i].RightUserID).Scan(&pairs[i].RightName)
	}
	return pairs, nil
}

func pairKey(left, right string) string {
	if right < left {
		left, right = right, left
	}
	return base64.RawURLEncoding.EncodeToString([]byte(left + "\x00" + right))
}

func (d *DolosService) ReviewPair(ctx context.Context, contestID, key, status, note string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO contest_plagiarism_pair_reviews(contest_id,pair_key,status,note) VALUES($1,$2,$3,$4) ON CONFLICT(contest_id,pair_key) DO UPDATE SET status=EXCLUDED.status,note=EXCLUDED.note,updated_at=NOW()`, contestID, key, status, note)
	return err
}

func (d *DolosService) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.process(ctx); err != nil {
				log.Errorf("Dolos worker: %v", err)
			}
		}
	}
}

func (d *DolosService) process(ctx context.Context) error {
	if d.db == nil || d.s3 == nil || d.baseURL == "" {
		return nil
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id::text, name FROM contests WHERE end_time <= $1 ORDER BY end_time`, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	type contest struct{ id, name string }
	var contests []contest
	for rows.Next() {
		var c contest
		if err := rows.Scan(&c.id, &c.name); err != nil {
			rows.Close()
			return err
		}
		contests = append(contests, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, c := range contests {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := d.processContest(ctx, c.id, c.name); err != nil {
			log.Errorf("Dolos contest %s: %v", c.id, err)
		}
	}
	return nil
}

func (d *DolosService) processContest(ctx context.Context, contestID, contestName string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO contest_plagiarism_reports(contest_id,status) VALUES($1,'pending') ON CONFLICT(contest_id) DO NOTHING`, contestID)
	if err != nil {
		return err
	}
	var reportID string
	err = d.db.QueryRowContext(ctx, `UPDATE contest_plagiarism_reports SET status='processing', updated_at=NOW() WHERE contest_id=$1 AND (status='pending' OR (status='processing' AND updated_at < NOW()-INTERVAL '3 minutes')) RETURNING COALESCE(dolos_report_id,'')`, contestID).Scan(&reportID)
	if err == sql.ErrNoRows { 
		return d.pollReport(ctx, contestID)
	}
	if err != nil {
		return err
	}
	if reportID != "" {
		return d.pollReport(ctx, contestID)
	}

	sources, err := d.listSources(ctx, contestID)
	if err != nil {
		d.saveError(ctx, contestID, err)
		return err
	}
	if len(sources) < 2 { 
		_, err = d.db.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status='skipped', error='Fewer than two code submissions', updated_at=NOW() WHERE contest_id=$1`, contestID)
		return err
	}
	archive, err := d.makeArchive(ctx, sources)
	if err != nil {
		d.saveError(ctx, contestID, err)
		return err
	}
	if len(archive) > 10*1024*1024 {
		err = fmt.Errorf("Dolos archive exceeds public API 10 MB limit")
		d.saveError(ctx, contestID, err)
		return err
	}
	remote, err := d.createReport(ctx, contestName, archive)
	if err != nil {
		d.saveError(ctx, contestID, err)
		return err
	}
	_, err = d.db.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status=$2,dolos_report_id=$3,report_url=$4,error=NULL,updated_at=NOW() WHERE contest_id=$1`, contestID, remote.Status, remote.ID, remote.HTMLURL)
	return err
}

func (d *DolosService) listSources(ctx context.Context, contestID string) ([]dolosSource, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT ON (user_id, problem_id) id::text,user_id::text,problem_id::text,COALESCE(language,'') FROM submissions WHERE contest_id=$1 AND type='code' ORDER BY user_id,problem_id,created_at DESC,id DESC`, contestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dolosSource
	for rows.Next() {
		var s dolosSource
		if err := rows.Scan(&s.id, &s.userID, &s.problemID, &s.language); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DolosService) makeArchive(ctx context.Context, sources []dolosSource) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, s := range sources {
		code, err := d.s3.GetObject(ctx, s.id)
		if err != nil {
			zw.Close()
			return nil, fmt.Errorf("read source %s: %w", s.id, err)
		}
		code, err = decodeSource(code)
		if err != nil {
			zw.Close()
			return nil, fmt.Errorf("decode source %s: %w", s.id, err)
		}
		ext := languageExtension(s.language)
		name := path.Join(s.userID, s.problemID+s.id[:min(8, len(s.id))]+ext)
		f, err := zw.Create(name)
		if err != nil {
			zw.Close()
			return nil, err
		}
		if _, err = f.Write([]byte(code)); err != nil {
			zw.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *DolosService) createReport(ctx context.Context, name string, archive []byte) (*dolosReportResponse, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("dataset[name]", name)
	fw, err := mw.CreateFormFile("dataset[zipfile]", "contest.zip")
	if err != nil {
		return nil, err
	}
	if _, err = fw.Write(archive); err != nil {
		return nil, err
	}
	if err = mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/reports", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("Dolos returned HTTP %d: %s", resp.StatusCode, data)
	}
	var result dolosReportResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.ID == "" {
		return nil, fmt.Errorf("Dolos response omitted report ID")
	}
	if result.HTMLURL == "" {
		result.HTMLURL = result.URL
	}
	return &result, nil
}

func (d *DolosService) pollReport(ctx context.Context, contestID string) error {
	var id string
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(dolos_report_id,'') FROM contest_plagiarism_reports WHERE contest_id=$1`, contestID).Scan(&id)
	if err != nil || id == "" {
		return err
	}
	endpoint := d.baseURL + "/reports/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Dolos poll returned HTTP %d", resp.StatusCode)
	}
	var remote dolosReportResponse
	if err := json.NewDecoder(resp.Body).Decode(&remote); err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status=$2,error=NULLIF($3,''),updated_at=NOW() WHERE contest_id=$1`, contestID, remote.Status, remote.Error)
	return err
}

func (d *DolosService) saveError(ctx context.Context, contestID string, cause error) {
	_, err := d.db.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status='pending',error=$2,updated_at=NOW() WHERE contest_id=$1`, contestID, cause.Error())
	if err != nil {
		log.Errorf("save Dolos error for %s: %v", contestID, err)
	}
}

func languageExtension(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "python", "python3", "py":
		return ".py"
	case "c++", "cpp", "c++17", "gnu++17":
		return ".cpp"
	case "c", "c11", "gnu c11":
		return ".c"
	case "java":
		return ".java"
	case "javascript", "js", "node", "node.js":
		return ".js"
	case "go", "golang":
		return ".go"
	case "rust":
		return ".rs"
	case "kotlin":
		return ".kt"
	case "ruby":
		return ".rb"
	case "php":
		return ".php"
	case "swift":
		return ".swift"
	case "c#", "csharp":
		return ".cs"
	default:
		return ".txt"
	}
}

func NewDolosWorker(lc fx.Lifecycle, d *DolosService) {
	workerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{OnStart: func(context.Context) error { go func() { defer close(done); d.Run(workerCtx) }(); return nil }, OnStop: func(ctx context.Context) error {
		cancel()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
}
