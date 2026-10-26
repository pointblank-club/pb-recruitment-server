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
	consolidateFunc func(ctx context.Context, contestID string, regrade bool, force bool) (*dto.ConsolidationResponse, error)
	updateFunc      func(ctx context.Context, contestID, userID string, req *dto.UpdateLeaderboardUserRequest) error
	leaderboardFunc func(ctx context.Context, contestID string, page int) (*dto.GetLeaderboardResponse, error)
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
			_, err := NewContestService(storage, nil).GetLeaderboard(context.Background(), "c1", 1)
			if !errors.Is(err, tc.wantErr) || called != tc.wantCalled {
				t.Fatalf("err = %v, ranking store called = %v", err, called)
			}
		})
	}
}

type visibilityContestTestStore struct {
	*stores.ContestStore
	start, end   int64
	isRegistered bool
	err          error
}

func (s visibilityContestTestStore) GetContest(context.Context, string) (*dto.GetContestResponse, error) {
	return &dto.GetContestResponse{Contest: models.Contest{StartTime: s.start, EndTime: s.end}}, s.err
}

func (s visibilityContestTestStore) IsRegistered(context.Context, string, string) (bool, error) {
	return s.isRegistered, s.err
}

func TestGetProblemVisibility(t *testing.T) {
	now := time.Now().UnixMilli()
	tests := []struct {
		name         string
		start, end   int64
		isRegistered bool
		wantErr      error
	}{
		{
			name:         "upcoming contest should block all",
			start:        now + 60_000,
			end:          now + 120_000,
			isRegistered: true,
			wantErr:      common.ContestNotRunningError,
		},
		{
			name:         "running contest blocks unregistered user",
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: false,
			wantErr:      common.UserNotRegisteredError,
		},
		{
			name:         "running contest allows registered user",
			start:        now - 60_000,
			end:          now + 60_000,
			isRegistered: true,
			wantErr:      nil,
		},
		{
			name:         "ended contest allows unregistered user",
			start:        now - 120_000,
			end:          now - 60_000,
			isRegistered: false,
			wantErr:      nil,
		},
		{
			name:         "ended contest allows registered user",
			start:        now - 120_000,
			end:          now - 60_000,
			isRegistered: true,
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
			err := svc.GetProblemVisibility(context.Background(), "c1", "u1")
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}
