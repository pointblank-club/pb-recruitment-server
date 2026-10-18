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

func TestDispatchOutagePreservesScoredWorkPostgres(t *testing.T) {
	for _, failure := range []string{"queue full", "connection refused"} {
		t.Run(failure, func(t *testing.T) {
			queueFull := failure == "queue full"
			db := postgresTestDB(t)
			user, contest, problem, _, _ := executionFixture(t, db, 0)
			var posts, admitted atomic.Int32
			var outage atomic.Bool
			outage.Store(true)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					n := posts.Add(1)
					if queueFull && n > 1 && outage.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
						w.Write([]byte(`{"error":"queue is full"}`))
						return
					}
					admitted.Add(1)
					w.WriteHeader(http.StatusCreated)
					fmt.Fprintf(w, `[{"token":"job-%d"}]`, n)
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
			wantAccepted, wantPosts := 1, int32(2)
			if !queueFull {
				judge.Close()
				wantAccepted, wantPosts = 0, 0
			}
			defer func() { judge.Close() }()
			t.Setenv("JUDGE0_URL", endpoint)
			t.Setenv("JUDGE0_BATCH_SIZE", "1")
			t.Setenv("JUDGE0_CALLBACK_BASE_URL", "")
			t.Setenv("JUDGE0_CALLBACK_SECRET", "")
			service := services.NewSubmissionService(stores.NewStorage(db), dispatchObjects(t), judge0.NewClient())
			ctx := context.Background()
			sub, err := service.CreateSubmission(ctx, user, models.Code, &dto.SubmitSubmissionRequest{ContestID: contest, ProblemID: problem, Language: "python", Code: "print('hello')\n"})
			if err != nil {
				t.Fatal(err)
			}
			var reservations, tokens int
			if err := db.QueryRow(`SELECT sum(dispatch_count),count(judge0_token) FROM submission_executions WHERE submission_id=$1`, sub).Scan(&reservations, &tokens); err != nil || reservations != wantAccepted || tokens != wantAccepted || posts.Load() != wantPosts {
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
				if err := db.QueryRow(`SELECT count(*) FILTER (WHERE status='accepted'),count(*) FILTER (WHERE status NOT IN ('pending','accepted') OR (status='pending' AND (dispatch_count<>0 OR recovery_failures<>0))) FROM submission_executions WHERE submission_id=$1`, sub).Scan(&completed, &charged); err != nil || completed != wantAccepted || charged != 0 || posts.Load()-before > 1 {
					t.Fatal("outage blocked polling or consumed retries", completed, charged, posts.Load()-before, err)
				}
				deferrals += posts.Load() - before
			}
			if queueFull && deferrals < 4 {
				t.Fatal("did not exercise more than three capacity rejections", deferrals)
			}
			outage.Store(false)
			if !queueFull {
				listener, err := net.Listen("tcp", strings.TrimPrefix(endpoint, "http://"))
				if err != nil {
					t.Fatal(err)
				}
				judge = httptest.NewUnstartedServer(handler)
				judge.Listener.Close()
				judge.Listener = listener
				judge.Start()
			}
			for range 3 {
				readyExecutions(t, db, sub)
				service.RecoverJudge0(ctx)
			}
			assertSubmission(t, db, sub, "accepted", 3, 0)
			var score, results int
			if err := db.QueryRow(`SELECT score FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&score); err != nil || score != 10 {
				t.Fatal(score, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM test_case_results WHERE submission_id=$1`, sub).Scan(&results); err != nil || results != 3 || admitted.Load() != 3 {
				t.Fatal(results, admitted.Load(), err)
			}
		})
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
