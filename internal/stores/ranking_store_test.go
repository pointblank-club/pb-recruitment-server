package stores

import (
	"context"
	"fmt"
	"testing"
	"time"

	"app/internal/models/dto"
)

func ts(v int64) *int64 { return &v }

// ponytail: one check for the solve aggregation. The earlier version counted a
// problem as solved when score == max_score, which also matched an unsolved
// zero-point problem, and it is easy to reintroduce that by "simplifying" the
// nil check away.
func TestSummarizeSolves(t *testing.T) {
	cases := []struct {
		name      string
		scores    []dto.ProblemScore
		wantSolve int
		wantLast  int64
	}{
		{"nothing solved", []dto.ProblemScore{
			{Score: 0, MaxScore: 100},
			{Score: 0, MaxScore: 50},
		}, 0, 0},
		{"zero point problem is not a solve", []dto.ProblemScore{
			{Score: 0, MaxScore: 0},
		}, 0, 0},
		{"takes the latest first solve", []dto.ProblemScore{
			{Score: 100, MaxScore: 100, SolvedAt: ts(3000)},
			{Score: 50, MaxScore: 50, SolvedAt: ts(1000)},
		}, 2, 3000},
		{"unsolved problems do not affect the time", []dto.ProblemScore{
			{Score: 100, MaxScore: 100, SolvedAt: ts(2000)},
			{Score: 0, MaxScore: 100},
		}, 1, 2000},
		{"no scores", nil, 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			solved, last := summarizeSolves(tc.scores)
			if solved != tc.wantSolve {
				t.Errorf("solved = %d, want %d", solved, tc.wantSolve)
			}
			if last != tc.wantLast {
				t.Errorf("lastSolve = %d, want %d", last, tc.wantLast)
			}
		})
	}
}

func TestMCQAnswersMatchLogic(t *testing.T) {
	cases := []struct {
		name    string
		choices []int64
		answer  []int64
		want    bool
	}{
		{"single correct", []int64{1}, []int64{1}, true},
		{"single incorrect", []int64{0}, []int64{1}, false},
		{"multi exact match in order", []int64{0, 2}, []int64{0, 2}, true},
		{"multi exact match reversed order", []int64{2, 0}, []int64{0, 2}, true},
		{"multi partial subset missing option", []int64{0}, []int64{0, 2}, false},
		{"multi with extra incorrect option", []int64{0, 1, 2}, []int64{0, 2}, false},
		{"empty choices", []int64{}, []int64{0}, false},
		{"empty answer", []int64{0}, []int64{}, false},
		{"both empty", []int64{}, []int64{}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mcqAnswersMatch(tc.choices, tc.answer)
			if got != tc.want {
				t.Errorf("mcqAnswersMatch(%v, %v) = %v, want %v", tc.choices, tc.answer, got, tc.want)
			}
		})
	}
}

func TestIsTiedCondition(t *testing.T) {
	p1 := participantSummary{
		UserID:             "user1",
		TotalScore:         100,
		ProblemsSolved:     2,
		LastSubmissionTime: 3000,
		TotalAttempts:      4,
	}
	p2 := participantSummary{
		UserID:             "user2",
		TotalScore:         100,
		ProblemsSolved:     2,
		LastSubmissionTime: 3000,
		TotalAttempts:      4,
	}
	pDifferentScore := p1
	pDifferentScore.TotalScore = 90

	pDifferentSolves := p1
	pDifferentSolves.ProblemsSolved = 1

	pDifferentTime := p1
	pDifferentTime.LastSubmissionTime = 2000

	pDifferentAttempts := p1
	pDifferentAttempts.TotalAttempts = 5

	if !isTied(p1, p2) {
		t.Errorf("expected p1 and p2 to be tied")
	}
	if isTied(p1, pDifferentScore) {
		t.Errorf("expected different score to not be tied")
	}
	if isTied(p1, pDifferentSolves) {
		t.Errorf("expected different solves to not be tied")
	}
	if isTied(p1, pDifferentTime) {
		t.Errorf("expected different solve time to not be tied")
	}
	if isTied(p1, pDifferentAttempts) {
		t.Errorf("expected different attempts to not be tied")
	}
}

