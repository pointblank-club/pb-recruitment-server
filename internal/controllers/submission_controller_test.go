package controllers

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/gommon/log"
)

type submissionContestStore struct {
	*stores.ContestStore
	contest               models.Contest
	registered            bool
	err                   error
	tabSwitchCount        int
	isTabSwitchTerminated bool
	isSubmitted           bool
}

func (s submissionContestStore) GetContest(context.Context, string) (*dto.GetContestResponse, error) {
	return &dto.GetContestResponse{Contest: s.contest}, s.err
}

func (s submissionContestStore) IsRegistered(context.Context, string, string) (bool, error) {
	return s.registered, nil
}

func (s submissionContestStore) GetCandidateSessionStatus(context.Context, string, string) (*dto.CandidateSessionStatusResponse, error) {
	return &dto.CandidateSessionStatusResponse{
		TabSwitchCount:        s.tabSwitchCount,
		IsTabSwitchTerminated: s.isTabSwitchTerminated,
		IsSubmitted:           s.isSubmitted,
	}, nil
}

func TestSubmitSolutionMissingContest(t *testing.T) {
	storage := &stores.Storage{Contests: submissionContestStore{err: common.ContestNotFoundError}}
	controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/submit", nil), recorder)
	ctx.Set(common.AUTH_USER_ID, "user")
	ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "missing", ProblemID: "problem", Type: models.MCQ, Option: []int{0}})
	if err := controller.SubmitSolution(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("HTTP %d, want 404", recorder.Code)
	}
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
func (s *controllerSubmissionStore) CreateMCQSubmission(context.Context, *models.Submission, int64) (string, error) {
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
			controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil), nil)
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

type rateLimitedSubmissionStore struct {
	*stores.SubmissionStore
	rateLimitErr error
}

func (s *rateLimitedSubmissionStore) CheckSubmissionRateLimit(ctx context.Context, userID string, cooldownSeconds int64) error {
	return s.rateLimitErr
}
func (s *rateLimitedSubmissionStore) CreateSubmission(context.Context, *models.Submission) (string, error) {
	return "submission", nil
}
func (s *rateLimitedSubmissionStore) CreateMCQSubmission(context.Context, *models.Submission, int64) (string, error) {
	if s.rateLimitErr != nil {
		return "", s.rateLimitErr
	}
	return "submission", nil
}
func (s *rateLimitedSubmissionStore) JudgeMCQ(context.Context, string) error {
	return nil
}

func TestSubmitSolutionRateLimit(t *testing.T) {
	now := time.Now().UnixMilli()
	storage := &stores.Storage{
		Contests: submissionContestStore{
			contest:    models.Contest{StartTime: now - 60_000, EndTime: now + 60_000},
			registered: true,
		},
		Problems: submissionProblemStore{},
		Submissions: &rateLimitedSubmissionStore{
			rateLimitErr: common.ErrRateLimitExceeded,
		},
	}
	controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/submit", nil), recorder)
	ctx.Set(common.AUTH_USER_ID, "user")
	ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.MCQ, Option: []int{1}})
	if err := controller.SubmitSolution(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("HTTP %d, want 429", recorder.Code)
	}
}

func TestSubmitSolutionLogsCreateFailure(t *testing.T) {
	now := time.Now().UnixMilli()
	storeErr := errors.New("relation submission_rate_limits does not exist")
	storage := &stores.Storage{
		Contests: submissionContestStore{
			contest: models.Contest{StartTime: now - 60_000, EndTime: now + 60_000}, registered: true,
		},
		Problems:    submissionProblemStore{},
		Submissions: &rateLimitedSubmissionStore{rateLimitErr: storeErr},
	}
	controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil), nil)
	var output bytes.Buffer
	previous := log.Output()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/submit", nil), recorder)
	ctx.Set(common.AUTH_USER_ID, "user")
	ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.MCQ, Option: []int{1}})
	if err := controller.SubmitSolution(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP %d, want 500", recorder.Code)
	}
	if logged := output.String(); !strings.Contains(logged, "create submission for user user") || !strings.Contains(logged, storeErr.Error()) {
		t.Fatalf("missing user or database error in log: %s", logged)
	}
	if strings.Contains(recorder.Body.String(), storeErr.Error()) {
		t.Fatal("database error exposed in response")
	}
}

