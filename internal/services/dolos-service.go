package services

import (
	"app/internal/common"
	"app/internal/judge0"
	"app/internal/models/dto"
	"app/internal/s3"
	"archive/zip"
	"bytes"
	"container/heap"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/labstack/gommon/log"
	"go.uber.org/fx"
)

type cachedReportPairs struct {
	pairs     []dto.PlagiarismPair
	truncated bool
}

type DolosService struct {
	db           *sql.DB
	s3           *s3.S3
	client       *http.Client
	baseURL      string
	pairsCache   map[string]cachedReportPairs
	pairsCacheMu sync.RWMutex
}

type DolosReport = dto.DolosContestReportResponse

type dolosReportResponse struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	HTMLURL string `json:"html_url"`
	Status  string `json:"status"`
	Error   string `json:"error"`
}

type dolosUploadError struct {
	err       error
	ambiguous bool
	retryable bool
}

func (e *dolosUploadError) Error() string { return e.err.Error() }
func (e *dolosUploadError) Unwrap() error { return e.err }

type dolosSource struct {
	id        string
	userID    string
	problemID string
	language  string
}

var errDolosArchiveTooLarge = errors.New("Dolos archive exceeds 10 MB limit")

func NewDolosService(db *sql.DB, objectStore *s3.S3) *DolosService {
	base := strings.TrimRight(os.Getenv("DOLOS_API_URL"), "/")
	return &DolosService{
		db:         db,
		s3:         objectStore,
		baseURL:    base,
		client:     &http.Client{Timeout: 65 * time.Second},
		pairsCache: make(map[string]cachedReportPairs),
	}
}

func (d *DolosService) getCachedPairs(reportID string) (cachedReportPairs, bool) {
	d.pairsCacheMu.RLock()
	defer d.pairsCacheMu.RUnlock()
	cached, ok := d.pairsCache[reportID]
	if !ok {
		return cachedReportPairs{}, false
	}
	out := make([]dto.PlagiarismPair, len(cached.pairs))
	copy(out, cached.pairs)
	return cachedReportPairs{pairs: out, truncated: cached.truncated}, true
}

func (d *DolosService) setCachedPairs(reportID string, pairs []dto.PlagiarismPair, truncated bool) {
	d.pairsCacheMu.Lock()
	defer d.pairsCacheMu.Unlock()
	if d.pairsCache == nil {
		d.pairsCache = make(map[string]cachedReportPairs)
	}
	if len(d.pairsCache) >= 100 {
		for k := range d.pairsCache {
			delete(d.pairsCache, k)
			break
		}
	}
	copied := make([]dto.PlagiarismPair, len(pairs))
	copy(copied, pairs)
	d.pairsCache[reportID] = cachedReportPairs{pairs: copied, truncated: truncated}
}

func (d *DolosService) deleteCachedPairs(reportID string) {
	d.pairsCacheMu.Lock()
	defer d.pairsCacheMu.Unlock()
	delete(d.pairsCache, reportID)
}

func generateClaimToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (d *DolosService) checkContestExists(ctx context.Context, contestID string) error {
	var dummy int
	err := d.db.QueryRowContext(ctx, `SELECT 1 FROM contests WHERE id = $1`, contestID).Scan(&dummy)
	if err == sql.ErrNoRows {
		return common.ContestNotFoundError
	}
	return err
}

