package stores_test

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/stores"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestConsolidationRepeatedPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user1, user2 := uuid.NewString(), uuid.NewString()
	contest := uuid.NewString()
	mcqProblem := uuid.NewString()
	codeProblem := uuid.NewString()

	for _, u := range []string{user1, user2} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Ranking user',$2,$1,1,'CS')`, u, u+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UnixMilli()
	if _, err := db.ExecContext(ctx, `INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time) VALUES($1,'Ranking Contest','',1,$2,$3,$2)`, contest, now+3_600_000, now-3_600_000); err != nil {
		t.Fatal(err)
	}

	problemStore := stores.NewProblemStore(db)
	if err := problemStore.CreateProblem(ctx, &models.Problem{ID: mcqProblem, ContestID: contest, Name: "MCQ Problem", Type: models.MCQ, Score: 10, Answer: []int{1}, Options: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := problemStore.CreateProblem(ctx, &models.Problem{ID: codeProblem, ContestID: contest, Name: "Code Problem", Type: models.Code, Score: 20, Options: []string{}, Answer: []int{}}); err != nil {
		t.Fatal(err)
	}

	subStore := stores.NewSubmissionStore(db)
	// User1: MCQ choice [1] (correct)
	_, err := subStore.CreateSubmission(ctx, &models.Submission{UserID: user1, ContestID: contest, ProblemID: mcqProblem, Type: models.MCQ, Option: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	// User2: MCQ choice [0] (incorrect)
	_, err = subStore.CreateSubmission(ctx, &models.Submission{UserID: user2, ContestID: contest, ProblemID: mcqProblem, Type: models.MCQ, Option: []int{0}})
	if err != nil {
		t.Fatal(err)
	}

	// Code submission for user1 (accepted)
	codeSub1, err := subStore.CreateSubmission(ctx, &models.Submission{UserID: user1, ContestID: contest, ProblemID: codeProblem, Type: models.Code})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE submissions SET status='accepted' WHERE id=$1`, codeSub1); err != nil {
		t.Fatal(err)
	}

	rankingStore := stores.NewRankingStore(db)

	// Run first consolidation
	res1, err := rankingStore.ConsolidateContest(ctx, contest, false, false)
	if err != nil {
		t.Fatalf("first consolidation failed: %v", err)
	}
	if res1.MCQsGraded != 2 || res1.ParticipantsRanked != 2 {
		t.Fatalf("unexpected res1: %+v", res1)
	}

	// Verify user1 has score 30 (10 MCQ + 20 Code), rank 1
	var score1, rank1 int
	if err := db.QueryRowContext(ctx, `SELECT score, rank FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user1).Scan(&score1, &rank1); err != nil {
		t.Fatalf("query user1 ranking: %v", err)
	}
	if score1 != 30 || rank1 != 1 {
		t.Fatalf("user1 got score=%d rank=%d, want score=30 rank=1", score1, rank1)
	}

	// Verify user2 has score 0, rank 2
	var score2, rank2 int
	if err := db.QueryRowContext(ctx, `SELECT score, rank FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user2).Scan(&score2, &rank2); err != nil {
		t.Fatalf("query user2 ranking: %v", err)
	}
	if score2 != 0 || rank2 != 2 {
		t.Fatalf("user2 got score=%d rank=%d, want score=0 rank=2", score2, rank2)
	}

	// Run second consolidation (re-run should be idempotent)
	res2, err := rankingStore.ConsolidateContest(ctx, contest, false, false)
	if err != nil {
		t.Fatalf("second consolidation failed: %v", err)
	}
	if res2.ParticipantsRanked != 2 {
		t.Fatalf("unexpected res2 participant count: %d", res2.ParticipantsRanked)
	}

	// Verify scores and ranks are preserved identically
	var score1Again, rank1Again int
	if err := db.QueryRowContext(ctx, `SELECT score, rank FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user1).Scan(&score1Again, &rank1Again); err != nil {
		t.Fatalf("query user1 ranking again: %v", err)
	}
	if score1Again != 30 || rank1Again != 1 {
		t.Fatalf("repeated consolidation changed user1: score=%d rank=%d", score1Again, rank1Again)
	}

	var score2Again, rank2Again int
	if err := db.QueryRowContext(ctx, `SELECT score, rank FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user2).Scan(&score2Again, &rank2Again); err != nil {
		t.Fatalf("query user2 ranking again: %v", err)
	}
	if score2Again != 0 || rank2Again != 2 {
		t.Fatalf("repeated consolidation changed user2: score=%d rank=%d", score2Again, rank2Again)
	}
}

func TestConsolidationConcurrentWithJudgeMCQPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user := uuid.NewString()
	contest := uuid.NewString()
	mcqProblem := uuid.NewString()

	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Concurrent user',$2,$1,1,'CS')`, user, user+"@example.test"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := db.ExecContext(ctx, `INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time) VALUES($1,'Concurrent Contest','',1,$2,$3,$2)`, contest, now+3_600_000, now-3_600_000); err != nil {
		t.Fatal(err)
	}

	problemStore := stores.NewProblemStore(db)
	if err := problemStore.CreateProblem(ctx, &models.Problem{ID: mcqProblem, ContestID: contest, Name: "MCQ Problem", Type: models.MCQ, Score: 10, Answer: []int{1}, Options: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}

	subStore := stores.NewSubmissionStore(db)
	subID, err := subStore.CreateSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: mcqProblem, Type: models.MCQ, Option: []int{1}})
	if err != nil {
		t.Fatal(err)
	}

	rankingStore := stores.NewRankingStore(db)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var judgeErr, consErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		judgeErr = subStore.JudgeMCQ(ctx, subID)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, consErr = rankingStore.ConsolidateContest(ctx, contest, false, false)
	}()

	close(start)
	wg.Wait()

	if judgeErr != nil {
		t.Fatalf("JudgeMCQ failed: %v", judgeErr)
	}
	if consErr != nil {
		t.Fatalf("ConsolidateContest failed: %v", consErr)
	}

	// Verify that user got the 10 points without lost update
	var finalScore int
	if err := db.QueryRowContext(ctx, `SELECT score FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&finalScore); err != nil {
		t.Fatalf("query rankings score: %v", err)
	}
	if finalScore != 10 {
		t.Fatalf("expected final score 10, got %d (lost update detected)", finalScore)
	}
}

func TestConsolidatedLeaderboardTieOrderPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user, contest, problem, submission, executions := executionFixture(t, db, 1)
	other := user + "-later"
	if _, err := db.ExecContext(ctx, `UPDATE users SET name='Zebra' WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Alpha',$2,$1,1,'CS')`, other, other+"@example.test"); err != nil {
		t.Fatal(err)
	}
	otherSubmission, otherExecutions := executionSubmission(t, db, other, contest, problem, 1)
	if _, err := db.ExecContext(ctx, `UPDATE submissions SET created_at=100 WHERE id IN ($1,$2)`, submission, otherSubmission); err != nil {
		t.Fatal(err)
	}
	for _, execution := range []models.Execution{executions[0], otherExecutions[0]} {
		if err := stores.NewExecutionStore(db).ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: execution.ID, Status: "accepted"}); err != nil {
			t.Fatal(err)
		}
	}

	rankingStore := stores.NewRankingStore(db)
	if _, err := rankingStore.ConsolidateContest(ctx, contest, false, false); err != nil {
		t.Fatal(err)
	}
	leaderboard, err := rankingStore.GetLeaderboard(ctx, contest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaderboard.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(leaderboard.Entries))
	}
	for i, userID := range []string{user, other} {
		if entry := leaderboard.Entries[i]; entry.UserID != userID || entry.Rank != 1 {
			t.Errorf("entry %d: got user %s rank %d, want user %s rank 1", i, entry.UserID, entry.Rank, userID)
		}
	}
}