type duplicateSubmissionStore struct {
	*stores.SubmissionStore
}

func (s *duplicateSubmissionStore) CreateSubmission(context.Context, *models.Submission) (string, error) {
	// Simulate DB unique constraint violation (concurrent insert race).
	return "", common.ErrSubmissionAlreadyExists
}
func (s *duplicateSubmissionStore) CreateMCQSubmission(context.Context, *models.Submission, int64) (string, error) {
	return "", common.ErrSubmissionAlreadyExists
}
func (s *duplicateSubmissionStore) JudgeMCQ(context.Context, string) error {
	return errors.New("should not be called")
}

func TestSubmitSolutionDuplicateMCQ(t *testing.T) {
	now := time.Now().UnixMilli()
	storage := &stores.Storage{
		Contests: submissionContestStore{
			contest:    models.Contest{StartTime: now - 60_000, EndTime: now + 60_000},
			registered: true,
		},
		Problems:    submissionProblemStore{},
		Submissions: &duplicateSubmissionStore{},
	}
	controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil), nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/submit", nil), recorder)
	ctx.Set(common.AUTH_USER_ID, "user")
	ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.MCQ, Option: []int{1}})

	if err := controller.SubmitSolution(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("HTTP %d, want 409", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "submission already exists") {
		t.Fatalf("body = %s, want to contain 'submission already exists'", recorder.Body.String())
	}
}

type adminSubmissionTestStore struct {
	*stores.SubmissionStore
	details *dto.AdminSubmissionDetails
	err     error
}

func (s *adminSubmissionTestStore) GetAdminSubmissionDetails(_ context.Context, _ string) (*dto.AdminSubmissionDetails, error) {
	return s.details, s.err
}

func TestGetAdminSubmissionDetailsContestScoping(t *testing.T) {
	storage := &stores.Storage{
		Submissions: &adminSubmissionTestStore{
			details: &dto.AdminSubmissionDetails{
				AdminSubmissionItem: dto.AdminSubmissionItem{
					ID:        "sub-1",
					ContestID: "contest-1",
				},
			},
		},
	}
	controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), nil, nil)

	// Scoped request with matching contest ID should succeed (200)
	{
		recorder := httptest.NewRecorder()
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/admin/contests/contest-1/submissions/sub-1", nil)
		ctx := e.NewContext(req, recorder)
		ctx.SetParamNames("contestId", "id")
		ctx.SetParamValues("contest-1", "sub-1")

		if err := controller.GetAdminSubmissionDetails(ctx); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("matching contest: got %d, want 200", recorder.Code)
		}
	}

	// Scoped request with mismatched contest ID should return 404
	{
		recorder := httptest.NewRecorder()
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/admin/contests/contest-2/submissions/sub-1", nil)
		ctx := e.NewContext(req, recorder)
		ctx.SetParamNames("contestId", "id")
		ctx.SetParamValues("contest-2", "sub-1")

		if err := controller.GetAdminSubmissionDetails(ctx); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("mismatched contest: got %d, want 404", recorder.Code)
		}
	}

	// Global unscoped request should succeed (200)
	{
		recorder := httptest.NewRecorder()
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/admin/submissions/sub-1", nil)
		ctx := e.NewContext(req, recorder)
		ctx.SetParamNames("id")
		ctx.SetParamValues("sub-1")

		if err := controller.GetAdminSubmissionDetails(ctx); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("unscoped global route: got %d, want 200", recorder.Code)
		}
	}
}