func (d *DolosService) GetReport(ctx context.Context, contestID string) (*DolosReport, error) {
	if err := d.checkContestExists(ctx, contestID); err != nil {
		return nil, err
	}
	fresh, err := d.contestEvidenceCurrent(ctx, contestID)
	if err != nil {
		return nil, err
	}
	if !fresh {
		return &DolosReport{Status: "processing", Reports: []dto.ProblemReportSummary{}}, nil
	}

	rows, err := d.db.QueryContext(ctx, `
		SELECT problem_id, language, status, COALESCE(report_url, ''), COALESCE(error, '')
		FROM contest_plagiarism_reports
		WHERE contest_id = $1
		ORDER BY problem_id, language
	`, contestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reports []dto.ProblemReportSummary
	for rows.Next() {
		var s dto.ProblemReportSummary
		if err := rows.Scan(&s.ProblemID, &s.Language, &s.Status, &s.ReportURL, &s.Error); err != nil {
			return nil, err
		}
		reports = append(reports, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(reports) == 0 {
		var settled bool
		if err := d.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contest_plagiarism_runs WHERE contest_id=$1)`, contestID).Scan(&settled); err != nil {
			return nil, err
		}
		if settled {
			return &DolosReport{Status: "skipped", Reports: []dto.ProblemReportSummary{}}, nil
		}
		return &DolosReport{
			Status:  "pending",
			Reports: []dto.ProblemReportSummary{},
		}, nil
	}

	var errMsgs []string
	for _, r := range reports {
		if r.Error != "" {
			errMsgs = append(errMsgs, fmt.Sprintf("%s/%s: %s", r.ProblemID, r.Language, r.Error))
		}
	}

	return &DolosReport{
		Status:  dolosOverallStatus(reports),
		Error:   strings.Join(errMsgs, "; "),
		Reports: reports,
	}, nil
}

func dolosOverallStatus(reports []dto.ProblemReportSummary) string {
	allSkipped, allComplete, hasRunning, hasFailed := true, true, false, false
	for _, r := range reports {
		if r.Status != "skipped" {
			allSkipped = false
		}
		if r.Status != "finished" && r.Status != "skipped" {
			allComplete = false
		}
		switch r.Status {
		case "queued", "running", "processing", "uploading", "pending", "unknown":
			hasRunning = true
		case "failed", "error", "purged", "unavailable":
			hasFailed = true
		}
	}
	if allSkipped {
		return "skipped"
	}
	if allComplete {
		return "finished"
	}
	if !hasRunning && hasFailed {
		return "failed"
	}
	return "processing"
}

func (d *DolosService) contestEvidenceCurrent(ctx context.Context, contestID string) (bool, error) {
	var exists bool
	var analyzedEnd int64
	var sourceCount, latestSubmission int64
	err := d.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM contest_plagiarism_runs r WHERE r.contest_id=$1),
		       COALESCE((SELECT analyzed_end_time FROM contest_plagiarism_runs WHERE contest_id=$1),0),
		       (SELECT COUNT(*) FROM submissions WHERE contest_id=$1 AND type='code'),
		       COALESCE((SELECT MAX(created_at) FROM submissions WHERE contest_id=$1 AND type='code'),0)
	`, contestID).Scan(&exists, &analyzedEnd, &sourceCount, &latestSubmission)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	var recordedCount, recordedLatest int64
	if err := d.db.QueryRowContext(ctx, `SELECT source_count,latest_submission_at FROM contest_plagiarism_runs WHERE contest_id=$1`, contestID).Scan(&recordedCount, &recordedLatest); err != nil {
		return false, err
	}
	var currentEnd int64
	if err := d.db.QueryRowContext(ctx, `SELECT end_time FROM contests WHERE id=$1`, contestID).Scan(&currentEnd); err != nil {
		return false, err
	}
	return analyzedEnd == currentEnd && recordedCount == sourceCount && recordedLatest == latestSubmission, nil
}

func (d *DolosService) GetPairsWithMeta(ctx context.Context, contestID string, page int) ([]dto.PlagiarismPair, int, bool, error) {
	if page < 0 {
		page = 0
	}
	if err := d.checkContestExists(ctx, contestID); err != nil {
		return nil, 0, false, err
	}
	fresh, err := d.contestEvidenceCurrent(ctx, contestID)
	if err != nil {
		return nil, 0, false, err
	}
	if !fresh {
		return nil, 0, false, common.ErrReportNotReady
	}

	rows, err := d.db.QueryContext(ctx, `
		SELECT status, COALESCE(dolos_report_id, '')
		FROM contest_plagiarism_reports
		WHERE contest_id = $1
	`, contestID)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()

	type repInfo struct {
		status   string
		reportID string
	}
	var reps []repInfo
	for rows.Next() {
		var r repInfo
		if err := rows.Scan(&r.status, &r.reportID); err != nil {
			return nil, 0, false, err
		}
		reps = append(reps, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}

	if len(reps) == 0 {
		return nil, 0, false, common.ErrReportNotFound
	}
	for _, r := range reps {
		if r.status == "processing" || r.status == "uploading" || r.status == "pending" {
			return nil, 0, false, common.ErrReportNotReady
		}
	}
	hasFinished := false
	for _, r := range reps {
		if r.status == "finished" && r.reportID != "" {
			hasFinished = true
		}
	}

	if !hasFinished {
		for _, r := range reps {
			if r.status == "queued" || r.status == "running" || r.status == "unknown" {
				return nil, 0, false, common.ErrReportNotReady
			}
		}
		return nil, 0, false, common.ErrReportNotFound
	}

	var allPairs []dto.PlagiarismPair
	anyTruncated := false
	for _, r := range reps {
		if r.status != "finished" || r.reportID == "" {
			continue
		}
		pairs, repTruncated, err := d.fetchReportPairs(ctx, r.reportID)
		if err != nil {
			return nil, 0, false, err
		}
		if repTruncated {
			anyTruncated = true
		}
		for i := range pairs {
			pairs[i].ReportID = r.reportID
		}
		allPairs = append(allPairs, pairs...)
	}

	if len(allPairs) == 0 {
		return []dto.PlagiarismPair{}, 0, anyTruncated, nil
	}
	reviewsByReport, reviewsByKey, err := d.loadContestReviews(ctx, contestID)
	if err != nil {
		return nil, 0, false, err
	}

	subIDs := make(map[string]bool)
	for i := range allPairs {
		if rev, ok := reviewsByReport[allPairs[i].ReportID+"|"+allPairs[i].Key]; ok {
			allPairs[i].ReviewStatus, allPairs[i].ReviewNote = rev.status, rev.note
		} else if rev, ok := reviewsByKey[allPairs[i].Key]; ok {
			// Preserve earlier human reviews across report regenerations
			allPairs[i].ReviewStatus, allPairs[i].ReviewNote = rev.status, rev.note
		} else {
			allPairs[i].ReviewStatus = "unreviewed"
		}
		if allPairs[i].LeftUserID != "" {
			subIDs[allPairs[i].LeftUserID] = true
		}
		if allPairs[i].RightUserID != "" {
			subIDs[allPairs[i].RightUserID] = true
		}
	}

	subUsers, err := d.loadSubmissionUsers(ctx, subIDs)
	if err != nil {
		return nil, 0, false, err
	}
	for i := range allPairs {
		if info, ok := subUsers[allPairs[i].LeftUserID]; ok {
			allPairs[i].LeftUserID = info.userID
			allPairs[i].LeftName = info.name
		} else {
			allPairs[i].LeftUserID = ""
			allPairs[i].LeftName = ""
		}
		if info, ok := subUsers[allPairs[i].RightUserID]; ok {
			allPairs[i].RightUserID = info.userID
			allPairs[i].RightName = info.name
		} else {
			allPairs[i].RightUserID = ""
			allPairs[i].RightName = ""
		}
	}

	sort.Slice(allPairs, func(i, j int) bool {
		if allPairs[i].Similarity != allPairs[j].Similarity {
			return allPairs[i].Similarity > allPairs[j].Similarity
		}
		return allPairs[i].Key < allPairs[j].Key
	})
	totalCount := len(allPairs)
	// The API remains a plain array; page is a stable zero-based offset page.
	const pageSize = 100
	if page > len(allPairs)/pageSize {
		return []dto.PlagiarismPair{}, totalCount, anyTruncated, nil
	}
	start := page * pageSize
	if start >= len(allPairs) {
		return []dto.PlagiarismPair{}, totalCount, anyTruncated, nil
	}
	end := min(start+pageSize, len(allPairs))
	allPairs = allPairs[start:end]
	return allPairs, totalCount, anyTruncated, nil
}

type reviewData struct {
	status string
	note   string
}

func (d *DolosService) loadContestReviews(ctx context.Context, contestID string) (byReport map[string]reviewData, byKey map[string]reviewData, err error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT report_id, pair_key, status, note
		FROM contest_plagiarism_pair_reviews
		WHERE contest_id = $1
		ORDER BY updated_at ASC
	`, contestID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	byReport = make(map[string]reviewData)
	byKey = make(map[string]reviewData)
	for rows.Next() {
		var reportID, pairKey, status, note string
		if err := rows.Scan(&reportID, &pairKey, &status, &note); err != nil {
			return nil, nil, err
		}
		rd := reviewData{status: status, note: note}
		byReport[reportID+"|"+pairKey] = rd
		byKey[pairKey] = rd
	}
	return byReport, byKey, rows.Err()
}

type userSubInfo struct {
	userID string
	name   string
}

func (d *DolosService) loadSubmissionUsers(ctx context.Context, subIDs map[string]bool) (map[string]userSubInfo, error) {
	out := make(map[string]userSubInfo)
	if len(subIDs) == 0 {
		return out, nil
	}

	ids := make([]string, 0, len(subIDs))
	for id := range subIDs {
		ids = append(ids, id)
	}

	rows, err := d.db.QueryContext(ctx, `
		SELECT s.id::text, COALESCE(u.id::text, ''), COALESCE(u.name, '')
		FROM submissions s
		LEFT JOIN users u ON u.id = s.user_id
		WHERE s.id = ANY($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var subID, userID, name string
		if err := rows.Scan(&subID, &userID, &name); err != nil {
			return nil, err
		}
		out[subID] = userSubInfo{userID: userID, name: name}
	}
	return out, rows.Err()
}

type pairMinHeap []dto.PlagiarismPair

func (h pairMinHeap) Len() int           { return len(h) }
func (h pairMinHeap) Less(i, j int) bool { return h.lessPair(h[i], h[j]) }
func (h pairMinHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *pairMinHeap) Push(x any)        { *h = append(*h, x.(dto.PlagiarismPair)) }
func (h *pairMinHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

func (h pairMinHeap) lessPair(a, b dto.PlagiarismPair) bool {
	if a.Similarity != b.Similarity {
		return a.Similarity < b.Similarity
	}
	return a.Key < b.Key
}

type limitedCountingReader struct {
	r     io.Reader
	limit int64
	read  int64
}

func (l *limitedCountingReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.read += int64(n)
	if l.read > l.limit {
		return n, fmt.Errorf("Dolos pairs CSV response exceeded maximum byte budget (%d bytes)", l.limit)
	}
	return n, err
}

const (
	MaxPairsBytes          = 64 * 1024 * 1024
	MaxPairsPerReport      = 1000
	MinSimilarityThreshold = 0.20
)

type dolosCSVReader struct {
	reader *csv.Reader
	closer io.Closer
	colIdx map[string]int
}

func (c *dolosCSVReader) Close() error {
	if c.closer != nil {
		return c.closer.Close()
	}
	return nil
}

func (c *dolosCSVReader) getCol(names ...string) (int, bool) {
	for _, n := range names {
		if idx, ok := c.colIdx[strings.ToLower(n)]; ok {
			return idx, true
		}
	}
	return -1, false
}

func (d *DolosService) openReportPairsCSV(ctx context.Context, reportID string) (*dolosCSVReader, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/reports/"+url.PathEscape(reportID)+"/data/pairs.csv", nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		if d.db != nil {
			_, _ = d.db.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status='purged', error='Report data was purged or not found on Dolos provider', updated_at=NOW() WHERE dolos_report_id=$1 AND status='finished'`, reportID)
		}
		d.deleteCachedPairs(reportID)
		return nil, common.ErrReportNotFound
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("Dolos pairs request returned HTTP %d", resp.StatusCode)
	}

	reader := csv.NewReader(&limitedCountingReader{r: resp.Body, limit: MaxPairsBytes})
	reader.TrimLeadingSpace = true
	headers, err := reader.Read()
	if err != nil {
		resp.Body.Close()
		return nil, err
	}

	colIdx := make(map[string]int, len(headers))
	for i, h := range headers {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}

	return &dolosCSVReader{
		reader: reader,
		closer: resp.Body,
		colIdx: colIdx,
	}, nil
}

func (d *DolosService) fetchReportPairs(ctx context.Context, reportID string) ([]dto.PlagiarismPair, bool, error) {
	if cached, ok := d.getCachedPairs(reportID); ok {
		return cached.pairs, cached.truncated, nil
	}

	cr, err := d.openReportPairsCSV(ctx, reportID)
	if err != nil {
		return nil, false, err
	}
	defer cr.Close()

	leftPathCol, hasLeft := cr.getCol("leftfilepath", "left_file_path", "left file path")
	rightPathCol, hasRight := cr.getCol("rightfilepath", "right_file_path", "right file path")
	simCol, hasSim := cr.getCol("similarity")
	overlapCol, hasOverlap := cr.getCol("totaloverlap", "total_overlap", "total overlap")
	longestCol, hasLongest := cr.getCol("longestfragment", "longest_fragment", "longest fragment")

	if !hasLeft || !hasRight || !hasSim || !hasOverlap || !hasLongest {
		return nil, false, fmt.Errorf("Dolos CSV missing required file path or metric headers")
	}

	h := &pairMinHeap{}
	heap.Init(h)

	var qualifyingCount int

	for {
		row, err := cr.reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}

		if leftPathCol >= len(row) || rightPathCol >= len(row) || simCol >= len(row) || overlapCol >= len(row) || longestCol >= len(row) {
			return nil, false, fmt.Errorf("Dolos CSV row has fewer columns than its header")
		}

		similarity, err := strconv.ParseFloat(strings.TrimSpace(row[simCol]), 64)
		if err != nil || math.IsNaN(similarity) || math.IsInf(similarity, 0) || similarity < 0 || similarity > 1 {
			return nil, false, fmt.Errorf("Dolos CSV row has invalid similarity")
		}
		if similarity < MinSimilarityThreshold {
			continue
		}

		rawLeft := strings.TrimSpace(row[leftPathCol])
		rawRight := strings.TrimSpace(row[rightPathCol])
		if rawLeft == "" || rawRight == "" {
			continue
		}

		canonicalLeft, leftSubID := extractCanonicalPathAndSubID(rawLeft)
		canonicalRight, rightSubID := extractCanonicalPathAndSubID(rawRight)
		if canonicalLeft == "" || canonicalRight == "" {
			continue
		}

		totalOverlap, err := strconv.Atoi(strings.TrimSpace(row[overlapCol]))
		if err != nil || totalOverlap < 0 {
			return nil, false, fmt.Errorf("Dolos CSV row has invalid total overlap")
		}
		longestFragment, err := strconv.Atoi(strings.TrimSpace(row[longestCol]))
		if err != nil || longestFragment < 0 {
			return nil, false, fmt.Errorf("Dolos CSV row has invalid longest fragment")
		}

		p := dto.PlagiarismPair{
			Key:             pairKey(canonicalLeft, canonicalRight),
			LeftFile:        canonicalLeft,
			LeftUserID:      leftSubID,
			RightFile:       canonicalRight,
			RightUserID:     rightSubID,
			Similarity:      similarity,
			TotalOverlap:    totalOverlap,
			LongestFragment: longestFragment,
			ReviewStatus:    "unreviewed",
		}

		qualifyingCount++

		if h.Len() < MaxPairsPerReport {
			heap.Push(h, p)
		} else if h.lessPair((*h)[0], p) {
			(*h)[0] = p
			heap.Fix(h, 0)
		}
	}

	result := make([]dto.PlagiarismPair, h.Len())
	for i := len(result) - 1; i >= 0; i-- {
		result[i] = heap.Pop(h).(dto.PlagiarismPair)
	}

	truncated := qualifyingCount > MaxPairsPerReport
	d.setCachedPairs(reportID, result, truncated)
	return result, truncated, nil
}

func extractCanonicalPathAndSubID(providerPath string) (canonicalPath, subID string) {
	cleaned := path.Clean(strings.ReplaceAll(providerPath, "\\", "/"))
	parts := strings.Split(cleaned, "/")
	if len(parts) >= 2 {
		subID := parts[len(parts)-2]
		fileName := parts[len(parts)-1]
		if subID != "" && subID != "." && subID != ".." && fileName != "" {
			return subID + "/" + fileName, subID
		}
	}
	return cleaned, ""
}

func pairKey(left, right string) string {
	if right < left {
		left, right = right, left
	}
	return base64.RawURLEncoding.EncodeToString([]byte(left + "\x00" + right))
}

func sourceSignature(endTime int64, sources []dolosSource) string {
	parts := make([]string, 0, len(sources)+1)
	parts = append(parts, strconv.FormatInt(endTime, 10))
	for _, s := range sources {
		parts = append(parts, strings.Join([]string{s.id, s.userID, s.problemID, s.language}, "\x00"))
	}
	sort.Strings(parts[1:])
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:])
}

func (d *DolosService) fetchReportPairMembership(ctx context.Context, reportID, key string) (bool, error) {
	if cached, ok := d.getCachedPairs(reportID); ok {
		for _, pair := range cached.pairs {
			if pair.Key == key {
				return true, nil
			}
		}
	}

	cr, err := d.openReportPairsCSV(ctx, reportID)
	if err != nil {
		return false, err
	}
	defer cr.Close()

	leftPathCol, hasLeft := cr.getCol("leftfilepath", "left_file_path", "left file path")
	rightPathCol, hasRight := cr.getCol("rightfilepath", "right_file_path", "right file path")
	if !hasLeft || !hasRight {
		return false, fmt.Errorf("Dolos CSV missing required file path headers")
	}

	for {
		row, err := cr.reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, err
		}
		if leftPathCol >= len(row) || rightPathCol >= len(row) {
			continue
		}
		rawLeft := strings.TrimSpace(row[leftPathCol])
		rawRight := strings.TrimSpace(row[rightPathCol])
		if rawLeft == "" || rawRight == "" {
			continue
		}
		canonicalLeft, _ := extractCanonicalPathAndSubID(rawLeft)
		canonicalRight, _ := extractCanonicalPathAndSubID(rawRight)
		if canonicalLeft == "" || canonicalRight == "" {
			continue
		}
		if pairKey(canonicalLeft, canonicalRight) == key {
			return true, nil
		}
	}
	return false, nil
}

func (d *DolosService) ReviewPair(ctx context.Context, contestID, key, expectedReportID, status, note string) error {
	if err := d.checkContestExists(ctx, contestID); err != nil {
		return err
	}
	fresh, err := d.contestEvidenceCurrent(ctx, contestID)
	if err != nil {
		return err
	}
	if !fresh {
		return common.ErrReportNotReady
	}

	if key == "" || expectedReportID == "" {
		return common.ErrInvalidPairKey
	}
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || !bytes.Contains(decoded, []byte("\x00")) {
		return common.ErrInvalidPairKey
	}

	// The displayed report ID scopes the provider lookup. An unrelated report
	// must not make this review fail if its provider endpoint is unavailable.
	var foundReport string
	if err := d.db.QueryRowContext(ctx, `SELECT dolos_report_id FROM contest_plagiarism_reports WHERE contest_id=$1 AND status='finished' AND dolos_report_id=$2`, contestID, expectedReportID).Scan(&foundReport); err != nil {
		if err == sql.ErrNoRows {
			return common.ErrPairNotFound
		}
		return err
	}
	found, err := d.fetchReportPairMembership(ctx, foundReport, key)
	if err != nil {
		return err
	}
	if !found {
		return common.ErrPairNotFound
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Match cascade deletion's contest-before-report lock order.
	var lockedContest string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM contests WHERE id=$1 FOR KEY SHARE`, contestID).Scan(&lockedContest); err != nil {
		if err == sql.ErrNoRows {
			return common.ContestNotFoundError
		}
		return err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT p.dolos_report_id
		FROM contest_plagiarism_reports p
		JOIN contests c ON c.id=p.contest_id
		JOIN contest_plagiarism_runs r ON r.contest_id=c.id
		WHERE p.contest_id=$1 AND p.status='finished' AND p.dolos_report_id=$2
		  AND r.analyzed_end_time=c.end_time
		  AND r.source_count=(SELECT COUNT(*) FROM submissions WHERE contest_id=c.id AND type='code')
		  AND r.latest_submission_at=COALESCE((SELECT MAX(created_at) FROM submissions WHERE contest_id=c.id AND type='code'),0)
		FOR UPDATE OF p
	`, contestID, expectedReportID).Scan(&foundReport); err != nil {
		if err == sql.ErrNoRows {
			return common.ErrPairNotFound
		}
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO contest_plagiarism_pair_reviews(contest_id, report_id, pair_key, status, note, updated_at)
		VALUES($1, $2, $3, $4, $5, NOW())
		ON CONFLICT(contest_id, report_id, pair_key)
		DO UPDATE SET status=EXCLUDED.status, note=EXCLUDED.note, updated_at=NOW()
	`, contestID, foundReport, key, status, note)
	if err != nil {
		return err
	}
	return tx.Commit()
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
				sentry.WithScope(func(scope *sentry.Scope) {
					scope.SetTag("worker", "dolos")
					sentry.CaptureException(err)
				})
			}
		}
	}
}

