package services

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/stores"
	"context"
	"errors"
	"testing"
	"time"
)

func TestMCQAnswerRange(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    models.SubmissionType
		answers []int
		valid   bool
	}{
		{"negative", models.MCQ, []int{-1}, false},
		{"out of range", models.MCQ, []int{2}, false},
		{"valid boundaries", models.MCQ, []int{0, 1}, true},
		{"code", models.Code, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &dto.CreateProblemRequest{Type: tc.kind, Answer: tc.answers, Options: []string{"a", "b"}}
			if err := validateMCQAnswerRange(req); (err == nil) != tc.valid {
				t.Errorf("range error=%v valid=%v", err, tc.valid)
			}
			if tc.valid {
				return
			}
			svc := NewContestService(nil, nil)
			// Invalid answers must fail before database access or S3 writes.
			if _, err := svc.CreateProblem(context.Background(), "contest", req); !errors.Is(err, common.ErrInvalidAnswer) {
				t.Errorf("create error=%v", err)
			}
			if _, err := svc.UpdateProblem(context.Background(), "contest", "problem", req); !errors.Is(err, common.ErrInvalidAnswer) {
				t.Errorf("update error=%v", err)
			}
		})
	}
}

type problemContestTestStore struct {
	*stores.ContestStore
	start, end int64
	err        error
}

func (s problemContestTestStore) GetContest(context.Context, string) (*dto.GetContestResponse, error) {
	return &dto.GetContestResponse{Contest: models.Contest{StartTime: s.start, EndTime: s.end}}, s.err
}

func TestProblemEditWindow(t *testing.T) {
	now := time.Now().UnixMilli()
	lookupErr := errors.New("contest lookup failed")
	for _, tc := range []struct {
		name               string
		start, end         int64
		lookupErr, wantErr error
	}{
		{"upcoming", now + 60_000, now + 120_000, nil, nil},
		{"running", now - 60_000, now + 60_000, nil, common.ErrProblemsLocked},
		{"ended", now - 120_000, now - 60_000, nil, common.ErrProblemsLocked},
		{"database failure", 0, 0, lookupErr, lookupErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &stores.Storage{Contests: problemContestTestStore{start: tc.start, end: tc.end, err: tc.lookupErr}}
			err := NewContestService(storage, nil).checkProblemEditable(context.Background(), "contest")
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error=%v want %v", err, tc.wantErr)
			}
		})
	}
}

type serviceRankingTestStore struct {
	consolidateFunc     func(ctx context.Context, contestID string, regrade bool, force bool) (*dto.ConsolidationResponse, error)
	updateFunc          func(ctx context.Context, contestID, userID string, req *dto.UpdateLeaderboardUserRequest) error
	leaderboardFunc     func(ctx context.Context, contestID string, page int) (*dto.GetLeaderboardResponse, error)
	codeLeaderboardFunc func(ctx context.Context, contestID string, page int) (*dto.GetLeaderboardResponse, error)
}

func (s serviceRankingTestStore) ConsolidateContest(ctx context.Context, contestID string, regrade bool, force bool) (*dto.ConsolidationResponse, error) {
	if s.consolidateFunc != nil {
		return s.consolidateFunc(ctx, contestID, regrade, force)
	}
	return nil, nil
}

func (s serviceRankingTestStore) UpdateLeaderboardUser(ctx context.Context, contestID, userID string, req *dto.UpdateLeaderboardUserRequest) error {
	if s.updateFunc != nil {
		return s.updateFunc(ctx, contestID, userID, req)
	}
	return nil
}

func (s serviceRankingTestStore) GetLeaderboard(ctx context.Context, contestID string, page int) (*dto.GetLeaderboardResponse, error) {
	if s.leaderboardFunc != nil {
		return s.leaderboardFunc(ctx, contestID, page)
	}
	return nil, nil
}

func (s serviceRankingTestStore) GetCodeLeaderboard(ctx context.Context, contestID string, page int) (*dto.GetLeaderboardResponse, error) {
	if s.codeLeaderboardFunc != nil {
		return s.codeLeaderboardFunc(ctx, contestID, page)
	}
	return s.GetLeaderboard(ctx, contestID, page)
}

