package stores_test

import (
	"app/internal/controllers"
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/s3"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func readyExecutions(t *testing.T, db *sql.DB, sub string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE submission_executions SET claim_until='-infinity',next_recovery_at='-infinity' WHERE submission_id=$1`, sub); err != nil {
		t.Fatal(err)
	}
}
func snapshotExecutions(t *testing.T, db *sql.DB, sub string) {
	t.Helper()
	output := "hello\n"
	payload, _ := json.Marshal(judge0.SubmissionRequest{SourceCode: "print('hello')\n", LanguageID: 71, Stdin: "", ExpectedOutput: &output, CPUTimeLimit: 1, MemoryLimit: 262144})
	if _, err := db.Exec(`UPDATE submission_executions SET dispatch_payload=$2 WHERE submission_id=$1`, sub, payload); err != nil {
		t.Fatal(err)
	}
	readyExecutions(t, db, sub)
}

func TestRecoveryLargeCompilerOutputPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, items := executionFixture(t, db, 1)
	store := stores.NewExecutionStore(db)
	if err := store.SaveAttemptToken(context.Background(), items[0].ID, "", "compiler"); err != nil {
		t.Fatal(err)
	}
	diagnostics := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("E", 4<<20)))
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"submissions": []any{map[string]any{
			"token": "compiler", "status": map[string]int{"id": 6}, "compile_output": diagnostics,
		}}})
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	readyExecutions(t, db, sub)
	services.NewSubmissionService(stores.NewStorage(db), nil, judge0.NewClient()).RecoverJudge0(context.Background())
	assertSubmission(t, db, sub, "failed_to_process", 0, 0)
	var stored string
	if err := db.QueryRow(`SELECT compile_output FROM submission_executions WHERE id=$1`, items[0].ID).Scan(&stored); err != nil || len(stored) != 4096 || gets.Load() != 1 {
		t.Fatalf("diagnostic bytes=%d GETs=%d error=%v", len(stored), gets.Load(), err)
	}
}

func TestRecoveryFirstTokenResetsObservationBudgetPostgres(t *testing.T) {
	for _, callback := range []bool{false, true} {
		t.Run(fmt.Sprint(callback), func(t *testing.T) {
			db := postgresTestDB(t)
			_, _, _, sub, _ := executionFixture(t, db, 1)
			store := stores.NewExecutionStore(db)
			ctx := context.Background()
			if _, err := db.Exec(`UPDATE submission_executions SET dispatch_count=1 WHERE submission_id=$1`, sub); err != nil {
				t.Fatal(err)
			}
			var current models.Execution
			for range 2 {
				readyExecutions(t, db, sub)
				items, err := store.ClaimRecovery(ctx, 1, false)
				if err != nil || len(items) != 1 {
					t.Fatal(items, err)
				}
				current = items[0]
				if current.DispatchCount == 2 {
					if err := store.ReleaseRecovery(ctx, items, []bool{false}); err != nil {
						t.Fatal(err)
					}
				}
			}
			publish := func() error {
				if callback {
					return store.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: current.ID, Attempt: current.Attempt, Token: "known", Status: "wrong_answer", Stdout: json.RawMessage("null")})
				}
				return store.SaveAttemptToken(ctx, current.ID, current.Attempt, "known")
			}
			// A first token is durable, then the worker crashes before healthy release.
			if err := publish(); err != nil {
				t.Fatal(err)
			}
			assertFailures := func(want int) {
				t.Helper()
				var got int
				if err := db.QueryRow(`SELECT recovery_failures FROM submission_executions WHERE id=$1`, current.ID).Scan(&got); err != nil || got != want {
					t.Fatalf("failures=%d want=%d error=%v", got, want, err)
				}
			}
			assertFailures(0)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			service := services.NewSubmissionService(stores.NewStorage(db), nil, judge0.NewClient())
			for failure := 1; failure <= 3; failure++ {
				readyExecutions(t, db, sub)
				service.RecoverJudge0(ctx)
				assertFailures(failure)
				if failure < 3 {
					assertSubmission(t, db, sub, "pending", 0, 0)
					if err := publish(); err != nil {
						t.Fatal(err)
					}
					assertFailures(failure) // Duplicate tokens cannot replenish retries.
				}
			}
			assertSubmission(t, db, sub, "judge_error", 0, 0)
		})
	}
}

