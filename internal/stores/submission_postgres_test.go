package stores_test

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUserProblemProgressPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	user, otherUser := uuid.NewString(), uuid.NewString()
	contest, otherContest := uuid.NewString(), uuid.NewString()
	for _, id := range []string{user, otherUser} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Progress test',$2,$1,1,'CS')`, id, id+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{contest, otherContest} {
		if _, err := db.ExecContext(ctx, `INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time) VALUES($1,'Progress test','',1,4102444800000,1,4102444800000)`, id); err != nil {
			t.Fatal(err)
		}
	}

	type attempt struct {
		id, status string
		createdAt  int64
	}
	tests := []struct {
		name     string
		attempts []attempt
		want     string
	}{
		{"latest pending with lower ID", []attempt{{"z", "wrong_answer", 100}, {"a", "pending", 100}}, "judging"},
		{"latest failure with lower ID", []attempt{{"z", "pending", 100}, {"a", "wrong_answer", 100}}, "failed"},
		{"earlier acceptance wins", []attempt{{"z", "accepted", 100}, {"a", "pending", 100}}, "solved"},
		{"latest timestamp wins", []attempt{{"z", "pending", 100}, {"a", "wrong_answer", 101}}, "failed"},
		{"no submissions", nil, ""},
	}
	want := make(map[string]string)
	var firstProblem string
	for i, tc := range tests {
		problem := uuid.NewString()
		if i == 0 {
			firstProblem = problem
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO problems(id,contest_id,name,score,type) VALUES($1,$2,$3,10,'code')`, problem, contest, tc.name); err != nil {
			t.Fatal(err)
		}
		var previousSeq int64
		for _, a := range tc.attempts {
			var seq int64
			err := db.QueryRowContext(ctx, `INSERT INTO submissions(id,user_id,contest_id,problem_id,type,status,created_at) VALUES($1,$2,$3,$4,'code',$5,$6) RETURNING submission_seq`, fmt.Sprintf("%02d-%s", i, a.id), user, contest, problem, a.status, a.createdAt).Scan(&seq)
			if err != nil || seq <= previousSeq {
				t.Fatalf("%s: sequence %d after %d: %v", tc.name, seq, previousSeq, err)
			}
			previousSeq = seq
		}
		if tc.want != "" {
			want[problem] = tc.want
		}
	}
	// Another account's accepted result must not change this user's pending result.
	if _, err := db.ExecContext(ctx, `INSERT INTO submissions(id,user_id,contest_id,problem_id,type,status,created_at) VALUES('other-user',$1,$2,$3,'code','accepted',100)`, otherUser, contest, firstProblem); err != nil {
		t.Fatal(err)
	}
	otherProblem := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO problems(id,contest_id,name,score,type) VALUES($1,$2,'Other contest',10,'code')`, otherProblem, otherContest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO submissions(id,user_id,contest_id,problem_id,type,status,created_at) VALUES('other-contest',$1,$2,$3,'code','accepted',100)`, user, otherContest, otherProblem); err != nil {
		t.Fatal(err)
	}

	store := stores.NewSubmissionStore(db)
	got, err := store.GetUserProblemProgress(ctx, user, contest)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("user progress = %v, want %v: %v", got, want, err)
	}
	got, err = store.GetUserProblemProgress(ctx, otherUser, contest)
	if err != nil || !reflect.DeepEqual(got, map[string]string{firstProblem: "solved"}) {
		t.Fatalf("other user progress = %v: %v", got, err)
	}
	got, err = store.GetUserProblemProgress(ctx, user, otherContest)
	if err != nil || !reflect.DeepEqual(got, map[string]string{otherProblem: "solved"}) {
		t.Fatalf("other contest progress = %v: %v", got, err)
	}
}

func TestJudgeMCQPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	user, contest, problem := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'MCQ test',$2,$1,1,'CS')`, user, user+"@example.test"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := db.ExecContext(ctx, `INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time) VALUES($1,'MCQ test','',1,$2,$3,$2)`, contest, now+3_600_000, now-3_600_000); err != nil {
		t.Fatal(err)
	}
	problemStore := stores.NewProblemStore(db)
	if err := problemStore.CreateProblem(ctx, &models.Problem{ID: problem, ContestID: contest, Name: "Question", Type: models.MCQ, Score: 10, Answer: []int{1}, Options: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	submissions := stores.NewSubmissionStore(db)
	id, err := submissions.CreateSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.MCQ, Option: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	_, dupErr := submissions.CreateSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.MCQ, Option: []int{1}})
	if !errors.Is(dupErr, common.ErrSubmissionAlreadyExists) {
		t.Fatalf("expected ErrSubmissionAlreadyExists, got %v", dupErr)
	}

	const n = 24
	start := make(chan struct{})
	failures := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; failures <- submissions.JudgeMCQ(ctx, id) }()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := submissions.JudgeMCQ(ctx, id); err != nil {
		t.Fatal(err)
	}
	var accepted int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM submissions WHERE user_id=$1 AND contest_id=$2 AND status='accepted'`, user, contest).Scan(&accepted); err != nil || accepted != 1 {
		t.Fatalf("accepted=%d error=%v", accepted, err)
	}
	assertScore := func() {
		t.Helper()
		leaderboard, err := stores.NewRankingStore(db).GetLeaderboard(ctx, contest, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(leaderboard.Entries) != 1 {
			t.Fatalf("entries=%d", len(leaderboard.Entries))
		}
		entry := leaderboard.Entries[0]
		sum := 0
		for _, score := range entry.ProblemScores {
			sum += score.Score
		}
		if entry.TotalScore != 10 || sum != entry.TotalScore {
			t.Fatalf("total=%d breakdown=%d", entry.TotalScore, sum)
		}
	}
	assertScore()
	// A completed verdict and its points must survive failure cleanup.
	if err := submissions.MarkFailed(ctx, id); err != nil {
		t.Fatal(err)
	}
	status, err := submissions.GetSubmissionStatusByID(ctx, id)
	if err != nil || status.Status != models.Accepted {
		t.Fatalf("completed verdict changed: %+v %v", status, err)
	}
	svc := services.NewContestService(stores.NewStorage(db), nil)
	for _, end := range []int64{now + 3_600_000, now - 60_000} {
		if _, err := db.ExecContext(ctx, `UPDATE contests SET end_time=$2 WHERE id=$1`, contest, end); err != nil {
			t.Fatal(err)
		}
		req := &dto.CreateProblemRequest{Name: "Changed", Type: models.MCQ, Score: 20, Answer: []int{0}, Options: []string{"changed", "options"}}
		if _, err := svc.UpdateProblem(ctx, contest, problem, req); !errors.Is(err, common.ErrProblemsLocked) {
			t.Fatalf("edit error=%v", err)
		}
		if err := svc.DeleteProblem(ctx, contest, problem); !errors.Is(err, common.ErrProblemsLocked) {
			t.Fatalf("delete error=%v", err)
		}
		meta, err := problemStore.GetProblem(ctx, problem, contest)
		if err != nil || meta.Score != 10 || len(meta.Answer) != 1 || meta.Answer[0] != 1 || meta.Options[0] != "a" {
			t.Fatalf("frozen problem changed: %+v %v", meta, err)
		}
		assertScore()
	}
}

func TestSubmissionRateLimitPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user := uuid.NewString()
	store := stores.NewSubmissionStore(db)

	// First submission attempt passes
	if err := store.CheckSubmissionRateLimit(ctx, user, 5); err != nil {
		t.Fatalf("first check failed: %v", err)
	}

	// Immediate second attempt within 5s cooldown fails
	if err := store.CheckSubmissionRateLimit(ctx, user, 5); !errors.Is(err, common.ErrRateLimitExceeded) {
		t.Fatalf("expected ErrRateLimitExceeded, got %v", err)
	}

	// Another user passes independently
	otherUser := uuid.NewString()
	if err := store.CheckSubmissionRateLimit(ctx, otherUser, 5); err != nil {
		t.Fatalf("other user check failed: %v", err)
	}

	// Simulate concurrent requests across replicas for the same user
	concurrentUser := uuid.NewString()
	const workers = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx] = store.CheckSubmissionRateLimit(ctx, concurrentUser, 10)
		}(i)
	}

	close(start)
	wg.Wait()

	successCount := 0
	rateLimitedCount := 0
	for _, res := range results {
		if res == nil {
			successCount++
		} else if errors.Is(res, common.ErrRateLimitExceeded) {
			rateLimitedCount++
		} else {
			t.Fatalf("unexpected error: %v", res)
		}
	}

	if successCount != 1 || rateLimitedCount != workers-1 {
		t.Fatalf("expected exactly 1 success and %d rate limited, got %d successes and %d rate limited",
			workers-1, successCount, rateLimitedCount)
	}
}