func (s serviceRankingTestStore) InvalidateCache(contestID string) {}

func TestConsolidateContestService(t *testing.T) {
	t.Run("nil ranking store error", func(t *testing.T) {
		svc := NewContestService(&stores.Storage{}, nil)
		_, err := svc.ConsolidateContest(context.Background(), "c1", false, false)
		if err == nil {
			t.Fatal("expected error with nil ranking store")
		}
	})

	t.Run("delegates to ranking store", func(t *testing.T) {
		called := false
		rankStore := serviceRankingTestStore{
			consolidateFunc: func(_ context.Context, cid string, regrade bool, force bool) (*dto.ConsolidationResponse, error) {
				called = true
				if cid != "c123" || !regrade || !force {
					t.Errorf("unexpected args: cid=%s, regrade=%v, force=%v", cid, regrade, force)
				}
				return &dto.ConsolidationResponse{ContestID: cid, Status: "success"}, nil
			},
		}
		svc := NewContestService(&stores.Storage{Rankings: rankStore}, nil)
		res, err := svc.ConsolidateContest(context.Background(), "c123", true, true)
		if err != nil {
			t.Fatal(err)
		}
		if !called || res == nil || res.Status != "success" {
			t.Errorf("unexpected result: called=%v, res=%+v", called, res)
		}
	})

	t.Run("fails when contest is running and force is false", func(t *testing.T) {
		now := time.Now().UnixMilli()
		storage := &stores.Storage{
			Contests: problemContestTestStore{start: now - 60_000, end: now + 60_000},
			Rankings: serviceRankingTestStore{},
		}
		svc := NewContestService(storage, nil)
		_, err := svc.ConsolidateContest(context.Background(), "c123", false, false)
		if !errors.Is(err, common.ErrContestNotClosed) {
			t.Errorf("expected ErrContestNotClosed, got %v", err)
		}
	})

	t.Run("succeeds when contest is running and force is true", func(t *testing.T) {
		now := time.Now().UnixMilli()
		called := false
		rankStore := serviceRankingTestStore{
			consolidateFunc: func(_ context.Context, cid string, regrade bool, force bool) (*dto.ConsolidationResponse, error) {
				called = true
				return &dto.ConsolidationResponse{ContestID: cid, Status: "success"}, nil
			},
		}
		storage := &stores.Storage{
			Contests: problemContestTestStore{start: now - 60_000, end: now + 60_000},
			Rankings: rankStore,
		}
		svc := NewContestService(storage, nil)
		res, err := svc.ConsolidateContest(context.Background(), "c123", false, true)
		if err != nil || !called || res == nil {
			t.Fatalf("unexpected error or uncalled: %v", err)
		}
	})
}

