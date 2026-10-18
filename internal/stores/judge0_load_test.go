package stores_test

import (
	"app/internal/common"
	"app/internal/controllers"
	"app/internal/db"
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// Opt-in: real PostgreSQL and service paths, simulated S3 and Judge0; no production writes.
func TestJudge0ContestLoadPostgres(t *testing.T) {
	if os.Getenv("RUN_JUDGE0_LOAD_TEST") != "1" {
		t.Skip("set RUN_JUDGE0_LOAD_TEST=1 and a disposable TEST_DATABASE_URL")
	}
	const participants, cases = 600, 10
	for _, scenario := range []struct {
		name       string
		callbacks  bool
		queueLimit int
	}{
		{name: "polling"},
		{name: "callbacks_with_three_recovery_workers", callbacks: true},
		{name: "queue_limit_100", queueLimit: 100},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			callbacks := scenario.callbacks
			config := db.LoadDBConfig()
			db := postgresTestDB(t)
			db.SetMaxOpenConns(config.MaxOpenConns)
			db.SetMaxIdleConns(config.MaxIdleConns)
			db.SetConnMaxLifetime(config.ConnMaxLifetime)
			_, contest, problem, empty, _ := executionFixture(t, db, 0)
			if _, err := db.Exec(`DELETE FROM submissions WHERE id=$1`, empty); err != nil {
				t.Fatal(err)
			}
			rows, err := db.Query(`INSERT INTO users(id,name,email,usn,current_year,department)
    SELECT id,'Load',id||'@example.test',id,1,'CS' FROM (SELECT gen_random_uuid()::text id FROM generate_series(1,$1)) u RETURNING id`, participants)
			if err != nil {
				t.Fatal(err)
			}
			users := make([]string, 0, participants)
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				users = append(users, id)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					w.WriteHeader(200)
					return
				}
				if strings.HasSuffix(r.URL.Path, "testcases.json") {
					items := make([]map[string]any, cases)
					for i := range items {
						items[i] = map[string]any{"index": i, "input": ""}
					}
					json.NewEncoder(w).Encode(items)
				} else if strings.HasSuffix(r.URL.Path, "answers.json") {
					answers := make([]string, cases)
					for i := range answers {
						answers[i] = "hello\n"
					}
					json.NewEncoder(w).Encode(answers)
				} else {
					t.Error("unexpected object read", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer objects.Close()
			for k, v := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "", "AWS_REGION": "us-east-1", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "AWS_CONFIG_FILE": "/dev/null", "AWS_EC2_METADATA_DISABLED": "true", "S3_SUBMISSIONS_BUCKET": "test", "AWS_ENDPOINT_URL_S3": objects.URL} {
				t.Setenv(k, v)
			}
			var posts, gets, admitted, refused atomic.Int32
			var creationFinished atomic.Bool
			var queueMu sync.Mutex
			queued := make(map[string]bool)
			judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					var body struct {
						Submissions []judge0.SubmissionRequest `json:"submissions"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					posts.Add(1)
					out := make([]map[string]string, len(body.Submissions))
					for i, j := range body.Submissions {
						u, err := url.Parse(j.CallbackURL)
						if err != nil {
							t.Error(err)
							return
						}
						id := u.Path[strings.LastIndex(u.Path, "/")+1:]
						if code, err := base64.StdEncoding.DecodeString(j.SourceCode); err != nil || string(code) != "print('hello')\n" {
							t.Error("incorrect source encoding", err)
						}
						out[i] = map[string]string{"token": id}
					}
					if scenario.queueLimit > 0 {
						queueMu.Lock()
						if len(queued)+len(out) > scenario.queueLimit {
							queueMu.Unlock()
							refused.Add(1)
							w.WriteHeader(http.StatusServiceUnavailable)
							w.Write([]byte(`{"error":"queue is full"}`))
							return
						}
						for _, job := range out {
							queued[job["token"]] = true
						}
						queueMu.Unlock()
						if creationFinished.Load() {
							// Simulated execution frees capacity independently of result polling.
							time.AfterFunc(10*time.Millisecond, func() {
								queueMu.Lock()
								defer queueMu.Unlock()
								for _, job := range out {
									delete(queued, job["token"])
								}
							})
						}
					}
					admitted.Add(int32(len(out)))
					w.WriteHeader(201)
					json.NewEncoder(w).Encode(out)
					return
				}
				gets.Add(1)
				if strings.Contains(r.URL.Query().Get("fields"), "stdout") {
					t.Error("accepted jobs should not download stdout")
				}
				tokens := strings.Split(r.URL.Query().Get("tokens"), ",")
				if len(tokens) > 20 {
					t.Error("oversized poll batch", len(tokens))
				}
				out := make([]map[string]any, len(tokens))
				for i, token := range tokens {
					status := 3
					queueMu.Lock()
					if queued[token] {
						status = 1
					}
					queueMu.Unlock()
					out[i] = map[string]any{"token": token, "status": map[string]int{"id": status}, "time": "0.001", "memory": 1024}
				}
				json.NewEncoder(w).Encode(map[string]any{"submissions": out})
			}))
			defer judge.Close()
			t.Setenv("JUDGE0_URL", judge.URL)
			t.Setenv("JUDGE0_CALLBACK_BASE_URL", "http://callback.test")
			t.Setenv("JUDGE0_CALLBACK_SECRET", "load-test-secret")
			t.Setenv("JUDGE0_BATCH_SIZE", "20")
			t.Setenv("JUDGE0_TIMEOUT_MS", "10000")
			t.Setenv("SUBMISSION_RATE_LIMIT_SECONDS", "5")
			client := judge0.NewClient()
			storage := stores.NewStorage(db)
			service := services.NewSubmissionService(storage, s3.NewS3Client(), client)
			start := make(chan struct{})
			errs := make(chan error, participants*cases*2+3)
			latencies := make([]time.Duration, participants)
			var wg sync.WaitGroup
			began := time.Now()
			for i, user := range users {
				wg.Add(1)
				go func(i int, user string) {
					defer wg.Done()
					<-start
					at := time.Now()
					_, err := service.CreateSubmission(context.Background(), user, models.Code, &dto.SubmitSubmissionRequest{ContestID: contest, ProblemID: problem, Language: "python", Code: base64.StdEncoding.EncodeToString([]byte("print('hello')\n"))})
					latencies[i] = time.Since(at)
					if err != nil {
						errs <- fmt.Errorf("create %s: %w", user, err)
					}
				}(i, user)
			}
			close(start)
			wg.Wait()
			creation := time.Since(began)
			if scenario.queueLimit > 0 {
				beforeRuns := posts.Load()
				controller := controllers.NewSubmissionController(service, nil, client)
				for _, user := range users {
					wg.Add(1)
					go func(user string) {
						defer wg.Done()
						w := httptest.NewRecorder()
						c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(`{"language":"python","code":"print(1)"}`)), w)
						c.Set(common.AUTH_USER_ID, user)
						if err := controller.Run(c); err != nil || w.Code != http.StatusTooManyRequests {
							errs <- fmt.Errorf("run during scored backlog: HTTP %d error=%v", w.Code, err)
						}
					}(user)
				}
				wg.Wait()
				if posts.Load() != beforeRuns {
					t.Fatal("Run dispatched into scored backlog")
				}
				t.Logf("rejected %d optional Runs while scored submissions were pending", participants)
			}
			// Hold the initial jobs until the burst finishes so queue saturation is deterministic.
			creationFinished.Store(true)
			queueMu.Lock()
			clear(queued)
			queueMu.Unlock()
			if len(errs) > 0 {
				close(errs)
				for err := range errs {
					t.Error(err)
				}
				t.FailNow()
			}
			if _, err := db.Exec(`UPDATE submission_executions SET next_recovery_at='-infinity'`); err != nil {
				t.Fatal(err)
			}
			type delivery struct{ id, attempt, user string }
			deliveries := make(map[string][]delivery)
			if callbacks {
				rows, err := db.Query(`SELECT e.id,e.dispatch_attempt,s.user_id FROM submission_executions e JOIN submissions s ON s.id=e.submission_id`)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var d delivery
					if err := rows.Scan(&d.id, &d.attempt, &d.user); err != nil {
						t.Fatal(err)
					}
					deliveries[d.user] = append(deliveries[d.user], d)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			finished := time.Now()
			if callbacks {
				controller := controllers.NewSubmissionController(service, nil, client)
				for _, items := range deliveries {
					wg.Add(1)
					go func(items []delivery) {
						defer wg.Done()
						for _, d := range items {
							for range 2 {
								body := fmt.Sprintf(`{"token":%q,"status":{"id":3},"time":"0.001","memory":1024}`, d.id)
								req := httptest.NewRequest(http.MethodPut, client.CallbackURL(d.id, d.attempt), strings.NewReader(body)).WithContext(ctx)
								w := httptest.NewRecorder()
								c := echo.New().NewContext(req, w)
								c.SetParamNames("execution_id")
								c.SetParamValues(d.id)
								if err := controller.Judge0Callback(c); err != nil || w.Code != 200 {
									errs <- fmt.Errorf("callback: status=%d error=%v", w.Code, err)
								}
							}
						}
					}(items)
				}
			}
			replicas := 1
			if callbacks {
				replicas = 3
			}
			for range replicas {
				wg.Add(1)
				go func() {
					defer wg.Done()
					service := services.NewSubmissionService(stores.NewStorage(db), nil, client)
					for ctx.Err() == nil {
						sweepCtx, stopSweep := context.WithTimeout(ctx, 30*time.Second)
						service.RecoverJudge0(sweepCtx)
						stopSweep()
						var pending int
						if err := db.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE status='pending'`).Scan(&pending); err != nil {
							errs <- err
							return
						}
						if pending == 0 {
							return
						}
						select {
						case <-time.After(15 * time.Second):
						case <-ctx.Done():
							return
						}
					}
				}()
			}
			wg.Wait()
			finalize := time.Since(finished)
			close(errs)
			for err := range errs {
				t.Error(err)
			}
			for _, check := range []struct {
				query string
				want  int
			}{
				{`SELECT count(*) FROM submissions WHERE status='accepted'`, participants},
				{`SELECT count(*) FROM submission_executions WHERE status='accepted' AND dispatch_count=1`, participants * cases},
				{`SELECT count(*) FROM test_case_results WHERE status='pass'`, participants * cases},
				{`SELECT count(*) FROM rankings WHERE score=10`, participants},
				{`SELECT count(*) FROM submission_executions WHERE recovery_failures<>0 AND status='pending'`, 0},
			} {
				var n int
				if err := db.QueryRow(check.query).Scan(&n); err != nil || n != check.want {
					t.Errorf("%s: got%d want%d error=%v", check.query, n, check.want, err)
				}
			}
			if admitted.Load() != participants*cases || (scenario.queueLimit == 0 && posts.Load() != participants) {
				t.Error("unexpected physical dispatch count", admitted.Load(), posts.Load())
			}
			if scenario.queueLimit > 0 && refused.Load() == 0 {
				t.Error("bounded queue never exercised capacity recovery")
			}
			// Cancellation may require observing an unfinished batch again on the next sweep.
			if !callbacks && gets.Load() < participants*cases/20 {
				t.Error("missing polling observations", gets.Load())
			}
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			stats := db.Stats()
			t.Logf("participants=%d cases=%d queueLimit=%d DBpool=%d DBidle=%d create=%s p95=%s p99=%s finalize=%s POST=%d GET=%d admitted=%d refused=%d DBwaits=%d DBwaitTotal=%s idleClosed=%d", participants, cases, scenario.queueLimit, config.MaxOpenConns, config.MaxIdleConns, creation, latencies[participants*95/100], latencies[participants*99/100], finalize, posts.Load(), gets.Load(), admitted.Load(), refused.Load(), stats.WaitCount, stats.WaitDuration, stats.MaxIdleClosed)
		})
	}
}
