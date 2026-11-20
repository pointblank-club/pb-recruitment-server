package stores_test

import (
	"app/internal/common"
	"app/internal/stores"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// seedFreezeContest creates an open contest whose users each have some accepted code solves.
func seedFreezeContest(t *testing.T, db *sql.DB, users int) (contest string, userIDs, problems []string) {
	t.Helper()
	contest = uuid.NewString()
	now := time.Now().UnixMilli()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time) VALUES($1,'Freeze','',1,$2,$3,$4)`, contest, now+3_600_000, now-3_600_000, now+3_600_000)
	for i := 0; i < 4; i++ {
		problems = append(problems, uuid.NewString())
		exec(`INSERT INTO problems(id,contest_id,name,score,type) VALUES($1,$2,$3,100,'code')`, problems[i], contest, fmt.Sprint("P", i))
	}
	for i := 0; i < users; i++ {
		userIDs = append(userIDs, uuid.NewString())
		exec(`INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,$2,$3,$1,1,'CS')`, userIDs[i], fmt.Sprintf("u%04d", i), userIDs[i]+"@x.test")
		exec(`INSERT INTO rankings(contest_id,user_id) VALUES($1,$2)`, contest, userIDs[i])
	}
	exec(`INSERT INTO submissions(id,user_id,contest_id,problem_id,type,status,created_at)
		SELECT gen_random_uuid()::text, r.user_id, $1, p.id, 'code', 'accepted', $2::bigint/1000 - (random()*3000)::int
		FROM rankings r JOIN problems p ON p.contest_id = r.contest_id
		WHERE r.contest_id = $1 AND random() < 0.4`, contest, now)
	return contest, userIDs, problems
}

func TestLeaderboardFreezeSnapshotUnderConcurrentScoringPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	const users = 600 // thirty pages
	contest, userIDs, problems := seedFreezeContest(t, db, users)
	store := stores.NewRankingStore(db)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(r *rand.Rand) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := db.Exec(`INSERT INTO submissions(id,user_id,contest_id,problem_id,type,status,created_at) VALUES($1,$2,$3,$4,'code','accepted',$5)`,
					uuid.NewString(), userIDs[r.Intn(users)], contest, problems[r.Intn(len(problems))], time.Now().Unix()); err != nil {
					t.Error(err)
					return
				}
			}
		}(rand.New(rand.NewSource(int64(w))))
	}
	time.Sleep(50 * time.Millisecond)
	cutoff := time.Now().UnixMilli()
	err := store.SetLeaderboardFrozen(ctx, contest, true, &cutoff)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	prevRank, prevScore := 0, 1e9
	for page := 0; ; page++ {
		lb, err := store.GetFrozenLeaderboard(ctx, contest, page)
		if err != nil || lb == nil || !lb.Frozen {
			t.Fatalf("page %d: %+v, %v", page, lb, err)
		}
		if lb.TotalCount != users {
			t.Fatalf("page %d total = %d, want %d", page, lb.TotalCount, users)
		}
		if len(lb.Entries) == 0 {
			break
		}
		for _, e := range lb.Entries {
			if seen[e.UserID] || len(e.ProblemScores) != len(problems) || e.TotalScore != float64(100*e.ProblemsSolved) {
				t.Fatalf("duplicate, missing DSA breakdown, or inconsistent entry: %+v", e)
			}
			if e.DSASolved == nil || e.MCQSolved == nil || *e.DSASolved != e.ProblemsSolved || *e.MCQSolved != 0 {
				t.Fatalf("inconsistent frozen split counts: %+v", e)
			}
			for _, ps := range e.ProblemScores {
				if ps.Type != "code" {
					t.Fatalf("non-DSA breakdown leaked: %+v", ps)
				}
			}
			if e.Rank < prevRank || e.TotalScore > prevScore {
				t.Fatalf("ordering broke at %+v (previous rank %d, score %v)", e, prevRank, prevScore)
			}
			seen[e.UserID], prevRank, prevScore = true, e.Rank, e.TotalScore
		}
	}
	if len(seen) != users {
		t.Fatalf("snapshot has %d users, want %d", len(seen), users)
	}
}

func TestLeaderboardFreezeLifecyclePostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	contest, userIDs, _ := seedFreezeContest(t, db, 3)
	if _, err := db.Exec(`DELETE FROM submissions WHERE contest_id = $1`, contest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE rankings SET score = CASE WHEN user_id = $2 THEN 30 ELSE 20 END WHERE contest_id = $1`, contest, userIDs[0]); err != nil {
		t.Fatal(err)
	}
	store := stores.NewRankingStore(db)
	store.SetCacheTTL(time.Minute)

	// Overlapping freezes may conflict; successful and repeated freezes keep one snapshot.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = store.SetLeaderboardFrozen(ctx, contest, true, nil) }(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil && !errors.Is(err, common.ErrLeaderboardFreezeInProgress) {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE rankings SET score = 99 WHERE contest_id = $1 AND user_id = $2`, contest, userIDs[2]); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLeaderboardFrozen(ctx, contest, true, nil); err != nil {
		t.Fatal(err)
	}
	lb, err := store.GetFrozenLeaderboard(ctx, contest, 0)
	if err != nil || lb == nil || len(lb.Entries) != 3 || lb.Entries[0].UserID != userIDs[0] || lb.Entries[0].TotalScore != 30 {
		t.Fatalf("repeated freeze replaced the snapshot: %+v, %v", lb, err)
	}
	if lb.Entries[1].Rank != 2 || lb.Entries[2].Rank != 2 {
		t.Fatalf("ties not kept: %+v", lb.Entries)
	}

	// Moderation during a freeze compacts ranks and keeps ties.
	if _, err := db.Exec(`UPDATE rankings SET disqualified = true WHERE contest_id = $1 AND user_id = $2`, contest, userIDs[0]); err != nil {
		t.Fatal(err)
	}
	store.InvalidateCache(contest) // UpdateLeaderboardUser does this.
	lb, err = store.GetFrozenLeaderboard(ctx, contest, 0)
	if err != nil || lb.TotalCount != 2 || lb.Entries[0].Rank != 1 || lb.Entries[1].Rank != 1 {
		t.Fatalf("after disqualification: %+v, %v", lb, err)
	}

	// Unfreezing takes effect immediately, despite cached frozen pages.
	if err := store.SetLeaderboardFrozen(ctx, contest, false, nil); err != nil {
		t.Fatal(err)
	}
	if lb, err := store.GetFrozenLeaderboard(ctx, contest, 0); err != nil || lb != nil {
		t.Fatalf("frozen page served after unfreeze: %+v, %v", lb, err)
	}
	if frozen, err := store.GetLeaderboardFreezeStatus(ctx, contest); err != nil || frozen {
		t.Fatalf("status after unfreeze = %v, %v", frozen, err)
	}
}