func TestGetLeaderboard(t *testing.T) {
	now := time.Now().UnixMilli()
	for _, tc := range []struct {
		name       string
		start, end int64
		lookupErr  error
		wantErr    error
		wantCalled bool
	}{
		{"upcoming", now + 60_000, now + 120_000, nil, common.ErrContestNotStarted, false},
		{"running", now - 60_000, now + 60_000, nil, nil, true},
		{"ended", now - 120_000, now - 60_000, nil, nil, true},
		{"not found", 0, 0, common.ContestNotFoundError, common.ContestNotFoundError, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			storage := &stores.Storage{
				Contests: problemContestTestStore{start: tc.start, end: tc.end, err: tc.lookupErr},
				Rankings: serviceRankingTestStore{leaderboardFunc: func(_ context.Context, _ string, page int) (*dto.GetLeaderboardResponse, error) {
					called = true
					return &dto.GetLeaderboardResponse{Page: page}, nil
				}},
			}
			_, err := NewContestService(storage, nil).GetLeaderboard(context.Background(), "c1", 1, false)
			if !errors.Is(err, tc.wantErr) || called != tc.wantCalled {
				t.Fatalf("err = %v, ranking store called = %v", err, called)
			}
		})
	}

	t.Run("redacts problem scores for non-admin", func(t *testing.T) {
		scores := []dto.ProblemScore{{ProblemID: "p1", Score: 10}}
		storage := &stores.Storage{
			Contests: problemContestTestStore{start: now - 60_000, end: now + 60_000},
			Rankings: serviceRankingTestStore{leaderboardFunc: func(_ context.Context, _ string, _ int) (*dto.GetLeaderboardResponse, error) {
				return &dto.GetLeaderboardResponse{
					Entries: []dto.LeaderboardEntry{
						{UserID: "u1", ProblemScores: scores},
					},
				}, nil
			}},
		}
		res, err := NewContestService(storage, nil).GetLeaderboard(context.Background(), "c1", 0, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Entries[0].ProblemScores != nil {
			t.Fatalf("expected problem scores to be nil for non-admin, got %v", res.Entries[0].ProblemScores)
		}
	})

	t.Run("preserves problem scores for admin", func(t *testing.T) {
		scores := []dto.ProblemScore{{ProblemID: "p1", Score: 10}}
		storage := &stores.Storage{
			Contests: problemContestTestStore{start: now - 60_000, end: now + 60_000},
			Rankings: serviceRankingTestStore{leaderboardFunc: func(_ context.Context, _ string, _ int) (*dto.GetLeaderboardResponse, error) {
				return &dto.GetLeaderboardResponse{
					Entries: []dto.LeaderboardEntry{
						{UserID: "u1", ProblemScores: scores},
					},
				}, nil
			}},
		}
		res, err := NewContestService(storage, nil).GetLeaderboard(context.Background(), "c1", 0, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Entries[0].ProblemScores) != 1 {
			t.Fatalf("expected 1 problem score for admin, got %v", res.Entries[0].ProblemScores)
		}
	})

	t.Run("non-admin during live contest calls GetCodeLeaderboard", func(t *testing.T) {
		codeLeaderboardCalled := false
		adminLeaderboardCalled := false
		storage := &stores.Storage{
			Contests: problemContestTestStore{start: now - 60_000, end: now + 60_000},
			Rankings: serviceRankingTestStore{
				codeLeaderboardFunc: func(_ context.Context, _ string, _ int) (*dto.GetLeaderboardResponse, error) {
					codeLeaderboardCalled = true
					return &dto.GetLeaderboardResponse{Entries: []dto.LeaderboardEntry{{UserID: "u1"}}}, nil
				},
				leaderboardFunc: func(_ context.Context, _ string, _ int) (*dto.GetLeaderboardResponse, error) {
					adminLeaderboardCalled = true
					return &dto.GetLeaderboardResponse{Entries: []dto.LeaderboardEntry{{UserID: "u1"}}}, nil
				},
			},
		}
		// Non-admin during live contest
		_, err := NewContestService(storage, nil).GetLeaderboard(context.Background(), "c1", 0, false)
		if err != nil || !codeLeaderboardCalled || adminLeaderboardCalled {
			t.Fatalf("expected codeLeaderboardCalled=true, adminLeaderboardCalled=false; got code=%v admin=%v err=%v", codeLeaderboardCalled, adminLeaderboardCalled, err)
		}

		// Admin during live contest
		codeLeaderboardCalled = false
		adminLeaderboardCalled = false
		_, err = NewContestService(storage, nil).GetLeaderboard(context.Background(), "c1", 0, true)
		if err != nil || codeLeaderboardCalled || !adminLeaderboardCalled {
			t.Fatalf("expected codeLeaderboardCalled=false, adminLeaderboardCalled=true; got code=%v admin=%v err=%v", codeLeaderboardCalled, adminLeaderboardCalled, err)
		}
	})
}

type visibilityContestTestStore struct {
	*stores.ContestStore
	start, end   int64
	isRegistered bool
}

func (s visibilityContestTestStore) GetContest(context.Context, string) (*dto.GetContestResponse, error) {
	return &dto.GetContestResponse{Contest: models.Contest{StartTime: s.start, EndTime: s.end}}, nil
}

func (s visibilityContestTestStore) IsRegistered(context.Context, string, string) (bool, error) {
	return s.isRegistered, nil
}

