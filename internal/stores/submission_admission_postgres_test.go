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
	"time"
)

func TestCodeAdmissionCapacityAndRetryPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, contest, _, problem := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
	register := func(user string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Admission',$2,$1,1,'CS')`, user, user+"@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO contest_registrations(contest_id,user_id,registered_at) VALUES($1,$2,1)`, contest, user); err != nil {
			t.Fatal(err)
		}
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
		user := uuid.NewString()
		register(user)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, replay, err := stores.NewSubmissionStore(db).CreateCodeSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code}, "", "", 5, 4)
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
	register(user)
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
	user, contest, _, problem := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
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

func TestCodeAdmissionKeyFailureRollsBackPostgres(t *testing.T) {
	for _, existingCooldown := range []bool{false, true} {
		t.Run(fmt.Sprint("existing cooldown=", existingCooldown), func(t *testing.T) {
			db := postgresTestDB(t)
			user, contest, _, problem := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
			if existingCooldown {
				if _, err := db.Exec(`INSERT INTO submission_rate_limits(user_id,last_submission_at) VALUES($1,1)`, user); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`ALTER TABLE code_submission_requests ADD CONSTRAINT reject_test_key CHECK (request_hash <> 'reject-test')`); err != nil {
				t.Fatal(err)
			}
			store := stores.NewSubmissionStore(db)
			sub := &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code}
			key := uuid.NewString()
			if _, _, err := store.CreateCodeSubmission(context.Background(), sub, key, "reject-test", 5, 1); err == nil {
				t.Fatal("key insert constraint must reject admission")
			}
			var parents, keys, cooldowns int
			var last int64
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM submissions WHERE user_id=$1), (SELECT count(*) FROM code_submission_requests WHERE user_id=$1), count(*), COALESCE(max(last_submission_at),0) FROM submission_rate_limits WHERE user_id=$1`, user).Scan(&parents, &keys, &cooldowns, &last); err != nil {
				t.Fatal(err)
			}
			wantCooldowns, wantLast := 0, int64(0)
			if existingCooldown {
				wantCooldowns, wantLast = 1, 1
			}
			if parents != 0 || keys != 0 || cooldowns != wantCooldowns || last != wantLast {
				t.Fatalf("failed admission leaked state: parents=%d keys=%d cooldowns=%d last=%d", parents, keys, cooldowns, last)
			}
			if _, replay, err := store.CreateCodeSubmission(context.Background(), sub, key, "accepted-test", 5, 1); err != nil || replay {
				t.Fatalf("rolled-back key/capacity/cooldown not reusable: replay=%v err=%v", replay, err)
			}
		})
	}
}

func TestCodeAdmissionReplaysAfterTerminationAndContestEndPostgres(t *testing.T) {
	db := postgresTestDB(t)
	user, contest, _, problem := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
	store := stores.NewSubmissionStore(db)
	sub := &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code}
	ctx := context.Background()
	key := uuid.NewString()
	id, replay, err := store.CreateCodeSubmission(ctx, sub, key, "same-payload", 5, 1)
	if err != nil || replay {
		t.Fatal(id, replay, err)
	}
	if _, err := db.Exec(`UPDATE contest_registrations SET is_tab_switch_terminated=true WHERE contest_id=$1 AND user_id=$2`, contest, user); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		want error
	}{
		{"terminated", common.ErrTabSwitchTerminated},
		{"ended", common.ContestNotRunningError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "ended" {
				if _, err := db.Exec(`UPDATE contests SET end_time=$2 WHERE id=$1`, contest, time.Now().UnixMilli()-1); err != nil {
					t.Fatal(err)
				}
			}
			if got, replay, err := store.CreateCodeSubmission(ctx, sub, key, "same-payload", 5, 1); err != nil || !replay || got != id {
				t.Fatalf("original request no longer replays: id=%s replay=%v err=%v", got, replay, err)
			}
			if _, _, err := store.CreateCodeSubmission(ctx, sub, uuid.NewString(), "same-payload", 5, 1); !errors.Is(err, tc.want) {
				t.Fatalf("new request: got %v, want %v", err, tc.want)
			}
			if _, _, err := store.CreateCodeSubmission(ctx, sub, key, "changed-payload", 5, 1); !errors.Is(err, common.ErrIdempotencyConflict) {
				t.Fatal("changed payload must not replay", err)
			}
		})
	}
}

func TestSubmissionCooldownRemainingPostgres(t *testing.T) {
	db := postgresTestDB(t)
	store := stores.NewSubmissionStore(db)
	if _, err := db.Exec(`INSERT INTO submission_rate_limits(user_id,last_submission_at) VALUES('cooldown-user',floor(extract(epoch from clock_timestamp()))::bigint-40)`); err != nil {
		t.Fatal(err)
	}
	err := store.CheckSubmissionRateLimit(context.Background(), "cooldown-user", 60)
	var cooldown *common.SubmissionCooldownError
	if !errors.Is(err, common.ErrRateLimitExceeded) || !errors.As(err, &cooldown) || cooldown.RetryAfter < 19 || cooldown.RetryAfter > 20 {
		t.Fatalf("remaining cooldown: %v", err)
	}
}