func (d *DolosService) process(ctx context.Context) error {
	if d.db == nil || d.s3 == nil || d.baseURL == "" {
		return nil
	}

	settledDeadline := time.Now().Add(-30 * time.Second).UnixMilli()

	rows, err := d.db.QueryContext(ctx, `
		SELECT id::text, end_time
		FROM contests c
		WHERE end_time <= $1 AND (
		 NOT EXISTS (SELECT 1 FROM contest_plagiarism_runs r WHERE r.contest_id=c.id)
		 OR EXISTS (
		   SELECT 1 FROM contest_plagiarism_runs r WHERE r.contest_id=c.id AND
		   (r.analyzed_end_time<>c.end_time OR r.source_count<>(SELECT COUNT(*) FROM submissions s WHERE s.contest_id=c.id AND s.type='code') OR r.latest_submission_at<>COALESCE((SELECT MAX(created_at) FROM submissions s WHERE s.contest_id=c.id AND s.type='code'),0))
		 )
		 OR EXISTS (
		   SELECT 1 FROM contest_plagiarism_reports p WHERE p.contest_id=c.id AND (
		     (p.status='pending' AND p.next_retry_at<=NOW())
		     OR (p.status IN ('processing','uploading') AND p.updated_at<NOW()-INTERVAL '3 minutes')
		     OR (p.status IN ('queued','running','unknown') AND p.dolos_report_id IS NOT NULL AND p.next_retry_at<=NOW())
		   )
		 )
		)
		ORDER BY end_time, id
		LIMIT 20
	`, settledDeadline)
	if err != nil {
		return err
	}

	type contestMeta struct {
		id      string
		endTime int64
	}
	var contests []contestMeta
	for rows.Next() {
		var c contestMeta
		if err := rows.Scan(&c.id, &c.endTime); err != nil {
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
		if err := d.processContest(ctx, c.id, c.endTime); err != nil {
			log.Errorf("Dolos contest %s: %v", c.id, err)
		}
	}

	return nil
}

type groupKey struct {
	problemID string
	language  string
}

func (d *DolosService) processContest(ctx context.Context, contestID string, expectedEndTime int64) error {
	var err error
	var previousSignature string
	// Serialize generation changes on the contest row. The run marker and report
	// reset commit together, so readers never observe a partial generation as ready.
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentEndTime int64
	if err := tx.QueryRowContext(ctx, `SELECT end_time FROM contests WHERE id=$1 FOR NO KEY UPDATE`, contestID).Scan(&currentEndTime); err != nil {
		return err
	}
	if currentEndTime != expectedEndTime || currentEndTime > time.Now().Add(-30*time.Second).UnixMilli() {
		return tx.Rollback()
	}
	var sourceCount, latestSubmission int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(created_at),0) FROM submissions WHERE contest_id=$1 AND type='code'`, contestID).Scan(&sourceCount, &latestSubmission); err != nil {
		return err
	}
	sources, err := listSourcesTx(ctx, tx, contestID)
	if err != nil {
		return err
	}
	signature := sourceSignature(currentEndTime, sources)
	grouped := make(map[groupKey][]dolosSource)
	for _, source := range sources {
		k := groupKey{problemID: source.problemID, language: source.language}
		grouped[k] = append(grouped[k], source)
	}
	txRunErr := tx.QueryRowContext(ctx, `SELECT source_signature FROM contest_plagiarism_runs WHERE contest_id=$1 FOR UPDATE`, contestID).Scan(&previousSignature)
	if txRunErr != nil && txRunErr != sql.ErrNoRows {
		return txRunErr
	}
	changed := txRunErr == sql.ErrNoRows || previousSignature != signature
	if changed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM contest_plagiarism_reports WHERE contest_id=$1`, contestID); err != nil {
			return err
		}
		for k, srcList := range grouped {
			status, message := "pending", ""
			if len(srcList) < 2 {
				status, message = "skipped", "Fewer than two distinct participants"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO contest_plagiarism_reports(contest_id,problem_id,language,status,error,updated_at) VALUES($1,$2,$3,$4,NULLIF($5,''),NOW())`, contestID, k.problemID, k.language, status, message); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO contest_plagiarism_runs(contest_id,analyzed_end_time,source_count,latest_submission_at,source_signature,updated_at) VALUES($1,$2,$3,$4,$5,NOW()) ON CONFLICT(contest_id) DO UPDATE SET analyzed_end_time=EXCLUDED.analyzed_end_time,source_count=EXCLUDED.source_count,latest_submission_at=EXCLUDED.latest_submission_at,source_signature=EXCLUDED.source_signature,updated_at=NOW()`, contestID, currentEndTime, sourceCount, latestSubmission, signature); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	for k, srcList := range grouped {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if len(srcList) < 2 {
			continue
		}

		claimToken := generateClaimToken()
		var claimed int

		claimErr := d.db.QueryRowContext(ctx, `
			UPDATE contest_plagiarism_reports p
			SET status = 'processing', claim_token = $4, attempts = p.attempts + 1,
			    next_retry_at = NOW() + INTERVAL '3 minutes', updated_at = NOW()
			WHERE p.contest_id=$1 AND p.problem_id=$2 AND p.language=$3
			  AND ((p.status='pending' AND p.next_retry_at<=NOW())
			       OR (p.status='processing' AND p.updated_at<NOW()-INTERVAL '3 minutes'))
			  AND p.attempts<6
			  AND EXISTS (SELECT 1 FROM contest_plagiarism_runs r WHERE r.contest_id=p.contest_id AND r.source_signature=$5)
			RETURNING 1
		`, contestID, k.problemID, k.language, claimToken, signature).Scan(&claimed)

		if claimErr == sql.ErrNoRows {
			continue
		}
		if claimErr != nil {
			return fmt.Errorf("claim Dolos report %s %s/%s: %w", contestID, k.problemID, k.language, claimErr)
		}

		archive, err := d.makeArchiveWithLease(ctx, contestID, k.problemID, k.language, claimToken, srcList)
		if err != nil {
			if saveErr := d.saveGroupError(ctx, contestID, k.problemID, k.language, claimToken, err, errors.Is(err, errDolosArchiveTooLarge)); saveErr != nil {
				return saveErr
			}
			continue
		}
		// Revalidate contest eligibility and the exact evidence generation at the
		// upload boundary. The provider call remains outside this transaction.
		uploadTx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var uploadEnd int64
		if err = uploadTx.QueryRowContext(ctx, `SELECT end_time FROM contests WHERE id=$1 FOR NO KEY UPDATE`, contestID).Scan(&uploadEnd); err != nil {
			uploadTx.Rollback()
			return err
		}
		var runSignature string
		if err = uploadTx.QueryRowContext(ctx, `SELECT source_signature FROM contest_plagiarism_runs WHERE contest_id=$1`, contestID).Scan(&runSignature); err != nil {
			uploadTx.Rollback()
			return err
		}
		if uploadEnd != currentEndTime || uploadEnd > time.Now().Add(-30*time.Second).UnixMilli() || runSignature != signature {
			uploadTx.Rollback()
			continue
		}
		res, err := uploadTx.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status='uploading', updated_at=NOW() WHERE contest_id=$1 AND problem_id=$2 AND language=$3 AND claim_token=$4 AND status='processing'`, contestID, k.problemID, k.language, claimToken)
		if err != nil {
			uploadTx.Rollback()
			return fmt.Errorf("persist Dolos upload phase: %w", err)
		}
		owned, err := res.RowsAffected()
		if err != nil {
			uploadTx.Rollback()
			return err
		}
		if owned == 0 {
			uploadTx.Rollback()
			continue
		}
		if err := uploadTx.Commit(); err != nil {
			return err
		}

		reportName := fmt.Sprintf("analysis-%s-%s-%s", contestID, k.problemID, k.language)
		remote, err := d.createReport(ctx, reportName, archive)
		if err != nil {
			var uploadErr *dolosUploadError
			if errors.As(err, &uploadErr) && uploadErr.ambiguous {
				// A failed POST may have been accepted remotely. Retain the ambiguity
				// and require operator resolution instead of creating duplicate datasets.
				if _, saveErr := d.db.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status='failed', error=$5, claim_token=NULL, updated_at=NOW() WHERE contest_id=$1 AND problem_id=$2 AND language=$3 AND claim_token=$4`, contestID, k.problemID, k.language, claimToken, "Dolos upload outcome is ambiguous: "+err.Error()); saveErr != nil {
					return saveErr
				}
			} else {
				permanent := uploadErr == nil || !uploadErr.retryable
				if saveErr := d.saveGroupError(ctx, contestID, k.problemID, k.language, claimToken, err, permanent); saveErr != nil {
					return saveErr
				}
			}
			continue
		}

		_, err = d.db.ExecContext(ctx, `
			UPDATE contest_plagiarism_reports
			SET status = $5,
			    dolos_report_id = $6,
			    report_url = $7,
			    error = NULL,
			    claim_token = NULL,
			    next_retry_at = NOW() + INTERVAL '15 seconds',
			    uploaded_at = NOW(),
			    updated_at = NOW()
			WHERE contest_id = $1 AND problem_id = $2 AND language = $3 AND claim_token = $4
		`, contestID, k.problemID, k.language, claimToken, remote.Status, remote.ID, remote.HTMLURL)
		if err != nil {
			return fmt.Errorf("persist accepted Dolos report %s: %w", remote.ID, err)
		}
	}

	return d.pollActiveReports(ctx, contestID)
}

