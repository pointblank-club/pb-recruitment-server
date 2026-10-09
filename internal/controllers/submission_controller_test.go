package controllers

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

type submissionContestStore struct {
	*stores.ContestStore
	contest    models.Contest
	registered bool
}

func (s submissionContestStore) GetContest(context.Context, string) (*dto.GetContestResponse, error) {
	return &dto.GetContestResponse{Contest: s.contest}, nil
}
func (s submissionContestStore) IsRegistered(context.Context, string, string) (bool, error) {
	return s.registered, nil
}

type submissionProblemStore struct{ *stores.ProblemStore }

func (submissionProblemStore) GetProblem(context.Context, string, string) (*dto.GetProblemStatementResponse, error) {
	return &dto.GetProblemStatementResponse{Type: models.MCQ}, nil
}

type controllerSubmissionStore struct {
	*stores.SubmissionStore
	created, judged bool
}

func (s *controllerSubmissionStore) CreateSubmission(context.Context, *models.Submission) (string, error) {
	s.created = true
	return "submission", nil
}
func (s *controllerSubmissionStore) JudgeMCQ(context.Context, string) error {
	s.judged = true
	return nil
}

func TestSubmitSolutionContestWindow(t *testing.T) {
	now := time.Now().UnixMilli()
	for _, tc := range []struct {
		name       string
		start, end int64
		registered bool
		want       int
	}{
		{"upcoming", now + 60_000, now + 120_000, true, http.StatusForbidden},
		{"ended", now - 120_000, now - 60_000, true, http.StatusForbidden},
		{"running", now - 60_000, now + 60_000, true, http.StatusCreated},
		{"unregistered", now - 60_000, now + 60_000, false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submissions := &controllerSubmissionStore{}
			storage := &stores.Storage{
				Contests: submissionContestStore{contest: models.Contest{StartTime: tc.start, EndTime: tc.end}, registered: tc.registered},
				Problems: submissionProblemStore{}, Submissions: submissions,
			}
			controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil))
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/submit", nil), recorder)
			ctx.Set(common.AUTH_USER_ID, "user")
			ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.MCQ, Option: []int{1}})
			if err := controller.SubmitSolution(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != tc.want {
				t.Errorf("HTTP %d, want %d", recorder.Code, tc.want)
			}
			allowed := tc.want == http.StatusCreated
			if submissions.created != allowed || submissions.judged != allowed {
				t.Errorf("created=%v judged=%v", submissions.created, submissions.judged)
			}
		})
	}
}
