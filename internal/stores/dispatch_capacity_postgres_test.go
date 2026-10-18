package stores_test

import (
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dispatchObjects(t *testing.T) *s3.S3 {
	t.Helper()
	objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			return
		}
		if strings.HasSuffix(r.URL.Path, "testcases.json") {
			w.Write([]byte(`[{"index":0,"input":""},{"index":1,"input":""},{"index":2,"input":""}]`))
		} else {
			w.Write([]byte(`["hello","hello","hello"]`))
		}
	}))
	t.Cleanup(objects.Close)
	for key, value := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "", "AWS_REGION": "us-east-1", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "AWS_CONFIG_FILE": "/dev/null", "AWS_EC2_METADATA_DISABLED": "true", "S3_SUBMISSIONS_BUCKET": "test", "AWS_ENDPOINT_URL_S3": objects.URL} {
		t.Setenv(key, value)
	}
	return s3.NewS3Client()
}

func TestQueueFullPreservesScoredWorkPostgres(t *testing.T) {
	db := postgresTestDB(t)
	user, contest, problem, _, _ := executionFixture(t, db, 0)
	var posts atomic.Int32
	var full atomic.Bool
	full.Store(true)
	judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			n := posts.Add(1)
			if n > 1 && full.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"error":"queue is full"}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `[{"token":"job-%d"}]`, n)
			return
		}
		results := make([]judge0.SubmissionStatusResponse, 0)
		for _, token := range strings.Split(r.URL.Query().Get("tokens"), ",") {
			results = append(results, judge0.SubmissionStatusResponse{Token: token, Status: judge0.StatusResponse{ID: 3}, Time: json.RawMessage(`"0.001"`)})
		}
		json.NewEncoder(w).Encode(map[string]any{"submissions": results})
	}))
	defer judge.Close()
	t.Setenv("JUDGE0_URL", judge.URL)
	t.Setenv("JUDGE0_BATCH_SIZE", "1")
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "")
	storage := stores.NewStorage(db)
	service := services.NewSubmissionService(storage, dispatchObjects(t), judge0.NewClient())
	ctx := context.Background()
	sub, err := service.CreateSubmission(ctx, user, models.Code, &dto.SubmitSubmissionRequest{ContestID: contest, ProblemID: problem, Language: "python", Code: "print('hello')\n"})
	if err != nil {
		t.Fatal(err)
	}
	var reservations, tokens int
	if err := db.QueryRow(`SELECT sum(dispatch_count),count(judge0_token) FROM submission_executions WHERE submission_id=$1`, sub).Scan(&reservations, &tokens); err != nil || reservations != 1 || tokens != 1 || posts.Load() != 2 {
		t.Fatal("partial dispatch or unsent reservation lost", reservations, tokens, posts.Load(), err)
	}
	var deferrals int32
	for range 5 {
		readyExecutions(t, db, sub)
		before := posts.Load()
		sweepCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		service.RecoverJudge0(sweepCtx)
		cancel()
		assertSubmission(t, db, sub, "pending", 0, 0)
		var completed, charged int
		// Polling may briefly lock every child, leaving nothing for SKIP LOCKED on the first sweep.
		if err := db.QueryRow(`SELECT count(*) FILTER (WHERE status='accepted'),count(*) FILTER (WHERE status='pending' AND (dispatch_count<>0 OR recovery_failures<>0)) FROM submission_executions WHERE submission_id=$1`, sub).Scan(&completed, &charged); err != nil || completed != 1 || charged != 0 || posts.Load()-before > 1 {
			t.Fatal("capacity blocked polling or consumed retries", completed, charged, posts.Load()-before, err)
		}
		deferrals += posts.Load() - before
	}
	if deferrals < 4 {
		t.Fatal("did not exercise more than three capacity rejections", deferrals)
	}
	full.Store(false)
	for range 3 {
		readyExecutions(t, db, sub)
		service.RecoverJudge0(ctx)
	}
	assertSubmission(t, db, sub, "accepted", 3, 0)
	var score, results int
	if err := db.QueryRow(`SELECT score FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&score); err != nil || score != 10 {
		t.Fatal(score, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM test_case_results WHERE submission_id=$1`, sub).Scan(&results); err != nil || results != 3 {
		t.Fatal(results, err)
	}
}