func TestConsolidationAttemptPenaltyPostgres(t *testing.T) {
	for _, tc := range []struct {
		status   string
		wantRank int
	}{
		{"judge_error", 1},
		{"wrong_answer", 2},
		{"failed_to_process", 2},
		{"tle", 2},
		{"rte", 2},
	} {
		t.Run(tc.status, func(t *testing.T) {
			db := postgresTestDB(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			user, contest, problem, submission, executions := executionFixture(t, db, 1)
			other := user + "-other"
			if _, err := db.ExecContext(ctx, `INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Other',$2,$1,1,'CS')`, other, other+"@example.test"); err != nil {
				t.Fatal(err)
			}
			otherSubmission, otherExecutions := executionSubmission(t, db, other, contest, problem, 1)
			if _, err := db.ExecContext(ctx, `UPDATE submissions SET created_at=100 WHERE id IN ($1,$2)`, submission, otherSubmission); err != nil {
				t.Fatal(err)
			}
			executionStore := stores.NewExecutionStore(db)
			for _, execution := range []models.Execution{executions[0], otherExecutions[0]} {
				if err := executionStore.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: execution.ID, Status: "accepted"}); err != nil {
					t.Fatal(err)
				}
			}
			// Repeat solves count as attempts but must not change points or finish time.
			for _, userID := range []string{user, other} {
				repeat, repeatExecutions := executionSubmission(t, db, userID, contest, problem, 1)
				if _, err := db.ExecContext(ctx, `UPDATE submissions SET created_at=200 WHERE id=$1`, repeat); err != nil {
					t.Fatal(err)
				}
				if err := executionStore.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: repeatExecutions[0].ID, Status: "accepted"}); err != nil {
					t.Fatal(err)
				}
			}
			_, extraExecutions := executionSubmission(t, db, user, contest, problem, 1)
			if err := executionStore.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: extraExecutions[0].ID, Status: tc.status}); err != nil {
				t.Fatal(err)
			}

			rankingStore := stores.NewRankingStore(db)
			for run := 1; run <= 2; run++ {
				res, err := rankingStore.ConsolidateContest(ctx, contest, false, false)
				if err != nil {
					t.Fatal(err)
				}
				if res.ParticipantsRanked != 2 || res.PendingSubmissions != 0 {
					t.Fatalf("run %d: unexpected consolidation: %+v", run, res)
				}
				leaderboard, err := rankingStore.GetLeaderboard(ctx, contest, 0)
				if err != nil {
					t.Fatal(err)
				}
				if len(leaderboard.Entries) != 2 {
					t.Fatalf("run %d: got %d entries, want 2", run, len(leaderboard.Entries))
				}
				for _, entry := range leaderboard.Entries {
					wantRank, wantSubmissions := 1, 2
					if entry.UserID == user {
						wantRank, wantSubmissions = tc.wantRank, 3
					}
					var score, rank int
					if err := db.QueryRowContext(ctx, `SELECT score, rank FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, entry.UserID).Scan(&score, &rank); err != nil {
						t.Fatal(err)
					}
					if rank != wantRank || score != 10 || entry.Rank != wantRank || entry.TotalScore != 10 || entry.ProblemsSolved != 1 || entry.LastSubmissionTime != 100_000 {
						t.Errorf("run %d: stored score/rank=%d/%d, leaderboard=%+v; want score=10 rank=%d solves=1 finish=100000", run, score, rank, entry, wantRank)
					}
					// Displayed submissions include infrastructure failures; the rank penalty does not.
					if len(entry.ProblemScores) != 1 || entry.ProblemScores[0].Attempts != wantSubmissions {
						t.Errorf("run %d: problem scores=%+v, want %d displayed submissions", run, entry.ProblemScores, wantSubmissions)
					}
				}
			}
		})
	}
}

func TestConsolidatedLeaderboardRankZeroOrderingPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user1, user2, userHidden, user4, userUnranked := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	contest := uuid.NewString()

	for _, u := range []string{user1, user2, userHidden, user4, userUnranked} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Ranking user',$2,$1,1,'CS')`, u, u+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UnixMilli()
	if _, err := db.ExecContext(ctx, `INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time) VALUES($1,'Ranking Contest','',1,$2,$3,$2)`, contest, now+3_600_000, now-3_600_000); err != nil {
		t.Fatal(err)
	}

	// user1: rank 1
	// user2: rank 2 (tied)
	// userHidden: rank 3 (hidden = true, should not create a gap for visible users)
	// user4: rank 4 (next visible after tied rank 2, should be rank 3 if hidden excluded or preserved rank)
	// userUnranked: rank 0 (reinstated / added after consolidation, should sort last)
	if _, err := db.ExecContext(ctx, `INSERT INTO rankings(contest_id, user_id, score, rank, hidden, disqualified) VALUES 
		($1, $2, 100, 1, false, false), 
		($1, $3, 50, 2, false, false), 
		($1, $4, 40, 3, true, false), 
		($1, $5, 30, 4, false, false), 
		($1, $6, 10, 0, false, false)`, contest, user1, user2, userHidden, user4, userUnranked); err != nil {
		t.Fatal(err)
	}

	rankingStore := stores.NewRankingStore(db)
	lb, err := rankingStore.GetLeaderboard(ctx, contest, 0)
	if err != nil {
		t.Fatalf("GetLeaderboard failed: %v", err)
	}

	if len(lb.Entries) != 4 {
		t.Fatalf("expected 4 visible entries, got %d", len(lb.Entries))
	}

	// Verify order: user1 (rank 1), user2 (rank 2), user4 (rank 3 because hidden user doesn't leave gap), userUnranked (rank 4 at bottom)
	if lb.Entries[0].UserID != user1 || lb.Entries[0].Rank != 1 {
		t.Errorf("entry 0: expected user1 rank 1, got user %s rank %d", lb.Entries[0].UserID, lb.Entries[0].Rank)
	}
	if lb.Entries[1].UserID != user2 || lb.Entries[1].Rank != 2 {
		t.Errorf("entry 1: expected user2 rank 2, got user %s rank %d", lb.Entries[1].UserID, lb.Entries[1].Rank)
	}
	if lb.Entries[2].UserID != user4 || lb.Entries[2].Rank != 3 {
		t.Errorf("entry 2: expected user4 rank 3 (no hidden gap), got user %s rank %d", lb.Entries[2].UserID, lb.Entries[2].Rank)
	}
	if lb.Entries[3].UserID != userUnranked || lb.Entries[3].Rank != 4 {
		t.Errorf("entry 3: expected userUnranked at the bottom with rank 4, got user %s rank %d", lb.Entries[3].UserID, lb.Entries[3].Rank)
	}
}


func TestUpdateLeaderboardUserParticipantValidationPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	userRegistered := uuid.NewString()
	userUnregistered := uuid.NewString()
	contest := uuid.NewString()

	for _, u := range []string{userRegistered, userUnregistered} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Test user',$2,$1,1,'CS')`, u, u+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UnixMilli()
	if _, err := db.ExecContext(ctx, `INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time) VALUES($1,'Validation Contest','',1,$2,$3,$2)`, contest, now+3_600_000, now-3_600_000); err != nil {
		t.Fatal(err)
	}

	// Register userRegistered
	if _, err := db.ExecContext(ctx, `INSERT INTO contest_registrations(contest_id, user_id, registered_at) VALUES($1, $2, $3)`, contest, userRegistered, now); err != nil {
		t.Fatal(err)
	}

	rankingStore := stores.NewRankingStore(db)
	hiddenTrue := true
	disqTrue := true

	// 1. Updating unregistered user should fail with common.UserNotRegisteredError
	err := rankingStore.UpdateLeaderboardUser(ctx, contest, userUnregistered, &dto.UpdateLeaderboardUserRequest{
		Hidden: &hiddenTrue,
	})
	if !errors.Is(err, common.UserNotRegisteredError) {
		t.Fatalf("expected common.UserNotRegisteredError for unregistered user, got: %v", err)
	}

	// 2. Updating registered user should succeed
	err = rankingStore.UpdateLeaderboardUser(ctx, contest, userRegistered, &dto.UpdateLeaderboardUserRequest{
		Disqualified: &disqTrue,
	})
	if err != nil {
		t.Fatalf("expected UpdateLeaderboardUser to succeed for registered user, got: %v", err)
	}

	// Verify row in rankings
	var disq, hidden bool
	err = db.QueryRowContext(ctx, `SELECT disqualified, hidden FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, userRegistered).Scan(&disq, &hidden)
	if err != nil {
		t.Fatalf("failed to query ranking: %v", err)
	}
	if !disq || hidden {
		t.Fatalf("expected disqualified=true, hidden=false, got disq=%v, hidden=%v", disq, hidden)
	}

	// 3. Now userRegistered exists in rankings; update hidden only, disqualified should remain true
	err = rankingStore.UpdateLeaderboardUser(ctx, contest, userRegistered, &dto.UpdateLeaderboardUserRequest{
		Hidden: &hiddenTrue,
	})
	if err != nil {
		t.Fatalf("expected second UpdateLeaderboardUser to succeed, got: %v", err)
	}
	err = db.QueryRowContext(ctx, `SELECT disqualified, hidden FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, userRegistered).Scan(&disq, &hidden)
	if err != nil {
		t.Fatalf("failed to query ranking: %v", err)
	}
	if !disq || !hidden {
		t.Fatalf("expected disqualified=true, hidden=true (COALESCE preserved disqualified), got disq=%v, hidden=%v", disq, hidden)
	}
}
