// Fixture writes run outside measurements. Credentials are read from protected files.
package main

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/stores"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type User struct {
	UID     string `json:"uid"`
	Token   string `json:"token"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"`
}
type Problem struct {
	ID    string `json:"id"`
	Cases int    `json:"cases"`
	Kind  string `json:"kind"`
}
type Manifest struct {
	RunID       string    `json:"run_id"`
	API         string    `json:"api"`
	Environment string    `json:"environment"`
	Contest     string    `json:"contest"`
	Users       []User    `json:"users"`
	Problems    []Problem `json:"problems"`
	Keys        []string  `json:"object_keys"`
	APIKey      string    `json:"api_key"`
	Created     int64     `json:"created"`
	Cleaned     bool      `json:"cleaned"`
}

var ctx = context.Background()
var client = &http.Client{Timeout: 30 * time.Second}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func save(path string, m any) {
	b, e := json.MarshalIndent(m, "", "  ")
	must(e)
	must(os.MkdirAll(filepath.Dir(path), 0700))
	must(os.WriteFile(path+".tmp", b, 0600))
	must(os.Rename(path+".tmp", path))
}
func request(method, url, token string, body any, out any) int {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r, e := http.NewRequest(method, url, bytes.NewReader(b))
	must(e)
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res, e := client.Do(r)
	must(e)
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	must(e)
	if res.StatusCode >= 200 && res.StatusCode < 300 && out != nil {
		must(json.Unmarshal(data, out))
	}
	return res.StatusCode
}
func requireStatus(got, want int) {
	if got != want {
		panic(fmt.Sprintf("HTTP %d, wanted %d (body suppressed)", got, want))
	}
}
func authClient() *auth.Client {
	app, e := firebase.NewApp(ctx, nil, option.WithCredentialsFile(os.Getenv("FIREBASE_SERVICE_ACCOUNT_PATH")))
	must(e)
	a, e := app.Auth(ctx)
	must(e)
	return a
}
func token(a *auth.Client, m *Manifest, u *User) {
	signed, e := a.CustomToken(ctx, u.UID)
	must(e)
	var r struct {
		Token   string `json:"idToken"`
		Refresh string `json:"refreshToken"`
	}
	requireStatus(request("POST", "https://identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key="+m.APIKey, "", map[string]any{"token": signed, "returnSecureToken": true}, &r), 200)
	u.Token = r.Token
	u.Refresh = r.Refresh
	u.Expires = time.Now().Unix() + 3500
}
func owned(m Manifest) {
	if !regexp.MustCompile(`^pbperf-[0-9]{8}T[0-9]{6}Z-[a-z0-9]+$`).MatchString(m.RunID) || m.Contest != "" && m.Contest != m.RunID {
		panic("not a test-owned manifest")
	}
	for _, u := range m.Users {
		if !strings.HasPrefix(u.UID, m.RunID+"-u") {
			panic("foreign user")
		}
	}
}
func main() {
	// Suppress SDK/SQL diagnostics, which can contain credential-bearing URLs.
	defer func() {
		if recover() != nil {
			fmt.Fprintln(os.Stderr, "fixture operation failed; protected inputs/credentials or contract need checking (diagnostics suppressed)")
			os.Exit(1)
		}
	}()
	if len(os.Args) < 2 {
		panic("command required")
	}
	cmd := os.Args[1]
	must(godotenv.Load())
	if os.Getenv("PERF_ENV_FILE") != "" {
		must(godotenv.Overload(os.Getenv("PERF_ENV_FILE")))
	}
	db, e := sql.Open("pgx", os.Getenv("DB_ADDR"))
	must(e)
	defer db.Close()
	db.SetMaxOpenConns(2)
	must(db.PingContext(ctx))
	api := os.Getenv("PERF_API")
	if api == "" {
		api = "https://api.recruitment.pointblank.club"
	}
	if cmd == "probe" {
		var list []models.Contest
		requireStatus(request("GET", api+"/contests/list", "", nil, &list), 200)
		n := 0
		for _, c := range list {
			var yes bool
			must(db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM contests WHERE id=$1 AND name=$2)", c.ID, c.Name).Scan(&yes))
			if yes {
				n++
			}
		}
		fmt.Printf("API/database public contest matches: %d/%d\n", n, len(list))
		if n == 0 || n != len(list) {
			panic("target mismatch")
		}
		return
	}
	if len(os.Args) < 3 {
		panic("manifest path required")
	}
	path := os.Args[2]
	var m Manifest
	if cmd == "provision" {
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			panic("manifest exists")
		}
		run := os.Getenv("PERF_RUN_ID")
		m = Manifest{RunID: run, API: api, Environment: os.Getenv("PERF_ENVIRONMENT"), Created: time.Now().Unix()}
		owned(m)
		if m.Environment != "production" && m.Environment != "isolated" {
			panic("environment required")
		}
		front, e := godotenv.Read("../pb-recruitment-website/.env")
		must(e)
		m.APIKey = front["VITE_FIREBASE_API_KEY"]
		if os.Getenv("PERF_FIREBASE_API_KEY") != "" {
			m.APIKey = os.Getenv("PERF_FIREBASE_API_KEY")
		}
		save(path, m)
	} else {
		b, e := os.ReadFile(path)
		must(e)
		must(json.Unmarshal(b, &m))
		owned(m)
		if m.Cleaned && cmd != "verify-cleanup" {
			panic("already cleaned")
		}
	}
	storage := stores.NewStorage(db)
	switch cmd {
	case "provision":
		// Require explicit target DB/API agreement before any write.
		var public []models.Contest
		requireStatus(request("GET", api+"/contests/list", "", nil, &public), 200)
		if len(public) == 0 {
			panic("no target witness")
		}
		var witness bool
		must(db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM contests WHERE id=$1 AND name=$2)", public[0].ID, public[0].Name).Scan(&witness))
		if !witness {
			panic("database target mismatch")
		}
		a := authClient()
		n := 3
		if os.Getenv("PERF_USERS") != "" {
			n, e = strconv.Atoi(os.Getenv("PERF_USERS"))
			must(e)
		}
		if n < 1 || n > 10000 {
			panic("user bound")
		}
		for i := 0; i < n; i++ {
			u := User{UID: fmt.Sprintf("%s-u%04d", m.RunID, i)}
			m.Users = append(m.Users, u)
			save(path, m)
			rec, e := a.CreateUser(ctx, (&auth.UserToCreate{}).UID(u.UID).Email(u.UID+"@example.invalid").DisplayName("Synthetic performance participant"))
			must(e)
			must(storage.Users.CreateUser(ctx, rec, &dto.CreateUserRequest{Name: "Synthetic performance participant", USN: u.UID, MobileNumber: "0000000000", CurrentYear: 1, Department: "TEST"}))
			token(a, &m, &m.Users[i])
			save(path, m)
		}
		m.Contest = m.RunID
		save(path, m)
		now := time.Now().UnixMilli()
		e := storage.Contests.CreateContest(ctx, &models.Contest{ID: m.Contest, Name: "[PERF TEST] " + m.RunID, Description: "Dedicated synthetic performance test. No recruitment ranking.", RegistrationStartTime: now - 60000, RegistrationEndTime: now + 172800000, StartTime: now - 1000, EndTime: now + 172800000, EligibleTo: []int{1}})
		must(e)
		object := s3.NewS3Client()
		// Pre-record deterministic IDs and object keys before any fixture publication write.
		for _, spec := range []struct {
			kind  string
			count int
		}{{"small", 1}, {"representative", 10}, {"fanout", 20}, {"memory", 1}, {"mcq", 0}, {"mcq_wrong", 0}} {
			req := &dto.CreateProblemRequest{Name: m.RunID + "-" + spec.kind, Description: base64.StdEncoding.EncodeToString([]byte("Read two integers and print their sum. MCQ: 2+2 = option 0.")), Score: 10, Type: models.Code, TimeLimit: 1000, MemoryLimit: 256, Options: []string{}, Answer: []int{}}
			if strings.HasPrefix(spec.kind, "mcq") {
				req.Type = models.MCQ
				req.Options = []string{"4", "5"}
				req.Answer = []int{0}
			} else {
				if spec.kind == "memory" {
					req.MemoryLimit = 32
				}
				for j := 0; j < spec.count; j++ {
					req.Testcases = append(req.Testcases, dto.CreateTestCaseRequest{Input: fmt.Sprintf("%d %d\n", j+1, j+3), ExpectedOutput: fmt.Sprintf("%d\n", 2*j+4), IsSample: j == 0})
				}
			}

			id := m.RunID + "-" + spec.kind
			descriptionKey := fmt.Sprintf("problems/%s/%s/description.json", m.Contest, id)
			testcaseKey := fmt.Sprintf("problems/%s/%s/testcases.json", m.Contest, id)
			answerKey := fmt.Sprintf("problems/%s/%s/answers.json", m.Contest, id)
			m.Problems = append(m.Problems, Problem{ID: id, Cases: spec.count, Kind: spec.kind})
			m.Keys = append(m.Keys, descriptionKey)
			if spec.count > 0 {
				m.Keys = append(m.Keys, testcaseKey, answerKey)
			}
			save(path, m)
			description, _ := json.Marshal(map[string]string{"description": req.Description})
			must(object.PutObject(ctx, descriptionKey, string(description)))
			if spec.count > 0 {
				var inputs []map[string]any
				var outputs []string
				for i, tc := range req.Testcases {
					inputs = append(inputs, map[string]any{"index": i, "input": tc.Input, "is_sample": tc.IsSample})
					outputs = append(outputs, tc.ExpectedOutput)
				}
				b, _ := json.Marshal(inputs)
				must(object.PutObject(ctx, testcaseKey, string(b)))
				b, _ = json.Marshal(outputs)
				must(object.PutObject(ctx, answerKey, string(b)))
			} else {
				testcaseKey = ""
			}
			must(storage.Problems.CreateProblem(ctx, &models.Problem{ID: id, ContestID: m.Contest, Name: req.Name, Description: descriptionKey, Type: req.Type, Score: req.Score, Options: req.Options, Answer: req.Answer, Testcases: testcaseKey, TimeLimit: req.TimeLimit, MemoryLimit: req.MemoryLimit}))

		}
		for _, u := range m.Users {
			requireStatus(request("POST", api+"/contests/"+m.Contest+"/registration", u.Token, map[string]string{"action": "register"}, nil), 200)
		}
		fmt.Printf("Provisioned %d distinct users, one test contest, %d problems\n", len(m.Users), len(m.Problems))
	case "progress":
		var registered int
		must(db.QueryRowContext(ctx, "SELECT count(*) FROM contest_registrations WHERE contest_id=$1 AND user_id=ANY($2)", m.Contest, userIDs(m)).Scan(&registered))
		ready := 0
		for _, u := range m.Users {
			if u.Token != "" {
				ready++
			}
		}
		b, _ := json.Marshal(map[string]any{"recorded_users": len(m.Users), "credentials_ready": ready, "registered_users": registered, "problems": len(m.Problems)})
		fmt.Println(string(b))
	case "refresh":
		a := authClient()
		for i := range m.Users {
			token(a, &m, &m.Users[i])
			save(path, m)
		}
		fmt.Println("Refreshed protected credentials outside measurement")
	case "seed-history":
		seedHistory(db, &m, path)
	case "configuration":
		rows, e := db.QueryContext(ctx, `SELECT c.id,(end_time-start_time)/60000,count(p.id) FILTER(WHERE p.type='code'),count(p.id) FILTER(WHERE p.type='mcq') FROM contests c LEFT JOIN problems p ON p.contest_id=c.id WHERE c.id<>$1 GROUP BY c.id ORDER BY c.id`, m.Contest)
		must(e)
		defer rows.Close()
		for rows.Next() {
			var id string
			var mins, code, mcq int64
			must(rows.Scan(&id, &mins, &code, &mcq))
			fmt.Printf("contest=%s duration_minutes=%d coding=%d mcq=%d\n", id, mins, code, mcq)
		}
		must(rows.Err())
	case "verify-cleanup":
		verifyCleanup(db, m)
	case "snapshot", "reconcile":
		snapshot(db, m, path, cmd == "reconcile")
	case "cleanup":
		cleanup(db, storage, m, path)
	default:
		panic("unknown command")
	}
}
func snapshot(db *sql.DB, m Manifest, path string, reconcile bool) {
	var pending, executions, terminal, tokenless, failures int
	var oldest float64
	must(db.QueryRowContext(ctx, `SELECT count(*),COALESCE(max(extract(epoch from now())-created_at),0) FROM submissions WHERE contest_id=$1 AND status='pending'`, m.Contest).Scan(&pending, &oldest))
	must(db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE e.status<>'pending'),count(*) FILTER(WHERE e.status='pending' AND judge0_token IS NULL),COALESCE(sum(dispatch_count-1) FILTER(WHERE dispatch_count>1),0) FROM submission_executions e JOIN submissions s ON s.id=e.submission_id WHERE s.contest_id=$1`, m.Contest).Scan(&executions, &terminal, &tokenless, &failures))
	out := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "pending_submissions": pending, "oldest_pending_seconds": oldest, "logical_executions": executions, "terminal_executions": terminal, "tokenless_pending": tokenless, "extra_dispatch_attempts": failures}

	var connections, active int
	if db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE state='active') FROM pg_stat_activity WHERE datname=current_database()`).Scan(&connections, &active) == nil {
		out["database_connections"] = connections
		out["database_active_queries"] = active
	}
	var commits, rollbacks, reads, hits int64
	var readMS, writeMS float64
	if db.QueryRowContext(ctx, `SELECT xact_commit,xact_rollback,blks_read,blks_hit,blk_read_time,blk_write_time FROM pg_stat_database WHERE datname=current_database()`).Scan(&commits, &rollbacks, &reads, &hits, &readMS, &writeMS) == nil {
		out["database_stats"] = map[string]any{"commits": commits, "rollbacks": rollbacks, "block_reads": reads, "block_hits": hits, "block_read_ms": readMS, "block_write_ms": writeMS}
	}
	if reconcile {
		rows, e := db.QueryContext(ctx, `SELECT s.id,s.user_id,s.problem_id,s.status,s.type,s.created_at,count(e.id),count(e.id) FILTER(WHERE e.status<>'pending') FROM submissions s LEFT JOIN submission_executions e ON e.submission_id=s.id WHERE s.contest_id=$1 GROUP BY s.id ORDER BY s.id`, m.Contest)
		must(e)
		defer rows.Close()
		subs := []map[string]any{}
		for rows.Next() {
			var id, uid, pid, status, kind string
			var created int64
			var jobs, done int
			must(rows.Scan(&id, &uid, &pid, &status, &kind, &created, &jobs, &done))
			subs = append(subs, map[string]any{"id": id, "uid": uid, "problem": pid, "status": status, "type": kind, "created": created, "jobs": jobs, "done": done})
		}
		must(rows.Err())
		save(filepath.Join(filepath.Dir(path), m.RunID+"-reconciliation.json"), subs)
		erows, e := db.QueryContext(ctx, `SELECT e.id,e.submission_id,COALESCE(e.judge0_token,''),e.status,e.dispatch_count FROM submission_executions e JOIN submissions s ON s.id=e.submission_id WHERE s.contest_id=$1 ORDER BY e.id`, m.Contest)
		must(e)
		jobs := []map[string]any{}
		for erows.Next() {
			var id, parent, token, status string
			var attempts int
			must(erows.Scan(&id, &parent, &token, &status, &attempts))
			jobs = append(jobs, map[string]any{"id": id, "submission": parent, "judge0_token": token, "status": status, "dispatch_attempts": attempts})
		}
		must(erows.Err())
		erows.Close()
		save(filepath.Join(filepath.Dir(path), m.RunID+"-executions.json"), jobs)

		var badScores int
		must(db.QueryRowContext(ctx, `SELECT count(*) FROM rankings r WHERE contest_id=$1 AND score<>(SELECT COALESCE(sum(p.score),0) FROM problems p WHERE p.contest_id=r.contest_id AND EXISTS(SELECT 1 FROM submissions s WHERE s.contest_id=p.contest_id AND s.problem_id=p.id AND s.user_id=r.user_id AND s.status='accepted'))`, m.Contest).Scan(&badScores))
		out["score_mismatches"] = badScores
		out["saved_submissions"] = len(subs)
		if pending > 0 || executions != terminal || badScores > 0 {
			out["passed"] = false
		} else {
			out["passed"] = true
		}
		save(filepath.Join(filepath.Dir(path), m.RunID+"-database-summary.json"), out)
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}
func cleanup(db *sql.DB, storage *stores.Storage, m Manifest, path string) {
	// Exact contest and user allowlists; refuse cleanup with foreign registrations or work still pending.
	var unsafe int
	if m.Contest != "" {
		var name string
		e := db.QueryRowContext(ctx, "SELECT name FROM contests WHERE id=$1", m.Contest).Scan(&name)
		if e != sql.ErrNoRows {
			must(e)
			if name != "[PERF TEST] "+m.RunID {
				panic("contest ownership changed")
			}
		}
	}
	must(db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM contest_registrations WHERE contest_id=$1 AND NOT(user_id=ANY($2)))+(SELECT count(*) FROM rankings WHERE contest_id=$1 AND NOT(user_id=ANY($2)))`, m.Contest, userIDs(m)).Scan(&unsafe))
	if unsafe != 0 {
		panic("foreign registrations or rankings")
	}
	must(db.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE contest_id=$1 AND (status='pending' OR NOT (user_id=ANY($2)))`, m.Contest, userIDs(m)).Scan(&unsafe))
	if unsafe != 0 {
		panic("pending or foreign submissions")
	}
	rows, e := db.QueryContext(ctx, `SELECT id FROM submissions WHERE contest_id=$1 AND type='code'`, m.Contest)
	must(e)
	for rows.Next() {
		var id string
		must(rows.Scan(&id))
		m.Keys = append(m.Keys, id)
	}
	must(rows.Err())
	rows.Close()
	save(path, m)
	var submissionKeys = map[string]bool{}
	for _, key := range m.Keys {
		if strings.HasPrefix(key, "problems/"+m.Contest+"/") {
			continue
		}
		var yes bool
		must(db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM submissions WHERE id=$1 AND contest_id=$2 AND user_id=ANY($3))", key, m.Contest, userIDs(m)).Scan(&yes))
		if !yes {
			panic("foreign object key")
		}
		submissionKeys[key] = true
	}
	object := s3.NewS3Client()
	for _, key := range m.Keys {
		must(object.DeleteObject(ctx, key))
	}
	if m.Contest != "" {
		must(storage.Contests.DeleteContest(ctx, m.Contest))
	}
	a := authClient()
	for _, u := range m.Users {
		var foreign int
		must(db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM submissions WHERE user_id=$1)+(SELECT count(*) FROM contest_registrations WHERE user_id=$1)`, u.UID).Scan(&foreign))
		if foreign != 0 {
			panic("user has foreign work")
		}
		_, e = db.ExecContext(ctx, `DELETE FROM submission_rate_limits WHERE user_id=$1 OR user_id=$2`, u.UID, "run:user:"+u.UID)
		must(e)
		_, e = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1 AND usn=$1`, u.UID)
		must(e)
		e = a.DeleteUser(ctx, u.UID)
		if e != nil && !auth.IsUserNotFound(e) {
			must(e)
		}
	}
	m.Cleaned = true
	for i := range m.Users {
		m.Users[i].Token = ""
		m.Users[i].Refresh = ""
	}
	save(path, m)
	fmt.Println("Deleted exact test-owned resources; Judge0 retention and shared run:capacity row are untouched")
}
func userIDs(m Manifest) []string {
	a := []string{}
	for _, u := range m.Users {
		a = append(a, u.UID)
	}
	return a
}