func TestGetProblemVisibility(t *testing.T) {
	now := time.Now().UnixMilli()
	tests := []struct {
		name         string
		userID       string
		start, end   int64
		isRegistered bool
		wantErr      error
	}{
		{
			name:         "upcoming contest should block all",
			userID:       "u1",
			start:        now + 60_000,
			end:          now + 120_000,
			isRegistered: true,
			wantErr:      common.ContestNotRunningError,
		},
		{
			name:         "running contest blocks unregistered user",
			userID:       "u1",
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: false,
			wantErr:      common.UserNotRegisteredError,
		},
		{
			name:         "running contest allows registered user",
			userID:       "u1",
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: true,
			wantErr:      nil,
		},
		{
			name:         "running contest blocks anonymous guest",
			userID:       "",
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: false,
			wantErr:      common.UserNotRegisteredError,
		},
		{
			name:         "ended contest allows unregistered user",
			userID:       "u1",
			start:        now - 120_000,
			end:          now - 60_000,
			isRegistered: false,
			wantErr:      nil,
		},
		{
			name:         "ended contest allows registered user",
			userID:       "u1",
			start:        now - 120_000,
			end:          now - 60_000,
			isRegistered: true,
			wantErr:      nil,
		},
		{
			name:         "ended contest allows anonymous guest",
			userID:       "",
			start:        now - 120_000,
			end:          now - 60_000,
			isRegistered: false,
			wantErr:      nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storage := &stores.Storage{
				Contests: visibilityContestTestStore{
					start:        tc.start,
					end:          tc.end,
					isRegistered: tc.isRegistered,
				},
			}
			svc := NewContestService(storage, nil)
			err := svc.GetProblemVisibility(context.Background(), "c1", tc.userID)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

type problemsListTestStore struct {
	*stores.ProblemStore
	problems []dto.ProblemOverview
}

func (s problemsListTestStore) GetProblemList(context.Context, string) ([]dto.ProblemOverview, error) {
	return s.problems, nil
}

type progressSubmissionsTestStore struct {
	*stores.SubmissionStore
	progress map[string]string
}

func (s progressSubmissionsTestStore) GetUserProblemProgress(context.Context, string, string) (map[string]string, error) {
	return s.progress, nil
}

func TestGetContestProblemsListMCQProgressMasking(t *testing.T) {
	storage := &stores.Storage{
		Contests: problemContestTestStore{start: 1, end: time.Now().Add(time.Minute).UnixMilli()},
		Problems: problemsListTestStore{
			problems: []dto.ProblemOverview{
				{ID: "mcq1", Type: models.MCQ},
				{ID: "mcq2", Type: models.MCQ},
				{ID: "mcq3", Type: models.MCQ},
				{ID: "code1", Type: models.Code},
				{ID: "code2", Type: models.Code},
			},
		},
		Submissions: progressSubmissionsTestStore{
			progress: map[string]string{
				"mcq1":  "solved",
				"mcq2":  "failed",
				"code1": "solved",
				"code2": "failed",
			},
		},
	}
	svc := NewContestService(storage, nil)
	list, err := svc.GetContestProblemsList(context.Background(), "contest-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := map[string]string{
		"mcq1":  "attempted",
		"mcq2":  "attempted",
		"mcq3":  "unsolved",
		"code1": "solved",
		"code2": "failed",
	}

	for _, p := range list {
		if got := p.Progress; got != expected[p.ID] {
			t.Errorf("problem %s progress: got %q, want %q", p.ID, got, expected[p.ID])
		}
	}
}

type tabSwitchServiceTestStore struct {
	*stores.ContestStore
	start, end       int64
	regStart, regEnd int64
	isRegistered     bool
	err              error
	count            int
	generation       int
	isTerminated     bool
	resetCalled      bool
	unregisterCalled bool
	seen             map[int]map[string]struct{}
}

func (s *tabSwitchServiceTestStore) GetContest(context.Context, string) (*dto.GetContestResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	regStart := s.regStart
	regEnd := s.regEnd
	if regStart == 0 && regEnd == 0 {
		now := time.Now().UnixMilli()
		regStart = now - 3_600_000
		regEnd = now + 3_600_000
	}
	return &dto.GetContestResponse{Contest: models.Contest{
		StartTime:             s.start,
		EndTime:               s.end,
		RegistrationStartTime: regStart,
		RegistrationEndTime:   regEnd,
	}}, nil
}

func (s *tabSwitchServiceTestStore) IsRegistered(context.Context, string, string) (bool, error) {
	return s.isRegistered, nil
}

func (s *tabSwitchServiceTestStore) UnregisterUser(context.Context, string, string) error {
	s.unregisterCalled = true
	if !s.isRegistered {
		return common.UserNotFoundError
	}
	s.isRegistered = false
	return nil
}

func (s *tabSwitchServiceTestStore) RecordTabSwitch(_ context.Context, _ string, _ string, observationID string, generation int, count int) (*dto.TabSwitchResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if !s.isRegistered {
		return nil, common.UserNotRegisteredError
	}
	now := time.Now().UnixMilli()
	if now < s.start || now > s.end {
		return nil, common.ContestNotRunningError
	}
	if generation != s.generation {
		return nil, &common.TabSwitchStaleGenerationError{
			TabSwitchCount: s.count,
			IsTerminated:   s.isTerminated,
			Generation:     s.generation,
		}
	}
	if s.isTerminated {
		return &dto.TabSwitchResponse{
			TabSwitchCount: s.count,
			IsTerminated:   true,
			Generation:     s.generation,
		}, nil
	}
	if s.seen == nil {
		s.seen = make(map[int]map[string]struct{})
	}
	if s.seen[generation] == nil {
		s.seen[generation] = make(map[string]struct{})
	}
	if _, dup := s.seen[generation][observationID]; dup {
		return &dto.TabSwitchResponse{
			TabSwitchCount: s.count,
			IsTerminated:   s.isTerminated,
			Generation:     s.generation,
		}, nil
	}
	s.seen[generation][observationID] = struct{}{}
	s.count += count
	if s.count > dto.TabSwitchTerminationThreshold {
		s.count = dto.TabSwitchTerminationThreshold
	}
	if s.count >= dto.TabSwitchTerminationThreshold {
		s.isTerminated = true
	}
	return &dto.TabSwitchResponse{
		TabSwitchCount: s.count,
		IsTerminated:   s.isTerminated,
		Generation:     s.generation,
	}, nil
}

func (s *tabSwitchServiceTestStore) GetCandidateSessionStatus(context.Context, string, string) (*dto.CandidateSessionStatusResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if !s.isRegistered {
		return nil, common.UserNotRegisteredError
	}
	return &dto.CandidateSessionStatusResponse{
		TabSwitchCount:        s.count,
		IsTabSwitchTerminated: s.isTerminated,
		Generation:            s.generation,
	}, nil
}

func (s *tabSwitchServiceTestStore) ResetContestantTabSwitches(context.Context, string, string, string) (int, error) {
	s.resetCalled = true
	s.count = 0
	s.isTerminated = false
	s.generation++
	return s.generation, nil
}

func TestContestServiceTabSwitch(t *testing.T) {
	now := time.Now().UnixMilli()
	obs1 := "11111111-1111-4111-8111-111111111111"
	obs2 := "22222222-2222-4222-8222-222222222222"

	t.Run("record tab switch during running contest and deduplicate retry", func(t *testing.T) {
		store := &tabSwitchServiceTestStore{
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: true,
		}
		svc := NewContestService(&stores.Storage{Contests: store}, nil)
		res, err := svc.RecordTabSwitch(context.Background(), "c1", "u1", obs1, 0, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.TabSwitchCount != 1 || res.IsTerminated || res.Generation != 0 {
			t.Fatalf("unexpected result: %+v", res)
		}

		// Identical retry does not increment again.
		retryRes, err := svc.RecordTabSwitch(context.Background(), "c1", "u1", obs1, 0, 1)
		if err != nil {
			t.Fatalf("retry error: %v", err)
		}
		if retryRes.TabSwitchCount != 1 {
			t.Fatalf("retry incremented count: got %d, want 1", retryRes.TabSwitchCount)
		}
	})

	t.Run("record tab switch rejects invalid observation contract before store", func(t *testing.T) {
		store := &tabSwitchServiceTestStore{
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: true,
		}
		svc := NewContestService(&stores.Storage{Contests: store}, nil)
		for _, tc := range []struct {
			name, obsID string
			gen, count  int
		}{
			{"invalid uuid", "bad-uuid", 0, 1},
			{"zero count", obs1, 0, 0},
			{"negative count", obs1, 0, -1},
			{"count=2", obs1, 0, 2},
			{"count=5", obs1, 0, 5},
			{"above threshold delta", obs1, 0, 6},
			{"negative generation", obs1, -1, 1},
		} {
			_, err := svc.RecordTabSwitch(context.Background(), "c1", "u1", tc.obsID, tc.gen, tc.count)
			if !errors.Is(err, common.ErrInvalidTabSwitchRequest) {
				t.Fatalf("%s: got %v, want ErrInvalidTabSwitchRequest", tc.name, err)
			}
		}
	})

	t.Run("record tab switch blocks when not running", func(t *testing.T) {
		store := &tabSwitchServiceTestStore{
			start:        now + 60_000,
			end:          now + 120_000,
			isRegistered: true,
		}
		svc := NewContestService(&stores.Storage{Contests: store}, nil)
		_, err := svc.RecordTabSwitch(context.Background(), "c1", "u1", obs1, 0, 1)
		if !errors.Is(err, common.ContestNotRunningError) {
			t.Fatalf("got error %v, want ContestNotRunningError", err)
		}
	})

	t.Run("record tab switch blocks when not registered", func(t *testing.T) {
		store := &tabSwitchServiceTestStore{
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: false,
		}
		svc := NewContestService(&stores.Storage{Contests: store}, nil)
		_, err := svc.RecordTabSwitch(context.Background(), "c1", "u1", obs1, 0, 1)
		if !errors.Is(err, common.UserNotRegisteredError) {
			t.Fatalf("got error %v, want UserNotRegisteredError", err)
		}
	})

	t.Run("session status returns registration status", func(t *testing.T) {
		store := &tabSwitchServiceTestStore{
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: true,
			count:        3,
			generation:   2,
			isTerminated: false,
		}
		svc := NewContestService(&stores.Storage{Contests: store}, nil)
		status, err := svc.GetCandidateSessionStatus(context.Background(), "c1", "u1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if status.TabSwitchCount != 3 || status.IsTabSwitchTerminated || status.Generation != 2 {
			t.Fatalf("unexpected status: %+v", status)
		}
	})

	t.Run("admin reset increments generation and stale reports conflict", func(t *testing.T) {
		store := &tabSwitchServiceTestStore{
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: true,
			count:        5,
			isTerminated: true,
		}
		svc := NewContestService(&stores.Storage{Contests: store}, nil)
		gen, err := svc.ResetContestantTabSwitches(context.Background(), "c1", "u1", "admin1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !store.resetCalled || gen != 1 || store.count != 0 || store.isTerminated {
			t.Fatalf("expected reset, got: gen=%d count=%d terminated=%v", gen, store.count, store.isTerminated)
		}

		// Delayed gen-0 observation is rejected as stale.
		_, err = svc.RecordTabSwitch(context.Background(), "c1", "u1", obs2, 0, 1)
		var staleErr *common.TabSwitchStaleGenerationError
		if !errors.As(err, &staleErr) || staleErr.Generation != 1 {
			t.Fatalf("expected TabSwitchStaleGenerationError gen=1, got %v", err)
		}
	})

	t.Run("unregister rejected after contest starts", func(t *testing.T) {
		store := &tabSwitchServiceTestStore{
			start:        now - 60_000,
			end:          now + 60_000,
			regStart:     now - 120_000,
			regEnd:       now + 60_000,
			isRegistered: true,
		}
		svc := NewContestService(&stores.Storage{Contests: store}, nil)
		err := svc.ModifyRegistration(context.Background(), "c1", "u1", dto.UnregisterAction)
		if !errors.Is(err, common.ErrCannotUnregisterAfterStart) {
			t.Fatalf("expected ErrCannotUnregisterAfterStart, got %v", err)
		}
		if store.unregisterCalled {
			t.Fatal("UnregisterUser should not be invoked after contest start")
		}
	})
}