func TestLeaderboardFreezeWithoutMigrationPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	contest, _, _ := seedFreezeContest(t, db, 2)
	if _, err := db.Exec(`DROP TABLE leaderboard_freezes`); err != nil {
		t.Fatal(err)
	}
	store := stores.NewRankingStore(db)
	if _, err := store.GetLiveLeaderboard(ctx, contest, 0, time.Now().UnixMilli()); err != nil {
		t.Fatalf("live leaderboard needs the freeze table: %v", err)
	}
	if _, err := store.GetFrozenLeaderboard(ctx, contest, 0); !errors.Is(err, common.ErrLeaderboardFreezeUnavailable) {
		t.Fatal(err)
	}
	if err := store.SetLeaderboardFrozen(ctx, contest, true, nil); !errors.Is(err, common.ErrLeaderboardFreezeUnavailable) {
		t.Fatal(err)
	}
}

// Overlapping changes are rejected at once instead of waiting with a pooled connection.
func TestLeaderboardFreezeContentionPostgres(t *testing.T) {
	db := postgresTestDB(t)
	db.SetMaxOpenConns(25)
	contest, _, _ := seedFreezeContest(t, db, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	guard, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Rollback()
	if _, err := guard.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('leaderboard_freeze:' || $1, 0))`, contest); err != nil {
		t.Fatal(err)
	}

	store := stores.NewRankingStore(db)
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ { // The guard holds one of 25 connections, so waiting would exhaust the pool.
		wg.Add(1)
		go func(frozen bool) {
			defer wg.Done()
			if err := store.SetLeaderboardFrozen(ctx, contest, frozen, nil); !errors.Is(err, common.ErrLeaderboardFreezeInProgress) {
				t.Errorf("overlapping frozen=%v: %v", frozen, err)
			}
		}(i%2 == 0)
	}
	wg.Wait()
	if err := guard.Rollback(); err != nil {
		t.Fatal(err)
	}
	if frozen, err := store.GetLeaderboardFreezeStatus(ctx, contest); err != nil || frozen {
		t.Fatalf("rejected changes mutated freeze state: frozen=%v err=%v", frozen, err)
	}
	for _, frozen := range []bool{true, false} {
		if err := store.SetLeaderboardFrozen(ctx, contest, frozen, nil); err != nil {
			t.Fatalf("retry frozen=%v: %v", frozen, err)
		}
		if current, err := store.GetLeaderboardFreezeStatus(ctx, contest); err != nil || current != frozen {
			t.Fatalf("retry frozen=%v: current=%v err=%v", frozen, current, err)
		}
	}
}

func TestLeaderboardFrozenCacheBoundsPostgres(t *testing.T) {
	db := postgresTestDB(t)
	contest, _, _ := seedFreezeContest(t, db, 2)
	store := stores.NewRankingStore(db)
	store.SetCacheTTL(time.Minute)
	ctx := context.Background()
	if err := store.SetLeaderboardFrozen(ctx, contest, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetFrozenLeaderboard(ctx, contest, 0); err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= 100; page++ {
		lb, err := store.GetFrozenLeaderboard(ctx, contest, page)
		if err != nil || lb == nil || len(lb.Entries) != 0 {
			t.Fatalf("page %d: %+v, %v", page, lb, err)
		}
	}
	if size := store.CacheLen(); size != 1 {
		t.Fatalf("cache has %d entries after 100 nonexistent pages, want only the valid page", size)
	}
}