func seedHistory(db *sql.DB, m *Manifest, path string) {
	if len(m.Users) == 0 {
		panic("no users")
	}
	var p Problem
	for _, v := range m.Problems {
		if v.Kind == "small" {
			p = v
		}
	}
	u := m.Users[0]
	var existing int
	must(db.QueryRowContext(ctx, "SELECT count(*) FROM submissions WHERE user_id=$1 AND problem_id=$2", u.UID, p.ID).Scan(&existing))
	if existing != 0 {
		panic("seed only a fresh user/problem")
	}
	for i := 0; i < 41; i++ {
		source := "print(-1)"
		expected := "wrong_answer"
		if i == 0 {
			source = "a,b=map(int,input().split());print(a+b)"
			expected = "accepted"
		}
		var response struct {
			ID string `json:"submission_id"`
		}
		// Never retry an ambiguous POST. Reconcile the exact test contest instead.
		requireStatus(request("POST", m.API+"/submission/submit", u.Token, map[string]any{"contest_id": m.Contest, "problem_id": p.ID, "type": "code", "language": "python", "code": source + "\n# " + m.RunID + " seed"}, &response), 201)
		save(filepath.Join(filepath.Dir(path), fmt.Sprintf(m.RunID+"-seed-%02d.json", i)), map[string]any{"id": response.ID, "expected": expected, "run_id": m.RunID})
		end := time.Now().Add(90 * time.Second)
		for {
			var status struct {
				Status string `json:"status"`
			}
			requireStatus(request("GET", m.API+"/submission/"+response.ID+"/status", u.Token, nil, &status), 200)
			if status.Status != "pending" {
				if status.Status != expected {
					panic("incorrect seed verdict")
				}
				break
			}
			if time.Now().After(end) {
				panic("seed unresolved")
			}
			time.Sleep(2 * time.Second)
		}
		time.Sleep(6 * time.Second)
	}
	for page, want := range []int{20, 20, 1} {
		var list struct {
			Submissions []models.Submission `json:"submissions"`
		}
		requireStatus(request("GET", fmt.Sprintf("%s/submission/list?problem_id=%s&page=%d", m.API, p.ID, page), u.Token, nil, &list), 200)
		if len(list.Submissions) != want {
			panic("history pagination")
		}
		if page == 2 && list.Submissions[0].Status != models.Accepted {
			panic("older accepted disappeared")
		}
	}
	fmt.Println("Seeded and verified 41 real submissions: older accepted + 40 wrong answers, history pages 20/20/1")
}

func verifyCleanup(db *sql.DB, m Manifest) {
	if !m.Cleaned {
		panic("cleanup not recorded")
	}
	var rows int
	must(db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM contests WHERE id=$1)+(SELECT count(*) FROM submissions WHERE contest_id=$1)+(SELECT count(*) FROM users WHERE id=ANY($2))+(SELECT count(*) FROM submission_rate_limits WHERE user_id=ANY($2))`, m.Contest, userIDs(m)).Scan(&rows))
	if rows != 0 {
		panic("application cleanup incomplete")
	}
	a := authClient()
	for _, u := range m.Users {
		_, err := a.GetUser(ctx, u.UID)
		if !auth.IsUserNotFound(err) {
			panic("authentication cleanup unverified")
		}
	}
	object := s3.NewS3Client()
	for _, key := range m.Keys {
		_, err := object.GetObject(ctx, key)
		if err != common.KeyNotFoundError {
			panic("object cleanup unverified")
		}
	}
	b, _ := json.Marshal(map[string]any{"run_id": m.RunID, "application_rows_remaining": 0, "firebase_users_remaining": 0, "object_keys_remaining": 0, "object_keys_checked": len(m.Keys), "judge0_native_retention": "untouched"})
	fmt.Println(string(b))
}
