package stores_test

import (
	"app/internal/models"
	"app/internal/stores"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMCQBatchCommitCrossingPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	user, contest, _, _, executions := executionFixture(t, db, 1)
	storage := stores.NewStorage(db)
	problem := uuid.NewString()
	if err := storage.Problems.CreateProblem(ctx, &models.Problem{ID: problem, ContestID: contest, Name: "MCQ", Score: 20, Type: models.MCQ, Options: []string{"a", "b"}, Answer: []int{1}}); err != nil {
		t.Fatal(err)
	}
	sub, err := storage.Submissions.CreateMCQSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.MCQ, Option: []int{1}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE submissions SET created_at=100 WHERE id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO rankings(contest_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, contest, user); err != nil {
		t.Fatal(err)
	}
	// Pause commit after verdict history is timestamped, like a large deferred-trigger queue.
	if _, err = db.ExecContext(ctx, `CREATE FUNCTION wait_for_mcq_commit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_advisory_xact_lock(hashtext(NEW.id)); RETURN NULL; END $$;
 CREATE CONSTRAINT TRIGGER zz_wait_for_mcq_commit AFTER UPDATE ON submissions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.type='mcq') EXECUTE FUNCTION wait_for_mcq_commit()`); err != nil {
		t.Fatal(err)
	}
	guard, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if _, err = guard.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, sub); err != nil {
		t.Fatal(err)
	}
	defer guard.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, sub)
	graded := make(chan error, 1)
	go func() { graded <- storage.Submissions.JudgeMCQ(ctx, sub) }()
	for {
		var waiting bool
		if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND objid=(hashtext($1)::bigint & 4294967295)::oid)`, sub).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	cutoff := time.Now().UnixMilli() + 1
	first, second := stores.NewRankingStore(db), stores.NewRankingStore(db)
	first.SetCacheTTL(0)
	second.SetCacheTTL(0)
	check := func(rs *stores.RankingStore, boundary int64, score, solves int) {
		t.Helper()
		lb, e := rs.GetLiveLeaderboard(ctx, contest, 0, boundary)
		if e != nil {
			t.Error(e)
			return
		}
		if len(lb.Entries) != 1 || lb.Entries[0].TotalScore != score || lb.Entries[0].ProblemsSolved != solves {
			t.Errorf("cutoff=%d: %+v", boundary, lb)
		}
	}
	// Simultaneous first fills across replicas must select one immutable batch.
	var wg sync.WaitGroup
	for _, rs := range []*stores.RankingStore{first, second} {
		wg.Add(1)
		go func(rs *stores.RankingStore) { defer wg.Done(); check(rs, cutoff, 0, 0) }(rs)
	}
	wg.Wait()
	if _, err = guard.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, sub); err != nil {
		t.Fatal(err)
	}
	if err = <-graded; err != nil {
		t.Fatal(err)
	}
	var finalized int64
	if err = db.QueryRowContext(ctx, `SELECT max(finalized_at) FROM mcq_verdict_history WHERE submission_id=$1`, sub).Scan(&finalized); err != nil {
		t.Fatal(err)
	}
	if finalized >= cutoff {
		t.Fatalf("commit crossing not exercised: finalized=%d cutoff=%d", finalized, cutoff)
	}
	first.InvalidateCache(contest)
	check(first, cutoff, 0, 0)
	check(second, cutoff, 0, 0)
	// Code stays live while MCQ results remain frozen, even after consolidation.
	if err = storage.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: executions[0].ID, Status: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if _, err = storage.Rankings.ConsolidateContest(ctx, contest, false, true); err != nil {
		t.Fatal(err)
	}
	check(first, cutoff, 10, 1)
	check(second, cutoff, 10, 1)
	check(second, cutoff+300000, 30, 2)
	// Empty markers and snapshots survive expiry; only distinct cutoffs create rows.
	var batches int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM leaderboard_mcq_batches WHERE contest_id=$1`, contest).Scan(&batches); err != nil || batches != 2 {
		t.Fatalf("batches=%d err=%v", batches, err)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM contests WHERE id=$1`, contest); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM leaderboard_mcq_batches WHERE contest_id=$1`, contest).Scan(&batches); err != nil || batches != 0 {
		t.Fatalf("orphan batches=%d err=%v", batches, err)
	}
}