func TestConsolidatorTieBreakingAndRanking(t *testing.T) {
	// Setup participant profiles to test all levels of tie-breaking:
	// 1. TotalScore (higher ranks first)
	// 2. ProblemsSolved (higher ranks first)
	// 3. LastSubmissionTime (earlier ranks first)
	// 4. TotalAttempts (fewer ranks first)
	// 5. Tied participants (share same rank)
	// 6. Disqualified participants (placed at end with rank 0)

	participants := []participantSummary{
		{UserID: "u_low_score", TotalScore: 50, ProblemsSolved: 1, LastSubmissionTime: 1000, TotalAttempts: 1},
		{UserID: "u_disqualified", TotalScore: 200, ProblemsSolved: 5, LastSubmissionTime: 1000, TotalAttempts: 5, Disqualified: true},
		{UserID: "u_more_solves", TotalScore: 100, ProblemsSolved: 3, LastSubmissionTime: 4000, TotalAttempts: 6},
		{UserID: "u_base", TotalScore: 100, ProblemsSolved: 2, LastSubmissionTime: 3000, TotalAttempts: 4},
		{UserID: "u_earlier_time", TotalScore: 100, ProblemsSolved: 2, LastSubmissionTime: 2000, TotalAttempts: 4},
		{UserID: "u_fewer_attempts", TotalScore: 100, ProblemsSolved: 2, LastSubmissionTime: 3000, TotalAttempts: 2},
		{UserID: "u_tied_with_base", TotalScore: 100, ProblemsSolved: 2, LastSubmissionTime: 3000, TotalAttempts: 4},
		{UserID: "u_zero_score", TotalScore: 0, ProblemsSolved: 0, LastSubmissionTime: 0, TotalAttempts: 2},
	}

	// Sort using the consolidator's comparator
	SortParticipantsForRanking(participants)

	// Assign ranks using consolidator ranking algorithm
	AssignRanks(participants)

	// Expected order:
	// Rank 1: u_more_solves (Score: 100, Solves: 3)
	// Rank 2: u_earlier_time (Score: 100, Solves: 2, Time: 2000)
	// Rank 3: u_fewer_attempts (Score: 100, Solves: 2, Time: 3000, Attempts: 2)
	// Rank 4: u_base & u_tied_with_base (Score: 100, Solves: 2, Time: 3000, Attempts: 4) -> both Rank 4!
	// Rank 6: u_low_score (Score: 50, Solves: 1, Time: 1000, Attempts: 1)
	// Rank 7: u_zero_score (Score: 0, Solves: 0)
	// Rank 0: u_disqualified (Disqualified = true)

	expectedOrder := []struct {
		userID   string
		wantRank int
	}{
		{"u_more_solves", 1},
		{"u_earlier_time", 2},
		{"u_fewer_attempts", 3},
		{"u_base", 4},
		{"u_tied_with_base", 4},
		{"u_low_score", 6},
		{"u_zero_score", 7},
		{"u_disqualified", 0},
	}

	for i, exp := range expectedOrder {
		got := participants[i]
		if got.UserID != exp.userID {
			t.Errorf("position %d: got user %s, want %s", i, got.UserID, exp.userID)
		}
		if got.Rank != exp.wantRank {
			t.Errorf("user %s: got rank %d, want rank %d", got.UserID, got.Rank, exp.wantRank)
		}
	}
}

func TestConsolidatorIdempotency(t *testing.T) {
	// Verify that running consolidation ranking twice produces exact same output
	genParticipants := func() []participantSummary {
		return []participantSummary{
			{UserID: "u1", TotalScore: 100, ProblemsSolved: 2, LastSubmissionTime: 2000, TotalAttempts: 3},
			{UserID: "u2", TotalScore: 100, ProblemsSolved: 2, LastSubmissionTime: 2000, TotalAttempts: 3},
			{UserID: "u3", TotalScore: 80, ProblemsSolved: 2, LastSubmissionTime: 1500, TotalAttempts: 2},
			{UserID: "u4", TotalScore: 0, ProblemsSolved: 0, LastSubmissionTime: 0, TotalAttempts: 1},
		}
	}

	run1 := genParticipants()
	SortParticipantsForRanking(run1)
	AssignRanks(run1)

	run2 := genParticipants()
	SortParticipantsForRanking(run2)
	AssignRanks(run2)

	if len(run1) != len(run2) {
		t.Fatalf("length mismatch: %d vs %d", len(run1), len(run2))
	}

	for i := range run1 {
		if run1[i] != run2[i] {
			t.Errorf("idempotency failed at index %d: run1=%+v, run2=%+v", i, run1[i], run2[i])
		}
	}
}


func TestLeaderboardCache(t *testing.T) {
	store := NewRankingStore(nil)
	store.SetCacheTTL(100 * time.Millisecond)

	contestID := "contest-1"
	page := 0
	key := fmt.Sprintf("%s:%d", contestID, page)

	mockResp := &dto.GetLeaderboardResponse{
		Entries: []dto.LeaderboardEntry{
			{
				Rank:       1,
				UserID:     "user-1",
				Username:   "Alice",
				TotalScore: 100,
			},
		},
		Page:       0,
		TotalPages: 1,
		TotalCount: 1,
	}

	// Manually populate cache to verify caching mechanics without database
	store.cacheMu.Lock()
	store.cache[key] = leaderboardCacheEntry{
		data:      mockResp,
		expiresAt: time.Now().Add(100 * time.Millisecond),
	}
	store.cacheMu.Unlock()

	// Should hit cache via GetLeaderboard
	resp, err := store.GetLeaderboard(context.Background(), contestID, page)
	if err != nil {
		t.Fatalf("expected cache hit from GetLeaderboard, got error: %v", err)
	}
	if resp == nil || len(resp.Entries) != 1 || resp.Entries[0].Username != "Alice" {
		t.Fatalf("unexpected cached data: %+v", resp)
	}

	// Mutating cloned response should not alter internal cache
	resp.Entries[0].Username = "Bob"
	respAgain, err := store.GetLeaderboard(context.Background(), contestID, page)
	if err != nil {
		t.Fatalf("expected second cache hit: %v", err)
	}
	if respAgain.Entries[0].Username != "Alice" {
		t.Errorf("cache was mutated by clone modification, got %s, want Alice", respAgain.Entries[0].Username)
	}

	// Invalidate cache for contest
	store.InvalidateCache(contestID)
	// Cache miss should attempt DB query and fail because db is nil
	if _, err := store.GetLeaderboard(context.Background(), contestID, page); err == nil {
		t.Fatal("expected DB error after cache invalidation")
	}

	// Test expiration
	store.cacheMu.Lock()
	store.cache[key] = leaderboardCacheEntry{
		data:      mockResp,
		expiresAt: time.Now().Add(-1 * time.Second), // already expired
	}
	store.cacheMu.Unlock()

	// Expired entry should be treated as cache miss and attempt DB query
	if _, err := store.GetLeaderboard(context.Background(), contestID, page); err == nil {
		t.Fatal("expected DB error for expired entry")
	}
}
