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

func TestMCQBatchFinalizationPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	user, contest, _, _, _ := executionFixture(t, db, 1)
	storage := stores.NewStorage(db)
	problem := uuid.NewString()
	if err := storage.Problems.CreateProblem(ctx, &models.Problem{ID: problem, ContestID: contest, Name: "MCQ", Score: 20, Type: models.MCQ, Options: []string{"a", "b"}, Answer: []int{1}}); err != nil {
		t.Fatal(err)
	}
	sub, err := storage.Submissions.CreateMCQSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.MCQ, Option: []int{1}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO rankings(contest_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, contest, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE submissions SET created_at=1001 WHERE id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	first := stores.NewRankingStore(db)
	second := stores.NewRankingStore(db)
	check := func(rs *stores.RankingStore, cutoff int64, score, solved int) {
		t.Helper()
		lb, err := rs.GetLiveLeaderboard(ctx, contest, 0, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		if len(lb.Entries) != 1 || lb.Entries[0].TotalScore != score || lb.Entries[0].ProblemsSolved != solved {
			t.Fatalf("cutoff=%d: %+v", cutoff, lb)
		}
		wantTime := int64(0)
		if solved > 0 {
			wantTime = 1001000
		}
		if lb.Entries[0].LastSubmissionTime != wantTime {
			t.Fatal(lb.Entries[0])
		}
		// Returned slices must not mutate the cached result.
		lb.Entries[0].TotalScore = -1
		if len(lb.Entries[0].ProblemScores) > 0 {
			lb.Entries[0].ProblemScores[0].Score = -1
		}
	}
	cutoff := time.Now().UnixMilli()
	check(first, cutoff, 0, 0)
	time.Sleep(2 * time.Millisecond)
	if err := storage.Submissions.JudgeMCQ(ctx, sub); err != nil {
		t.Fatal(err)
	}
	var finalized int64
	if err := db.QueryRow(`SELECT max(finalized_at) FROM mcq_verdict_history WHERE submission_id=$1`, sub).Scan(&finalized); err != nil {
		t.Fatal(err)
	}
	check(first, cutoff, 0, 0)
	check(second, cutoff, 0, 0)
	check(second, finalized, 0, 0) // Strict boundary excludes a verdict finalized exactly at it.
	check(first, finalized+1, 20, 1)
	if _, err := db.Exec(`UPDATE problems SET answer=ARRAY[0] WHERE id=$1`, problem); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := storage.Rankings.ConsolidateContest(ctx, contest, true, true); err != nil {
		t.Fatal(err)
	}
	// The previous accepted verdict remains published despite current wrong_answer.
	first.InvalidateCache(contest)
	check(first, finalized+1, 20, 1)
	check(second, finalized+1, 20, 1)
	var regraded int64
	if err := db.QueryRow(`SELECT max(finalized_at) FROM mcq_verdict_history WHERE submission_id=$1`, sub).Scan(&regraded); err != nil {
		t.Fatal(err)
	}
	check(first, regraded+1, 0, 0)
	check(second, regraded+1, 0, 0)
	// Fractional boundary includes timestamps before it without flooring to seconds.
	if _, err := db.Exec(`UPDATE mcq_verdict_history SET finalized_at=1001000 WHERE submission_id=$1 AND status='accepted'`, sub); err != nil {
		t.Fatal(err)
	}
	check(first, 1001123, 20, 1)
	// Exercise fills, invalidation, expiry and full/live isolation under the race detector.
	first.SetCacheTTL(time.Nanosecond)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				if i%3 == 0 {
					first.InvalidateCache(contest)
				}
				if i%2 == 0 {
					check(first, 1001123, 20, 1)
				} else {
					if _, err := first.GetLeaderboard(ctx, contest, 0); err != nil {
						t.Error(err)
					}
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestLiveLeaderboardPaginationPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	_, c, p, _, _ := executionFixture(t, db, 1)
	for i := 0; i < 25; i++ {
		u := uuid.NewString()
		if _, err := db.Exec(`INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Page',$2,$1,1,'CS')`, u, u+"@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO rankings(contest_id,user_id,hidden,disqualified) VALUES($1,$2,$3,$4)`, c, u, i == 23, i == 24); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO submissions(id,user_id,contest_id,problem_id,type,status,created_at) VALUES($1,$2,$3,$4,'code','accepted',$5)`, uuid.NewString(), u, c, p, 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	rs := stores.NewRankingStore(db)
	seen := map[string]bool{}
	for page := 0; page < 2; page++ {
		lb, err := rs.GetLiveLeaderboard(ctx, c, page, 0)
		if err != nil {
			t.Fatal(err)
		}
		if lb.TotalCount != 23 || lb.TotalPages != 2 {
			t.Fatalf("bad pagination %+v", lb)
		}
		for _, e := range lb.Entries {
			if seen[e.UserID] {
				t.Fatal("duplicate")
			}
			seen[e.UserID] = true
			if e.Rank != len(seen) {
				t.Fatalf("rank=%d position=%d", e.Rank, len(seen))
			}
		}
	}
	if len(seen) != 23 {
		t.Fatal(len(seen))
	}
}