func (d *DolosService) pollActiveReports(ctx context.Context, contestID string) error {
	// A process can die after provider acceptance or after exhausting its sixth
	// claim. Resolve those rows explicitly; neither case may be uploaded again.
	if _, err := d.db.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status='failed', error='Dolos upload outcome is ambiguous after worker interruption', claim_token=NULL, updated_at=NOW() WHERE contest_id=$1 AND status='uploading' AND updated_at<NOW()-INTERVAL '3 minutes'`, contestID); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status='failed', error='Dolos processing exhausted maximum claims', claim_token=NULL, updated_at=NOW() WHERE contest_id=$1 AND status='processing' AND updated_at<NOW()-INTERVAL '3 minutes' AND attempts>=6`, contestID); err != nil {
		return err
	}

	if _, err := d.db.ExecContext(ctx, `UPDATE contest_plagiarism_reports SET status='failed', error='Dolos report timed out after 30 minutes in queued/running state', updated_at=NOW() WHERE contest_id=$1 AND status IN ('queued','running','unknown') AND dolos_report_id IS NOT NULL AND COALESCE(uploaded_at, created_at) < NOW()-INTERVAL '30 minutes'`, contestID); err != nil {
		return err
	}

	rows, err := d.db.QueryContext(ctx, `
		SELECT problem_id, language, dolos_report_id, attempts
		FROM contest_plagiarism_reports
		WHERE contest_id = $1
		  AND status IN ('queued', 'running', 'unknown')
		  AND dolos_report_id IS NOT NULL
		  AND dolos_report_id != ''
		  AND next_retry_at <= NOW()
	`, contestID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type activeRep struct {
		problemID string
		language  string
		reportID  string
		attempts  int
	}
	var toPoll []activeRep
	for rows.Next() {
		var a activeRep
		if err := rows.Scan(&a.problemID, &a.language, &a.reportID, &a.attempts); err != nil {
			rows.Close()
			return err
		}
		toPoll = append(toPoll, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, a := range toPoll {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		endpoint := d.baseURL + "/reports/" + url.PathEscape(a.reportID)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			if err := d.schedulePollRetry(ctx, contestID, a.problemID, a.language, a.reportID, a.attempts); err != nil {
				return err
			}
			continue
		}
		resp, err := d.client.Do(req)
		if err != nil {
			if err := d.schedulePollRetry(ctx, contestID, a.problemID, a.language, a.reportID, a.attempts); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			if _, err := d.db.ExecContext(ctx, `
				UPDATE contest_plagiarism_reports
				SET status = 'error', error = 'Report not found on provider (404)', updated_at = NOW()
				WHERE contest_id = $1 AND problem_id = $2 AND language = $3 AND dolos_report_id=$4 AND status IN ('queued','running','unknown')
			`, contestID, a.problemID, a.language, a.reportID); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if err := d.schedulePollRetry(ctx, contestID, a.problemID, a.language, a.reportID, a.attempts); err != nil {
				return err
			}
			continue
		}

		var remote dolosReportResponse
		err = json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&remote)
		resp.Body.Close()
		if err != nil {
			if retryErr := d.schedulePollRetry(ctx, contestID, a.problemID, a.language, a.reportID, a.attempts); retryErr != nil {
				return retryErr
			}
			continue
		}

		remote.Status = validateStatus(remote.Status)

		if remote.Status == "unknown" {
			if err := d.schedulePollRetry(ctx, contestID, a.problemID, a.language, a.reportID, a.attempts); err != nil {
				return err
			}
			continue
		}
		if _, err := d.db.ExecContext(ctx, `
			UPDATE contest_plagiarism_reports
			SET status = $4,
			    error = NULLIF($5, ''),
			    next_retry_at = CASE WHEN $4 IN ('queued', 'running') THEN NOW() + INTERVAL '15 seconds' ELSE next_retry_at END,
			    updated_at = NOW()
			WHERE contest_id = $1 AND problem_id = $2 AND language = $3 AND dolos_report_id = $6 AND status IN ('queued','running','unknown')
		`, contestID, a.problemID, a.language, remote.Status, remote.Error, a.reportID); err != nil {
			return err
		}
	}
	return nil
}