func TestRecoveryPollingProgressDuringBlockedDispatchPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, items := executionFixture(t, db, 41)
	snapshotExecutions(t, db, sub)
	for _, e := range items[:40] {
		if err := stores.NewExecutionStore(db).SaveAttemptToken(context.Background(), e.ID, "", e.ID); err != nil {
			t.Fatal(err)
		}
	}
	postStarted, unblockPost := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			close(postStarted)
			<-unblockPost
			w.WriteHeader(502)
			return
		}
		// Keep polling finalization from locking the tokenless row before its claim.
		select {
		case <-postStarted:
		case <-r.Context().Done():
			return
		}
		tokens := strings.Split(r.URL.Query().Get("tokens"), ",")
		results := make([]judge0.SubmissionStatusResponse, len(tokens))
		for i, token := range tokens {
			results[i] = judge0.SubmissionStatusResponse{Token: token, Status: judge0.StatusResponse{ID: 3}}
		}
		json.NewEncoder(w).Encode(map[string]any{"submissions": results})
	}))
	defer server.Close()
	defer close(unblockPost)
	t.Setenv("JUDGE0_URL", server.URL)
	t.Setenv("JUDGE0_TIMEOUT_MS", "10000")
	t.Setenv("JUDGE0_BATCH_SIZE", "20")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		services.NewSubmissionService(stores.NewStorage(db), nil, judge0.NewClient()).RecoverJudge0(ctx)
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-postStarted:
	case <-ctx.Done():
		t.Fatal("tokenless dispatch did not start")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM submission_executions WHERE submission_id=$1 AND status='accepted'`, sub).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 40 {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("blocked dispatch held up polling: accepted=%d/40", count)
		}
	}
	cancel()
	<-done
	var active bool
	if err := db.QueryRow(`SELECT claim_until>now() FROM submission_executions WHERE id=$1`, items[40].ID).Scan(&active); err != nil || active {
		t.Fatalf("shutdown left dispatch lease active=%v error=%v", active, err)
	}
}

func TestRecoveryClaimsAndFencingPostgres(t *testing.T) {
	db := postgresTestDB(t)
	user, contest, _, sub, _ := executionFixture(t, db, 24)
	snapshotExecutions(t, db, sub)
	ctx := context.Background()
	claims := make(chan []models.Execution, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := stores.NewExecutionStore(db).ClaimRecovery(ctx, 4, false)
			claims <- items
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]models.Execution{}
	for batch := range claims {
		for _, e := range batch {
			if _, ok := seen[e.ID]; ok {
				t.Fatal("claimed twice", e.ID)
			}
			seen[e.ID] = e
		}
	}
	if len(seen) != 24 {
		t.Fatalf("claims=%d", len(seen))
	}
	store := stores.NewExecutionStore(db)
	items, err := store.ClaimRecovery(ctx, 100, false)
	if err != nil || len(items) != 0 {
		t.Fatalf("active leases reclaimed: %+v %v", items, err)
	}
	readyExecutions(t, db, sub)
	newClaims, err := store.ClaimRecovery(ctx, 100, false)
	if err != nil || len(newClaims) != 24 {
		t.Fatalf("expired leases: %d %v", len(newClaims), err)
	}
	for _, e := range newClaims {
		old := seen[e.ID]
		if old.Attempt == e.Attempt || old.Claim == e.Claim || e.DispatchCount != 2 {
			t.Fatalf("unfenced lease: %+v %+v", old, e)
		}
		if err := store.SaveAttemptToken(ctx, old.ID, old.Attempt, "stale-"+old.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: old.ID, Attempt: old.Attempt, Claim: old.Claim, Status: "wrong_answer"}); err != nil {
			t.Fatal(err)
		}
		// A stale release must not reset the new lease or its persisted counters.
		if err := store.ReleaseRecovery(ctx, []models.Execution{old}, []bool{true}); err != nil {
			t.Fatal(err)
		}
		var token sql.NullString
		var failures int
		if err := db.QueryRow(`SELECT judge0_token,recovery_failures FROM submission_executions WHERE id=$1`, e.ID).Scan(&token, &failures); err != nil || token.Valid || failures != 2 {
			t.Fatalf("stale mutation: %v %d %v", token, failures, err)
		}
		if err := store.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: e.ID, Attempt: e.Attempt, Claim: e.Claim, Token: "new-" + e.ID, Status: "accepted", Runtime: 1}); err != nil {
			t.Fatal(err)
		}
		if err := store.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: e.ID, Attempt: old.Attempt, Token: "stale-" + e.ID, Status: "wrong_answer", Runtime: 999}); err == nil { // Conflicting token may be rejected; either way the terminal result must stay unchanged.
			var status string
			if err := db.QueryRow(`SELECT status FROM submission_executions WHERE id=$1`, e.ID).Scan(&status); err != nil || status != "accepted" {
				t.Fatal(status, err)
			}
		}
	}
	assertSubmission(t, db, sub, "accepted", 24, 0)
	var score, count int
	if err := db.QueryRow(`SELECT score FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&score); err != nil || score != 10 {
		t.Fatal(score, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM test_case_results WHERE submission_id=$1`, sub).Scan(&count); err != nil || count != 24 {
		t.Fatal(count, err)
	}
}

func TestRecoveryWorkerCrashAndAmbiguousDispatchPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, executions := executionFixture(t, db, 1)
	snapshotExecutions(t, db, sub)
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "https://callback.test")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "secret")
	t.Setenv("JUDGE0_TIMEOUT_MS", "100")
	var posts atomic.Int32
	var oldAttempt string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct {
				Submissions []judge0.SubmissionRequest `json:"submissions"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if posts.Add(1) == 1 {
				mu.Lock()
				oldAttempt = body.Submissions[0].CallbackURL
				mu.Unlock()
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return // accepted, response lost
			}
			w.WriteHeader(201)
			w.Write([]byte(`[{"token":"recovered"}]`))
			return
		}
		w.Write([]byte(`{"submissions":[{"token":"recovered","status":{"id":3},"time":"0.025","memory":1024}]}`))
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	storage := stores.NewStorage(db)
	client := judge0.NewClient()
	ctx := context.Background()
	// Crash after claiming, before sending. A new replica must wait for expiry.
	crashed, err := storage.Executions.ClaimRecovery(ctx, 1, false)
	if err != nil || len(crashed) != 1 {
		t.Fatal(crashed, err)
	}
	services.NewSubmissionService(storage, nil, client).RecoverJudge0(ctx)
	if posts.Load() != 0 {
		t.Fatal("active crash lease was stolen")
	}
	readyExecutions(t, db, sub)
	services.NewSubmissionService(storage, nil, client).RecoverJudge0(ctx) // ambiguous second dispatch
	assertSubmission(t, db, sub, "pending", 0, 0)
	var graceSeconds float64
	if err := db.QueryRow(`SELECT extract(epoch FROM next_recovery_at-now()) FROM submission_executions WHERE id=$1`, executions[0].ID).Scan(&graceSeconds); err != nil || graceSeconds < 29 || graceSeconds > 31 {
		t.Fatal("missing callback grace period", graceSeconds, err)
	}
	services.NewSubmissionService(storage, nil, client).RecoverJudge0(ctx)
	if posts.Load() != 1 {
		t.Fatal("retried before callback grace expired", posts.Load())
	}
	readyExecutions(t, db, sub)
	services.NewSubmissionService(storage, nil, client).RecoverJudge0(ctx) // third attempt receives token
	if posts.Load() != 2 {
		t.Fatal("expected one ambiguous and one successful dispatch", posts.Load())
	}
	mu.Lock()
	callbackURL := oldAttempt
	mu.Unlock()
	controller := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, client), nil, client)
	w := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPut, callbackURL, strings.NewReader(`{"token":"old-job","status":{"id":4}}`)), w)
	c.SetParamNames("execution_id")
	c.SetParamValues(executions[0].ID)
	if err := controller.Judge0Callback(c); err != nil || w.Code != 200 {
		t.Fatal(w.Code, err)
	}
	assertSubmission(t, db, sub, "pending", 0, 0)
	readyExecutions(t, db, sub)
	services.NewSubmissionService(storage, nil, client).RecoverJudge0(ctx)
	assertSubmission(t, db, sub, "accepted", 25, 1024)
	// Even a valid late callback cannot replace the terminal verdict or award twice.
	for range 2 {
		if err := storage.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: executions[0].ID, Token: "recovered", Status: "accepted", Runtime: 900}); err != nil {
			t.Fatal(err)
		}
	}
	assertSubmission(t, db, sub, "accepted", 25, 1024)
}