func TestDialFailurePreservesScoredWorkPostgres(t *testing.T) {
	db := postgresTestDB(t)
	user, contest, problem, _, _ := executionFixture(t, db, 0)
	var posts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `[{"token":"job-%d"}]`, posts.Add(1))
			return
		}
		results := make([]judge0.SubmissionStatusResponse, 0)
		for _, token := range strings.Split(r.URL.Query().Get("tokens"), ",") {
			results = append(results, judge0.SubmissionStatusResponse{Token: token, Status: judge0.StatusResponse{ID: 3}, Time: json.RawMessage(`"0.001"`)})
		}
		json.NewEncoder(w).Encode(map[string]any{"submissions": results})
	})
	judge := httptest.NewServer(handler)
	endpoint := judge.URL
	judge.Close()
	t.Setenv("JUDGE0_URL", endpoint)
	t.Setenv("JUDGE0_BATCH_SIZE", "1")
	service := services.NewSubmissionService(stores.NewStorage(db), dispatchObjects(t), judge0.NewClient())
	ctx := context.Background()
	sub, err := service.CreateSubmission(ctx, user, models.Code, &dto.SubmitSubmissionRequest{ContestID: contest, ProblemID: problem, Language: "python", Code: "print('hello')\n"})
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		readyExecutions(t, db, sub)
		sweepCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		service.RecoverJudge0(sweepCtx)
		cancel()
		assertSubmission(t, db, sub, "pending", 0, 0)
		var charged int
		if err := db.QueryRow(`SELECT count(*) FROM submission_executions WHERE submission_id=$1 AND (status<>'pending' OR dispatch_count<>0 OR recovery_failures<>0)`, sub).Scan(&charged); err != nil || charged != 0 {
			t.Fatal("dial failure consumed retries or finalized work", charged, err)
		}
	}
	listener, err := net.Listen("tcp", strings.TrimPrefix(endpoint, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	judge = httptest.NewUnstartedServer(handler)
	judge.Listener.Close()
	judge.Listener = listener
	judge.Start()
	defer judge.Close()
	for range 3 {
		readyExecutions(t, db, sub)
		service.RecoverJudge0(ctx)
	}
	assertSubmission(t, db, sub, "accepted", 3, 0)
	var score, results int
	if err := db.QueryRow(`SELECT score FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&score); err != nil || score != 10 {
		t.Fatal(score, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM test_case_results WHERE submission_id=$1`, sub).Scan(&results); err != nil || results != 3 || posts.Load() != 3 {
		t.Fatal(results, posts.Load(), err)
	}
}

func TestDeferredDispatchFencingPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, _ := executionFixture(t, db, 1)
	snapshotExecutions(t, db, sub)
	store, ctx := stores.NewExecutionStore(db), context.Background()
	if _, err := db.Exec(`UPDATE submission_executions SET dispatch_count=1,recovery_failures=1 WHERE submission_id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimRecovery(ctx, 1, false)
	if err != nil || len(claims) != 1 {
		t.Fatal(claims, err)
	}
	e := claims[0]
	wrongClaim := e
	wrongClaim.Claim = "replaced"
	if err := store.DeferDispatch(ctx, wrongClaim); err != nil {
		t.Fatal(err)
	}
	assertCounters := func(dispatches, failures int) {
		t.Helper()
		var d, f int
		if err := db.QueryRow(`SELECT dispatch_count,recovery_failures FROM submission_executions WHERE id=$1`, e.ID).Scan(&d, &f); err != nil || d != dispatches || f != failures {
			t.Fatal(d, f, dispatches, failures, err)
		}
	}
	assertCounters(2, 2)
	if err := store.DeferDispatch(ctx, e); err != nil {
		t.Fatal(err)
	}
	assertCounters(1, 1)
	var deadline time.Time
	if err := db.QueryRow(`SELECT next_recovery_at FROM submission_executions WHERE id=$1`, e.ID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if err := store.DeferDispatch(ctx, e); err != nil {
		t.Fatal(err)
	}
	assertCounters(1, 1)
	if err := store.ReleaseRecovery(ctx, claims, []bool{false}); err != nil {
		t.Fatal(err)
	}
	var claim string
	var next time.Time
	if err := db.QueryRow(`SELECT recovery_claim,next_recovery_at FROM submission_executions WHERE id=$1`, e.ID).Scan(&claim, &next); err != nil || claim != "" || !next.Equal(deadline) {
		t.Fatal("old release replaced capacity retry", claim, next, deadline, err)
	}
	readyExecutions(t, db, sub)
	newClaims, err := store.ClaimRecovery(ctx, 1, false)
	if err != nil || len(newClaims) != 1 {
		t.Fatal(newClaims, err)
	}
	if err := store.DeferDispatch(ctx, e); err != nil {
		t.Fatal(err)
	}
	assertCounters(2, 2)
	if err := store.SaveAttemptToken(ctx, e.ID, newClaims[0].Attempt, "accepted-dispatch"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeferDispatch(ctx, newClaims[0]); err != nil {
		t.Fatal(err)
	}
	assertCounters(2, 0)
}