func (d *DolosService) schedulePollRetry(ctx context.Context, contestID, problemID, language, reportID string, currentAttempts int) error {
	if currentAttempts >= 10 {
		_, err := d.db.ExecContext(ctx, `
			UPDATE contest_plagiarism_reports
			SET status = 'failed', error = 'Polling exceeded maximum attempts', updated_at = NOW()
			WHERE contest_id = $1 AND problem_id = $2 AND language = $3 AND dolos_report_id=$4 AND status IN ('queued','running','unknown')
		`, contestID, problemID, language, reportID)
		return err
	}
	_, err := d.db.ExecContext(ctx, `
		UPDATE contest_plagiarism_reports
		SET attempts = attempts + 1, next_retry_at = NOW() + INTERVAL '30 seconds', updated_at = NOW()
		WHERE contest_id = $1 AND problem_id = $2 AND language = $3 AND dolos_report_id=$4 AND status IN ('queued','running','unknown')
	`, contestID, problemID, language, reportID)
	return err
}

func validateStatus(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "queued", "running", "finished", "failed", "error", "purged":
		return s
	default:
		return "unknown"
	}
}

func (d *DolosService) saveGroupError(ctx context.Context, contestID, problemID, language, claimToken string, cause error, permanent bool) error {
	newStatus := "pending"
	if permanent {
		newStatus = "failed"
	}

	_, err := d.db.ExecContext(ctx, `
		UPDATE contest_plagiarism_reports
		SET status = CASE WHEN attempts >= 6 THEN 'failed' ELSE $5 END,
		    error = $6,
		    claim_token = NULL,
		    next_retry_at = CASE WHEN $5 = 'pending' AND attempts < 6 THEN NOW() + INTERVAL '1 minute' ELSE next_retry_at END,
		    updated_at = NOW()
		WHERE contest_id = $1 AND problem_id = $2 AND language = $3 AND claim_token = $4
	`, contestID, problemID, language, claimToken, newStatus, cause.Error())
	if err != nil {
		log.Errorf("save error contest %s %s/%s: %v", contestID, problemID, language, err)
		return err
	}
	return nil
}

