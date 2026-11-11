package stores_test

import (
	"app/internal/common"
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCodeAdmissionCapacityAndRetryPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, contest, problem, fixture, _ := executionFixture(t, db, 0)
	if _, err := db.Exec(`UPDATE submissions SET status='judge_error' WHERE id=$1`, fixture); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const n = 32
	type result struct {
		id     string
		replay bool
		err    error
	}
	results := make(chan result, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, replay, err := stores.NewSubmissionStore(db).CreateCodeSubmission(ctx, &models.Submission{UserID: uuid.NewString(), ContestID: contest, ProblemID: problem, Type: models.Code}, "", "", 5, 4)
			results <- result{id, replay, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted, limited := 0, 0
	for r := range results {
		if r.err == nil {
			accepted++
		} else if errors.Is(r.err, common.ErrRateLimitExceeded) {
			limited++
		} else {
			t.Fatal(r.err)
		}
	}
	if accepted != 4 || limited != n-4 {
		t.Fatalf("admitted %d rejected %d", accepted, limited)
	}
	if _, err := db.Exec(`UPDATE submissions SET status='judge_error' WHERE type='code' AND status='pending'`); err != nil {
		t.Fatal(err)
	}
	user, key := uuid.NewString(), uuid.NewString()
	results = make(chan result, n)
	start = make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, replay, err := stores.NewSubmissionStore(db).CreateCodeSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code}, key, "same-payload", 5, 4)
			results <- result{id, replay, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var id string
	created, replays := 0, 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if id != "" && id != r.id {
			t.Fatal("retry created another submission")
		}
		id = r.id
		if r.replay {
			replays++
		} else {
			created++
		}
	}
	if created != 1 || replays != n-1 {
		t.Fatal(created, replays)
	}
	store := stores.NewSubmissionStore(db)
	if _, _, err := store.CreateCodeSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code}, key, "changed", 5, 4); !errors.Is(err, common.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, err := store.FindCodeSubmissionRequest(ctx, "someone-else", key, "same-payload"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.FindCodeSubmissionRequest(ctx, user, key, "same-payload"); err != nil || got != id {
		t.Fatal(got, err)
	}
	if _, err := db.Exec(`UPDATE submission_rate_limits SET last_submission_at=last_submission_at-10 WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateCodeSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code}, uuid.NewString(), "same-payload", 5, 4); !errors.Is(err, common.ErrRateLimitExceeded) {
		t.Fatal("pending user bypass", err)
	}
	if err := store.MarkFailed(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateCodeSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code}, uuid.NewString(), "same-payload", 5, 4); err != nil {
		t.Fatal("released slot not reusable", err)
	}
}

func TestCodeAdmissionFailsClosed(t *testing.T) {
	store := stores.NewSubmissionStore(nil)
	if _, _, err := store.CreateCodeSubmission(context.Background(), &models.Submission{}, "", "", 5, 1); err == nil {
		t.Fatal("missing db accepted")
	}
	if _, err := store.FindCodeSubmissionRequest(context.Background(), "u", "key", "hash"); err == nil {
		t.Fatal("missing db accepted replay")
	}
	if err := store.CheckSubmissionRateLimit(context.Background(), "u", 5); err == nil {
		t.Fatal("missing db allowed")
	}
}

func TestCodeRetryDoesNotRepeatJudgeDispatchPostgres(t *testing.T) {
	db := postgresTestDB(t)
	user, contest, problem, fixture, _ := executionFixture(t, db, 0)
	if _, err := db.Exec(`DELETE FROM submissions WHERE id=$1`, fixture); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := posts.Add(1)
		w.WriteHeader(201)
		fmt.Fprintf(w, `[{"token":"a-%d"},{"token":"b-%d"},{"token":"c-%d"}]`, n, n, n)
	}))
	defer judge.Close()
	t.Setenv("JUDGE0_URL", judge.URL)
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "")
	service := services.NewSubmissionService(stores.NewStorage(db), dispatchObjects(t), judge0.NewClient())
	req := &dto.SubmitSubmissionRequest{RequestID: uuid.NewString(), ContestID: contest, ProblemID: problem, Type: models.Code, Language: "python", Code: "print('hello')"}
	id, err := service.CreateSubmission(context.Background(), user, models.Code, req)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		got, err := service.CreateSubmission(context.Background(), user, models.Code, req)
		if err != nil || got != id {
			t.Fatal(got, err)
		}
	}
	if posts.Load() != 1 {
		t.Fatal("replayed Judge0 request", posts.Load())
	}
	var jobs int
	if err := db.QueryRow(`SELECT count(*) FROM submission_executions WHERE submission_id=$1`, id).Scan(&jobs); err != nil || jobs != 3 {
		t.Fatal(jobs, err)
	}
	req.Code = "print('changed')"
	if _, err := service.CreateSubmission(context.Background(), user, models.Code, req); !errors.Is(err, common.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
}
