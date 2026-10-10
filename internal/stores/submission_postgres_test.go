package stores_test

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestJudgeMCQPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database migrated through 000024")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(25)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	user, contest, problem := uuid.NewString(), uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM contests WHERE id=$1`, contest); err != nil {
			t.Error(err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM users WHERE id=$1`, user); err != nil {
			t.Error(err)
		}
	})
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
	const n = 24
	ids := make([]string, n)
	for i := range ids {
		ids[i], err = submissions.CreateSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.MCQ, Option: []int{1}})
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	failures := make(chan error, n)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) { defer wg.Done(); <-start; failures <- submissions.JudgeMCQ(ctx, id) }(id)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := submissions.JudgeMCQ(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	var accepted int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM submissions WHERE user_id=$1 AND contest_id=$2 AND status='accepted'`, user, contest).Scan(&accepted); err != nil || accepted != n {
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
	if err := submissions.MarkFailed(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	status, err := submissions.GetSubmissionStatusByID(ctx, ids[0])
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