func listSourcesTx(ctx context.Context, tx *sql.Tx, contestID string) ([]dolosSource, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT ON (user_id, problem_id)
			id::text, user_id::text, problem_id::text, COALESCE(language, '')
		FROM submissions
		WHERE contest_id = $1 AND type = 'code'
		ORDER BY user_id, problem_id, created_at DESC, id DESC
	`, contestID)
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
		canonicalLang := canonicalLanguage(s.language)
		if canonicalLang != "" {
			s.language = canonicalLang
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

type dolosLangMeta struct {
	canonical string
	extension string
}

var judge0LangToDolos = map[int]dolosLangMeta{
	71: {canonical: "python", extension: ".py"},
	54: {canonical: "cpp", extension: ".cpp"},
	50: {canonical: "c", extension: ".c"},
	62: {canonical: "java", extension: ".java"},
	63: {canonical: "javascript", extension: ".js"},
	60: {canonical: "go", extension: ".go"},
}

func canonicalLanguage(lang string) string {
	id, err := judge0.LanguageID(lang)
	if err != nil {
		return ""
	}
	if meta, ok := judge0LangToDolos[id]; ok {
		return meta.canonical
	}
	return ""
}

func (d *DolosService) makeArchive(ctx context.Context, sources []dolosSource) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	const maxObjectBytes = 1024 * 1024 // 1 MB per source code max
	const maxArchiveBytes = 10 * 1024 * 1024

	for _, s := range sources {
		readCtx, readCancel := context.WithTimeout(ctx, 30*time.Second)
		code, err := d.s3.GetObjectLimit(readCtx, s.id, maxObjectBytes)
		readCancel()
		if err != nil {
			zw.Close()
			return nil, fmt.Errorf("read source %s: %w", s.id, err)
		}

		decodedCode, err := decodeSource(code)
		if err != nil {
			zw.Close()
			return nil, fmt.Errorf("decode source %s: %w", s.id, err)
		}

		ext := languageExtension(s.language)
		// Use submission ID as the directory to avoid leaking user identifiers
		// to the external Dolos provider. Users are resolved locally via submissions.id.
		name := path.Join(s.id, s.problemID+ext)

		f, err := zw.Create(name)
		if err != nil {
			zw.Close()
			return nil, err
		}
		if _, err = f.Write([]byte(decodedCode)); err != nil {
			zw.Close()
			return nil, err
		}

		if buf.Len() >= maxArchiveBytes {
			zw.Close()
			return nil, errDolosArchiveTooLarge
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	if buf.Len() >= maxArchiveBytes {
		return nil, errDolosArchiveTooLarge
	}
	return buf.Bytes(), nil
}

func (d *DolosService) makeArchiveWithLease(ctx context.Context, contestID, problemID, language, token string, sources []dolosSource) ([]byte, error) {
	prepCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("panic in Dolos archive lease renovator: %v", r)
				sentry.CurrentHub().Recover(r)
			}
		}()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-prepCtx.Done():
				return
			case <-ticker.C:
				leaseCtx, leaseCancel := context.WithTimeout(ctx, 10*time.Second)
				res, err := d.db.ExecContext(leaseCtx, `UPDATE contest_plagiarism_reports SET updated_at=NOW() WHERE contest_id=$1 AND problem_id=$2 AND language=$3 AND claim_token=$4`, contestID, problemID, language, token)
				leaseCancel()
				if err != nil {
					cancel()
					return
				}
				n, err := res.RowsAffected()
				if err != nil || n == 0 {
					cancel()
					return
				}
			}
		}
	}()
	archive, err := d.makeArchive(prepCtx, sources)
	cancel()
	<-done
	if err != nil {
		return nil, err
	}
	var owned bool
	if err := d.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contest_plagiarism_reports WHERE contest_id=$1 AND problem_id=$2 AND language=$3 AND claim_token=$4)`, contestID, problemID, language, token).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return nil, context.Canceled
	}
	return archive, nil
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
		return nil, &dolosUploadError{err: err, ambiguous: true}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		statusErr := fmt.Errorf("Dolos returned HTTP %d: %s", resp.StatusCode, string(data))
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests {
			return nil, &dolosUploadError{err: statusErr, retryable: true}
		}
		if resp.StatusCode >= 500 {
			return nil, &dolosUploadError{err: statusErr, ambiguous: true}
		}
		return nil, &dolosUploadError{err: statusErr}
	}

	var result dolosReportResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&result); err != nil {
		return nil, &dolosUploadError{err: err, ambiguous: true}
	}
	if result.ID == "" {
		return nil, &dolosUploadError{err: fmt.Errorf("Dolos response omitted report ID"), ambiguous: true}
	}
	if result.HTMLURL == "" {
		result.HTMLURL = result.URL
	}
	result.Status = validateStatus(result.Status)

	return &result, nil
}

func languageExtension(canonicalLang string) string {
	for _, meta := range judge0LangToDolos {
		if meta.canonical == canonicalLang {
			return meta.extension
		}
	}
	return ".txt"
}

func NewDolosWorker(lc fx.Lifecycle, d *DolosService) {
	workerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				defer func() {
					if r := recover(); r != nil {
						log.Errorf("panic in Dolos worker: %v", r)
						sentry.CurrentHub().Recover(r)
						sentry.Flush(2 * time.Second)
					}
				}()
				d.Run(workerCtx)
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}
