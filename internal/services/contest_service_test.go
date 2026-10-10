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
