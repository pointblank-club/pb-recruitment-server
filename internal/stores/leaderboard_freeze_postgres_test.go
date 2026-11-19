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
	const users = 150 // eight pages
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
			if seen[e.UserID] || e.ProblemScores != nil || e.TotalScore != float64(100*e.ProblemsSolved) {
				t.Fatalf("duplicate, breakdown leak, or inconsistent entry: %+v", e)
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

	// Concurrent and repeated freezes keep one snapshot.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = store.SetLeaderboardFrozen(ctx, contest, true, nil) }(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
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

// An unfreeze sent while a freeze is capturing waits for it and then wins.
func TestLeaderboardUnfreezeDuringCapturePostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	contest, _, _ := seedFreezeContest(t, db, 600)
	store := stores.NewRankingStore(db)
	cutoff := time.Now().UnixMilli()
	freezeDone := make(chan error, 1)
	go func() { freezeDone <- store.SetLeaderboardFrozen(ctx, contest, true, &cutoff) }()

	for held := false; !held; {
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND granted AND objsubid = 1
			  AND ((classid::bigint << 32) | objid::bigint) = hashtextextended('leaderboard_freeze:' || $1, 0))
		`, contest).Scan(&held); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-freezeDone:
			t.Fatalf("freeze finished (%v) before the unfreeze could overlap it", err)
		default:
		}
	}
	if err := store.SetLeaderboardFrozen(ctx, contest, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-freezeDone; err != nil {
		t.Fatal(err)
	}
	if frozen, err := store.GetLeaderboardFreezeStatus(ctx, contest); err != nil || frozen {
		t.Fatalf("leaderboard frozen=%v err=%v after the later unfreeze", frozen, err)
	}
}
