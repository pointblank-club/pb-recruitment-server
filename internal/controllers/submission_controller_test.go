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
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
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
}

func (s submissionContestStore) GetContest(context.Context, string) (*dto.GetContestResponse, error) {
	return &dto.GetContestResponse{Contest: s.contest}, s.err
}

func (s submissionContestStore) IsRegistered(context.Context, string, string) (bool, error) {
	return s.registered, nil
}

func (s submissionContestStore) IsSessionEnded(context.Context, string, string) (bool, error) {
	return false, nil
}

func (s submissionContestStore) EndContestSession(context.Context, string, string) error {
	return nil
}

func (s submissionContestStore) GetCandidateSessionStatus(context.Context, string, string) (*dto.CandidateSessionStatusResponse, error) {
	return &dto.CandidateSessionStatusResponse{
		TabSwitchCount:        s.tabSwitchCount,
		IsTabSwitchTerminated: s.isTabSwitchTerminated,
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
	contests        *tabSwitchMockStore
	created, judged bool
}

func (s *controllerSubmissionStore) CreateCodeSubmission(context.Context, *models.Submission, string, string, int64, int) (string, bool, error) {
	if s.contests != nil && s.contests.isTerminated {
		return "", false, common.ErrTabSwitchTerminated
	}
	s.created = true
	return "submission", false, nil
}
func (s *controllerSubmissionStore) CreateMCQSubmission(context.Context, *models.Submission, int64) (string, error) {
	if s.contests != nil && s.contests.isTerminated {
		return "", common.ErrTabSwitchTerminated
	}
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
func (s *rateLimitedSubmissionStore) CreateCodeSubmission(context.Context, *models.Submission, string, string, int64, int) (string, bool, error) {
	if s.rateLimitErr != nil {
		return "", false, s.rateLimitErr
	}
	return "submission", false, nil
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
			rateLimitErr: &common.SubmissionCooldownError{RetryAfter: 47},
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
	if recorder.Header().Get("Retry-After") != "47" {
		t.Fatal("cooldown Retry-After", recorder.Header())
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
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") || !strings.Contains(recorder.Body.String(), `"error":`) {
		t.Fatal("submission failure must return a JSON error", recorder.Header(), recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), storeErr.Error()) {
		t.Fatal("database error exposed in response")
	}
}

type duplicateSubmissionStore struct {
	*stores.SubmissionStore
}

func (s *duplicateSubmissionStore) CreateCodeSubmission(context.Context, *models.Submission, string, string, int64, int) (string, bool, error) {
	return "", false, common.ErrSubmissionAlreadyExists
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

type replaySubmissionStore struct{ *stores.SubmissionStore }

func (replaySubmissionStore) FindCodeSubmissionRequest(context.Context, string, string, string) (string, error) {
	return "original", nil
}

func TestCodeSubmissionRetryReturnsOriginalWithoutContestLookup(t *testing.T) {
	controller := NewSubmissionController(services.NewSubmissionService(&stores.Storage{Submissions: replaySubmissionStore{}}, nil, nil), nil, nil)
	for _, tc := range []struct {
		key  string
		want int
	}{{uuid.NewString(), 201}, {"invalid", 400}} {
		request := httptest.NewRequest(http.MethodPost, "/submission/submit", nil)
		request.Header.Set("Idempotency-Key", tc.key)
		w := httptest.NewRecorder()
		c := echo.New().NewContext(request, w)
		c.Set(common.AUTH_USER_ID, "user")
		c.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "ended", ProblemID: "problem", Type: models.Code, Language: "python", Code: "print(1)"})
		if err := controller.SubmitSolution(c); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
		if tc.want == 201 && !strings.Contains(w.Body.String(), "original") {
			t.Fatal(w.Body.String())
		}
	}
}

func TestSubmitSolutionTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		registration bool
		failure      error
	}{
		{"registration timeout", true, fmt.Errorf("database query: %w", context.DeadlineExceeded)},
		{"admission timeout", false, context.DeadlineExceeded},
		{"connection exhaustion", false, &pgconn.PgError{Code: "53300"}},
		{"database restart", true, &pgconn.PgError{Code: "57P01"}},
		{"connection refused", true, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
		{"connection reset", false, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UnixMilli()
			contest := submissionContestStore{contest: models.Contest{StartTime: now - 60000, EndTime: now + 60000}, registered: true}
			submissions := &rateLimitedSubmissionStore{}
			if tc.registration {
				contest.err = tc.failure
			} else {
				submissions.rateLimitErr = tc.failure
			}
			storage := &stores.Storage{Contests: contest, Problems: submissionProblemStore{}, Submissions: submissions}
			controller := NewSubmissionController(services.NewSubmissionService(storage, nil, nil), services.NewContestService(storage, nil), nil)
			w := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/submit", nil), w)
			c.Set(common.AUTH_USER_ID, "user")
			c.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.MCQ, Option: []int{1}})
			if err := controller.SubmitSolution(c); err != nil {
				t.Fatal(err)
			}
			if w.Code != 503 || w.Header().Get("Retry-After") != "1" || !strings.Contains(w.Body.String(), `"error"`) {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
			if strings.Contains(w.Body.String(), tc.failure.Error()) {
				t.Fatal("database details exposed")
			}
		})
	}
	if submissionTemporarilyUnavailable(&pgconn.PgError{Code: "42601"}) {
		t.Fatal("SQL bugs must not be disguised as transient overload")
	}
}