func TestLostDispatchResponseCallbackDuringGracePostgres(t *testing.T) {
	db := postgresTestDB(t)
	user, contest, _, sub, _ := executionFixture(t, db, 0)
	storage := stores.NewStorage(db)
	ctx := context.Background()
	jobs := []judge0.SubmissionRequest{{SourceCode: "print(1)", LanguageID: 71}}
	executions, err := storage.Executions.InsertBatch(ctx, sub, []int{0}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	e := executions[0]
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close() // Judge0 accepted the job, but its response was lost.
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	client := judge0.NewClient()
	if _, err := client.CreateBatch(ctx, jobs); err == nil {
		t.Fatal("expected a lost dispatch response")
	}
	services.NewSubmissionService(storage, nil, client).RecoverJudge0(ctx)
	if posts.Load() != 1 {
		t.Fatal("initial grace period did not prevent retry", posts.Load())
	}
	ss := services.NewSubmissionService(storage, nil, client)
	for range 2 {
		if err := ss.HandleJudge0Callback(ctx, e.ID, judge0.SubmissionStatusResponse{Attempt: e.Attempt, Token: "original", Status: judge0.StatusResponse{ID: 3}, Time: json.RawMessage(`"0.025"`)}); err != nil {
			t.Fatal(err)
		}
	}
	readyExecutions(t, db, sub)
	services.NewSubmissionService(stores.NewStorage(db), nil, client).RecoverJudge0(ctx)
	if posts.Load() != 1 {
		t.Fatal("callback completion was redispatched", posts.Load())
	}
	assertSubmission(t, db, sub, "accepted", 25, 0)
	var score int
	if err := db.QueryRow(`SELECT score FROM rankings WHERE user_id=$1 AND contest_id=$2`, user, contest).Scan(&score); err != nil || score != 10 {
		t.Fatal(score, err)
	}
}

func TestRecoveryExhaustionAndReplicaSweepsPostgres(t *testing.T) {
	for _, withToken := range []bool{false, true} {
		t.Run(fmt.Sprint(withToken), func(t *testing.T) {
			db := postgresTestDB(t)
			_, _, _, sub, execs := executionFixture(t, db, 4)
			snapshotExecutions(t, db, sub)
			if withToken {
				for _, e := range execs {
					if err := stores.NewExecutionStore(db).SaveAttemptToken(context.Background(), e.ID, "", e.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			for pass := 0; pass < 4; pass++ {
				readyExecutions(t, db, sub)
				var wg sync.WaitGroup
				for range 3 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						services.NewSubmissionService(stores.NewStorage(db), nil, judge0.NewClient()).RecoverJudge0(context.Background())
					}()
				}
				wg.Wait()
				if pass < 2 {
					assertSubmission(t, db, sub, "pending", 0, 0)
				}
			}
			assertSubmission(t, db, sub, "judge_error", 0, 0)
			var score int
			if err := db.QueryRow(`SELECT coalesce(sum(score),0) FROM rankings`).Scan(&score); err != nil || score != 0 {
				t.Fatal(score, err)
			}
			var min, max int
			if err := db.QueryRow(`SELECT min(recovery_failures),max(recovery_failures) FROM submission_executions WHERE submission_id=$1`, sub).Scan(&min, &max); err != nil || min != max || max > 4 {
				t.Fatal(min, max, err)
			}
		})
	}
}

func TestCallbackAndRecoveryRacePostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, executions := executionFixture(t, db, 1)
	snapshotExecutions(t, db, sub)
	ctx := context.Background()
	store := stores.NewExecutionStore(db)
	claims, err := store.ClaimRecovery(ctx, 1, false)
	if err != nil || len(claims) != 1 {
		t.Fatal(claims, err)
	}
	attempt := claims[0]
	if err := store.SaveAttemptToken(ctx, attempt.ID, attempt.Attempt, "job"); err != nil {
		t.Fatal(err)
	}
	readyExecutions(t, db, sub)
	firstPolls, err := store.ClaimRecovery(ctx, 1, true)
	if err != nil || len(firstPolls) != 1 {
		t.Fatal(firstPolls, err)
	}
	readyExecutions(t, db, sub)
	newPolls, err := store.ClaimRecovery(ctx, 1, true)
	if err != nil || len(newPolls) != 1 {
		t.Fatal(newPolls, err)
	}
	stale, current := firstPolls[0], newPolls[0]
	if stale.Attempt != current.Attempt || stale.Claim == current.Claim {
		t.Fatal("token poll identity changed", stale, current)
	}
	if err := store.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: stale.ID, Attempt: stale.Attempt, Claim: stale.Claim, Token: "job", Status: "judge_error"}); err != nil {
		t.Fatal(err)
	}
	assertSubmission(t, db, sub, "pending", 0, 0)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	start := make(chan struct{})
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			claim := ""
			if i%2 == 0 {
				claim = current.Claim
			}
			errs <- store.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: executions[0].ID, Attempt: current.Attempt, Claim: claim, Token: "job", Status: "accepted", Runtime: 125, Memory: 1024})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertSubmission(t, db, sub, "accepted", 125, 1024)
	var results, score int
	if err := db.QueryRow(`SELECT count(*) FROM test_case_results WHERE submission_id=$1`, sub).Scan(&results); err != nil || results != 1 {
		t.Fatal(results, err)
	}
	if err := db.QueryRow(`SELECT sum(score) FROM rankings`).Scan(&score); err != nil || score != 10 {
		t.Fatal(score, err)
	}
}

func TestRecoveryResumesAfterOutagePostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, executions := executionFixture(t, db, 1)
	snapshotExecutions(t, db, sub)
	store := stores.NewExecutionStore(db)
	ctx := context.Background()
	if err := store.SaveAttemptToken(ctx, executions[0].ID, "", "job"); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("tokened execution redispatched")
		}
		switch requests.Add(1) {
		case 1:
			w.WriteHeader(502)
		case 2:
			w.Write([]byte(`{"submissions":[{"token":"job","status":{"id":2}}]}`))
		default:
			w.Write([]byte(`{"submissions":[{"token":"job","status":{"id":3},"time":"0.1"}]}`))
		}
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	for i := range 3 {
		readyExecutions(t, db, sub)
		services.NewSubmissionService(stores.NewStorage(db), nil, judge0.NewClient()).RecoverJudge0(ctx)
		if i == 0 {
			var failures int
			if err := db.QueryRow(`SELECT recovery_failures FROM submission_executions WHERE id=$1`, executions[0].ID).Scan(&failures); err != nil || failures != 1 {
				t.Fatal(failures, err)
			}
		}
		if i == 1 {
			var failures int
			if err := db.QueryRow(`SELECT recovery_failures FROM submission_executions WHERE id=$1`, executions[0].ID).Scan(&failures); err != nil || failures != 0 {
				t.Fatal(failures, err)
			}
		}
	}
	assertSubmission(t, db, sub, "accepted", 100, 0)
}

func TestLegacyTokenlessRecoveryPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, _ := executionFixture(t, db, 2)
	if _, err := db.Exec(`UPDATE submissions SET language='python' WHERE id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	readyExecutions(t, db, sub)
	objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "testcases.json"):
			w.Write([]byte(`[{"index":0,"input":""},{"index":1,"input":"Unicode नमस्ते\n"}]`))
		case strings.HasSuffix(r.URL.Path, "answers.json"):
			w.Write([]byte(`["empty\n","Unicode नमस्ते\n"]`))
		case strings.HasSuffix(r.URL.Path, sub):
			w.Write([]byte(base64.StdEncoding.EncodeToString([]byte("print(input())\n"))))
		default:
			t.Error("unexpected object read", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer objects.Close()
	for key, value := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "", "AWS_REGION": "us-east-1", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "AWS_CONFIG_FILE": "/dev/null", "AWS_EC2_METADATA_DISABLED": "true", "S3_SUBMISSIONS_BUCKET": "test", "AWS_ENDPOINT_URL_S3": objects.URL} {
		t.Setenv(key, value)
	}
	var dispatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var reserved int
			if err := db.QueryRow(`SELECT count(*) FROM submission_executions WHERE submission_id=$1 AND judge0_token IS NULL AND recovery_claim<>''`, sub).Scan(&reserved); err != nil || reserved != 1 {
				t.Error("unsent jobs claimed ahead of their dispatch", reserved, err)
			}
			var payload struct {
				Submissions []judge0.SubmissionRequest `json:"submissions"`
			}
			json.NewDecoder(r.Body).Decode(&payload)
			if len(payload.Submissions) != 1 {
				t.Error(payload)
				w.WriteHeader(500)
				return
			}
			source, err := base64.StdEncoding.DecodeString(payload.Submissions[0].SourceCode)
			if err != nil || string(source) != "print(input())\n" || payload.Submissions[0].EnableNetwork {
				t.Error("legacy job corrupted", payload, err)
			}
			n := dispatches.Add(1)
			w.WriteHeader(201)
			fmt.Fprintf(w, `[{"token":"legacy-%d"}]`, n)
			return
		}
		tokens := strings.Split(r.URL.Query().Get("tokens"), ",")
		results := make([]judge0.SubmissionStatusResponse, len(tokens))
		for i, token := range tokens {
			results[i] = judge0.SubmissionStatusResponse{Token: token, Status: judge0.StatusResponse{ID: 3}}
		}
		json.NewEncoder(w).Encode(map[string]any{"submissions": results})
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	storage := stores.NewStorage(db)
	client := judge0.NewClient()
	ss := services.NewSubmissionService(storage, s3.NewS3Client(), client)
	ss.RecoverJudge0(context.Background())
	if dispatches.Load() != 2 {
		t.Fatal("legacy jobs not recovered", dispatches.Load())
	}
	readyExecutions(t, db, sub)
	services.NewSubmissionService(storage, nil, client).RecoverJudge0(context.Background())
	assertSubmission(t, db, sub, "accepted", 0, 0)
	if dispatches.Load() != 2 {
		t.Fatal("successful dispatch was repeated", dispatches.Load())
	}
	var snapshots int
	if err := db.QueryRow(`SELECT count(*) FROM submission_executions WHERE submission_id=$1 AND dispatch_payload IS NOT NULL`, sub).Scan(&snapshots); err != nil || snapshots != 2 {
		t.Fatal(snapshots, err)
	}
}

func TestPreparedDispatchSnapshotPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, _ := executionFixture(t, db, 0)
	store := stores.NewExecutionStore(db)
	ctx := context.Background()
	output := ""
	jobs := []judge0.SubmissionRequest{{SourceCode: "print('नमस्ते')\n", LanguageID: 71, Stdin: "", ExpectedOutput: &output, CPUTimeLimit: 1, MemoryLimit: 262144}, {SourceCode: "print('नमस्ते')\n", LanguageID: 71, Stdin: "two\n\x00नमस्ते", ExpectedOutput: &output, CPUTimeLimit: 1, MemoryLimit: 262144}}
	executions, err := store.InsertBatch(ctx, sub, []int{0, 1}, jobs)
	if err != nil || len(executions) != 2 {
		t.Fatal(executions, err)
	}
	claims, err := store.ClaimRecovery(ctx, 2, false)
	if err != nil || len(claims) != 0 {
		t.Fatal("initial dispatch lease missing", claims, err)
	}
	for i, e := range executions {
		var payload []byte
		var attempt string
		var count int
		if err := db.QueryRow(`SELECT dispatch_payload,dispatch_attempt,dispatch_count FROM submission_executions WHERE id=$1`, e.ID).Scan(&payload, &attempt, &count); err != nil {
			t.Fatal(err)
		}
		var snapshot judge0.SubmissionRequest
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			t.Fatal(err)
		}
		if attempt == "" || attempt != e.Attempt || count != 1 || snapshot.SourceCode != jobs[i].SourceCode || snapshot.Stdin != jobs[i].Stdin || snapshot.ExpectedOutput == nil || *snapshot.ExpectedOutput != "" || snapshot.EnableNetwork {
			t.Fatalf("invalid prepared snapshot: %+v %s %d", snapshot, attempt, count)
		}
	}
	readyExecutions(t, db, sub)
	claims, err = store.ClaimRecovery(ctx, 2, false)
	if err != nil || len(claims) != 2 {
		t.Fatal(claims, err)
	}
	for _, claim := range claims {
		if claim.DispatchCount != 2 || claim.Attempt == executions[claim.TestCaseIndex].Attempt {
			t.Fatal("initial attempt was not fenced", claim)
		}
	}
}

func TestBulkRecoveryReleaseFencingPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, items := executionFixture(t, db, 4)
	snapshotExecutions(t, db, sub)
	store := stores.NewExecutionStore(db)
	ctx := context.Background()
	for _, e := range items {
		if err := store.SaveAttemptToken(ctx, e.ID, "", e.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE submission_executions SET recovery_failures=2 WHERE submission_id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimRecovery(ctx, 4, true)
	if err != nil || len(claims) != 4 {
		t.Fatal(claims, err)
	}
	// Replace one claim while the old batch is still applying its observations.
	if _, err := db.Exec(`UPDATE submission_executions SET claim_until='-infinity',next_recovery_at='-infinity' WHERE id=$1`, claims[0].ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.ClaimRecovery(ctx, 1, true)
	if err != nil || len(replacement) != 1 || replacement[0].ID != claims[0].ID {
		t.Fatal(replacement, err)
	}
	outcomes := []bool{true, false, true, false}
	if err := store.ReleaseRecovery(ctx, claims, outcomes); err != nil {
		t.Fatal(err)
	}
	for i, e := range claims {
		var failures int
		var active bool
		var claim string
		var delay float64
		if err := db.QueryRow(`SELECT recovery_failures,claim_until>now(),recovery_claim,extract(epoch FROM next_recovery_at-now()) FROM submission_executions WHERE id=$1`, e.ID).Scan(&failures, &active, &claim, &delay); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if failures != 4 || !active || claim != replacement[0].Claim {
				t.Fatal("stale bulk release changed replacement", failures, active, claim)
			}
		} else {
			wantFailures, wantDelay := 3, 45.0
			if outcomes[i] {
				wantFailures, wantDelay = 0, 15
			}
			if failures != wantFailures || active || delay < wantDelay-2 || delay > wantDelay {
				t.Fatal("outcome assigned to wrong execution", i, failures, active, delay)
			}
		}
	}
	// Bulk releases and callbacks must acquire child locks in the same order.
	start := make(chan struct{})
	errs := make(chan error, len(claims)+1)
	go func() {
		<-start
		errs <- store.ReleaseRecovery(ctx, claims, outcomes)
	}()
	for _, e := range claims {
		go func(e models.Execution) {
			<-start
			errs <- store.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: e.ID, Attempt: e.Attempt, Token: e.Judge0Token, Status: "accepted", Runtime: 1})
		}(e)
	}
	close(start)
	for range len(claims) + 1 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	assertSubmission(t, db, sub, "accepted", 4, 0)
}
