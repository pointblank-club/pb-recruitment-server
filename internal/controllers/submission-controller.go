package controllers

import (
	"app/internal/common"
	"app/internal/judge0"
	_ "app/internal/middleware"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v4"
	"github.com/labstack/gommon/log"
)

type SubmissionController struct {
	submissionService *services.SubmissionService
	contestService    *services.ContestService
	judge0Client      *judge0.Client
}

func NewSubmissionController(submissionService *services.SubmissionService, contestService *services.ContestService, judge0Client *judge0.Client) *SubmissionController {
	return &SubmissionController{
		submissionService: submissionService,
		contestService:    contestService,
		judge0Client:      judge0Client,
	}
}

func (sc *SubmissionController) Judge0Callback(c echo.Context) error {
	id := c.Param("execution_id")
	if _, err := uuid.Parse(id); err != nil {
		return c.NoContent(http.StatusBadRequest)
	}
	attempt := c.QueryParam("attempt")
	if sc.judge0Client == nil || !sc.judge0Client.CallbacksEnabled() || !sc.judge0Client.VerifyCallback(id, c.QueryParam("sig"), attempt) {
		return c.NoContent(http.StatusUnauthorized)
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		var bodyErr *echo.HTTPError
		if errors.As(err, &bodyErr) && bodyErr.Code == http.StatusRequestEntityTooLarge {
			return err
		}
		return c.NoContent(http.StatusBadRequest)
	}
	var payload judge0.SubmissionStatusResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return c.NoContent(http.StatusBadRequest)
	}
	if payload.Token == "" || payload.Status.ID < 1 || payload.Status.ID > 14 {
		return c.NoContent(http.StatusBadRequest)
	}
	payload.Attempt = attempt
	if err := sc.submissionService.HandleJudge0Callback(c.Request().Context(), id, payload); err != nil {
		log.Errorf("judge0 callback failed for execution %s: %v", id, err)
		if hub := sentryecho.GetHubFromContext(c); hub != nil {
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("execution_id", id)
				hub.CaptureException(err)
			})
		}
		return c.NoContent(http.StatusInternalServerError)
	}
	return c.NoContent(http.StatusOK)
}

// GetSubmissionStatus godoc
// @Summary      Get submission status
// @Description  Get a submission's status. MCQs return attempted until the contest ends, then their actual verdict. Code verdicts are always visible.
// @Tags         Submissions
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Submission ID"
// @Success      200 {object} map[string]string "Submission status"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 "Forbidden"
// @Failure      404 "Submission not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /submission/{id}/status [get]
func (sc *SubmissionController) GetSubmissionStatus(ctx echo.Context) error {
	ctx.Response().Header().Set("Cache-Control", "private, no-store")
	id := ctx.Param("id")
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	sub, err := sc.submissionService.GetSubmissionStatusByID(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, common.ErrNotFound) {
			return ctx.NoContent(http.StatusNotFound)
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get submission status",
		})
	}

	if sub.UserID != userID {
		return ctx.NoContent(http.StatusForbidden)
	}

	return ctx.JSON(http.StatusOK, map[string]string{
		"status": string(sub.Status),
	})
}

// GetSubmissionDetails godoc
// @Summary      Get submission details
// @Description  Get submission details. MCQ status is attempted until the contest ends, then the actual verdict. Selected options remain visible.
// @Tags         Submissions
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Submission ID"
// @Success      200 {object} dto.GetSubmissionDetailsResponse
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden"
// @Failure      404 {object} map[string]string "Submission not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /submission/{id}/details [get]
func (sc *SubmissionController) GetSubmissionDetails(ctx echo.Context) error {
	ctx.Response().Header().Set("Cache-Control", "private, no-store")
	id := ctx.Param("id")
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	sub, err := sc.submissionService.GetSubmissionDetailsByID(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, common.ErrNotFound) || errors.Is(err, common.KeyNotFoundError) {
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get submission details",
		})
	}

	if sub.UserID != userID {
		return ctx.NoContent(http.StatusForbidden)
	}

	return ctx.JSON(http.StatusOK, sub)
}

// ListUserSubmissions godoc
// @Summary      List user submissions for a problem
// @Description  Get the user's submissions for a problem. MCQ statuses are attempted until the contest ends, then their actual verdicts.
// @Tags         Submissions
// @Produce      json
// @Security     BearerAuth
// @Param        problem_id query string true "Problem ID"
// @Param        page query int false "Page number (defaults to 0)"
// @Success      200 {object} dto.ListProblemSubmissionsResponse
// @Failure      400 {object} middleware.ValidationErrors "Bad request: JSON object with either an errors array (field validation) or an error string (malformed input or handler validation)"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /submission/list [get]
func (sc *SubmissionController) ListUserSubmissions(ctx echo.Context) error {
	ctx.Response().Header().Set("Cache-Control", "private, no-store")
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	req, ok := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.ListProblemSubmissionsRequest)
	if !ok {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Internal error: Request DTO not found in context",
		})
	}

	submissions, err := sc.submissionService.ListUserSubmissionsByProblemID(ctx.Request().Context(), userID, req.ProblemID, req.Page)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to list user submissions",
		})
	}

	return ctx.JSON(http.StatusOK, dto.ListProblemSubmissionsResponse{
		Submissions: submissions,
	})
}

// SubmitSolution godoc
// @Summary      Submit a solution
// @Description  Submit code or MCQ answer for a problem in a contest (user must be registered)
// @Tags         Submissions
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request body dto.SubmitSubmissionRequest true "Submission details"
// @Param        Idempotency-Key header string false "UUID reused only for retries of the same code submission"
// @Success      201 {object} dto.SubmitSubmissionResponse
// @Failure      400 {object} middleware.ValidationErrors "Bad request: JSON object with either an errors array (field validation) or an error string (malformed input or handler validation)"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 "Forbidden: empty body if not registered; JSON object with an error string if contest is not running"
// @Failure      404 "Contest or problem not found"
// @Failure      409 {object} map[string]string "Submission already exists"
// @Failure      429 {object} map[string]string "Too many requests: rate limit exceeded"
// @Failure      500 "Internal server error"
// @Router       /submission/submit [post]
func submissionTemporarilyUnavailable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) {
		return true
	}
	var connectionError *pgconn.ConnectError
	var operationError *net.OpError
	if errors.As(err, &connectionError) || errors.As(err, &operationError) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "40001", "40P01", "53300", "55P03", "57014", "57P01", "57P02", "57P03":
			return true
		}
		return strings.HasPrefix(pgError.Code, "08")
	}
	return false
}

func submissionBusy(c echo.Context, stage string, err error) error {
	log.Errorf("submission %s temporarily unavailable: %v", stage, err)
	c.Response().Header().Set(echo.HeaderRetryAfter, "1")
	return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "Submission service is busy. Please wait before retrying."})
}

func (sc *SubmissionController) SubmitSolution(ctx echo.Context) error {
	reqCtx, cancelPreparation := context.WithTimeout(ctx.Request().Context(), 5*time.Second)
	defer cancelPreparation()
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	req, ok := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.SubmitSubmissionRequest)
	if !ok {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Internal error: SubmitSubmissionRequest DTO not found in context",
		})
	}

	req.RequestID = ctx.Request().Header.Get("Idempotency-Key")
	if req.RequestID != "" {
		if _, err := uuid.Parse(req.RequestID); err != nil {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "Idempotency-Key must be a UUID"})
		}
	}
	if id, err := sc.submissionService.ReplaySubmission(reqCtx, userID, req); err != nil {
		if errors.Is(err, common.ErrIdempotencyConflict) {
			return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
		return submissionBusy(ctx, "replay lookup", err)
	} else if id != "" {
		return ctx.JSON(http.StatusCreated, dto.SubmitSubmissionResponse{SubmissionID: id})
	}

	contest_response, err := sc.contestService.GetContest(reqCtx, req.ContestID, userID)
	if err != nil {
		if errors.Is(err, common.ContestNotFoundError) {
			return ctx.NoContent(http.StatusNotFound)
		}
		if submissionTemporarilyUnavailable(err) {
			return submissionBusy(ctx, "registration lookup", err)
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to check contest registration",
		})
	}
	if contest_response.IsRegistered == nil || !*contest_response.IsRegistered {
		return ctx.NoContent(http.StatusForbidden)
	}
	if contest_response.GetRunningStatus() != models.ContestRunningOpen {
		return ctx.JSON(http.StatusForbidden, map[string]string{"error": common.ContestNotRunningError.Error()})
	}

	submissionType := req.Type

	submissionID, err := sc.submissionService.CreateSubmission(reqCtx, userID, submissionType, req)
	if err != nil {
		if errors.Is(err, common.ErrNotFound) || errors.Is(err, common.ContestNotFoundError) {
			return ctx.NoContent(http.StatusNotFound)
		}
		if errors.Is(err, common.UserNotRegisteredError) {
			return ctx.NoContent(http.StatusForbidden)
		}
		if errors.Is(err, common.ContestNotRunningError) || errors.Is(err, common.ErrTabSwitchTerminated) || errors.Is(err, common.ErrContestSessionEnded) {
			return ctx.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
		}
		if errors.Is(err, common.KeyAlreadyExistsError) || errors.Is(err, common.ErrSubmissionAlreadyExists) || errors.Is(err, common.ErrIdempotencyConflict) {
			return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
		if errors.Is(err, common.ErrUnsupportedLanguage) || errors.Is(err, common.ErrNoTestcases) || errors.Is(err, common.ErrInvalidCode) {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		if errors.Is(err, common.ErrRateLimitExceeded) {
			retryAfter := int64(5)
			var cooldown *common.SubmissionCooldownError
			if errors.As(err, &cooldown) {
				retryAfter = cooldown.RetryAfter
			}
			ctx.Response().Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			return ctx.JSON(http.StatusTooManyRequests, map[string]string{
				"error": err.Error(),
			})
		}
		if submissionTemporarilyUnavailable(err) {
			return submissionBusy(ctx, "preparation", err)
		}
		log.Errorf("create submission for user %s: %v", userID, err)
		if hub := sentryecho.GetHubFromContext(ctx); hub != nil {
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("user_id", userID)
				scope.SetTag("contest_id", req.ContestID)
				scope.SetTag("problem_id", req.ProblemID)
				hub.CaptureException(err)
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": "Submission could not be confirmed. Check your submission history before trying again."})
	}

	if submissionType == models.MCQ {
		judgeCtx, cancelJudge := context.WithTimeout(context.WithoutCancel(ctx.Request().Context()), 5*time.Second)
		defer cancelJudge()
		if err := sc.submissionService.JudgeMCQ(judgeCtx, submissionID); err != nil {
			if hub := sentryecho.GetHubFromContext(ctx); hub != nil {
				hub.WithScope(func(scope *sentry.Scope) {
					scope.SetTag("user_id", userID)
					scope.SetTag("submission_id", submissionID)
					hub.CaptureException(err)
				})
			}
			return ctx.JSON(http.StatusInternalServerError, map[string]string{
				"error": "failed to judge MCQ submission",
			})
		}
	}

	return ctx.JSON(http.StatusCreated, dto.SubmitSubmissionResponse{
		SubmissionID: submissionID,
	})
}

// GetAdminSubmissions godoc
// @Summary      Get contest coding submissions (Admin)
// @Description  Get all coding submissions made in a contest, optionally filtered by user_id and problem_id
// @Tags         Admin - Contests
// @Produce      json
// @Security     BearerAuth
// @Param        contestId path string true "Contest ID"
// @Param        user_id query string false "User ID (optional)"
// @Param        problem_id query string false "Problem ID (optional)"
// @Param        page query int false "Page number (defaults to 0)"
// @Param        page_size query int false "Page size (defaults to 50)"
// @Success      200 {array} dto.AdminSubmissionItem
// @Failure      400 {object} map[string]string "Contest ID is required"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/contests/{contestId}/submissions [get]
func (sc *SubmissionController) GetAdminSubmissions(ctx echo.Context) error {
	contestID := ctx.Param("contestId")
	if contestID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID is required",
		})
	}

	userID := ctx.QueryParam("user_id")
	problemID := ctx.QueryParam("problem_id")
	verdict := ctx.QueryParam("verdict")

	page := 0
	if p, err := strconv.Atoi(ctx.QueryParam("page")); err == nil && p >= 0 {
		page = p
	}

	pageSize := 50
	if ps, err := strconv.Atoi(ctx.QueryParam("page_size")); err == nil && ps > 0 {
		pageSize = ps
	}

	if pageSize > 200 {
		pageSize = 200
	}
	if page > int(^uint(0)>>1)/pageSize {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid pagination offset"})
	}
	submissions, totalCount, err := sc.submissionService.ListAdminSubmissions(ctx.Request().Context(), contestID, userID, problemID, verdict, page, pageSize)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to list submissions",
		})
	}

	ctx.Response().Header().Set("X-Total-Count", strconv.Itoa(totalCount))
	return ctx.JSON(http.StatusOK, submissions)
}

// GetAdminSubmissionDetails godoc
// @Summary      Get coding submission details (Admin)
// @Description  Get full execution and code details of a specific submission for admin viewing
// @Tags         Admin - Contests
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Submission ID"
// @Success      200 {object} dto.AdminSubmissionDetails
// @Failure      400 {object} map[string]string "Submission ID is required"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      404 {object} map[string]string "Submission not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/submissions/{id} [get]
func (sc *SubmissionController) GetAdminSubmissionDetails(ctx echo.Context) error {
	id := ctx.Param("id")
	if id == "" {
		id = ctx.Param("submissionId")
	}
	if id == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "submission ID is required",
		})
	}

	details, err := sc.submissionService.GetAdminSubmissionDetails(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, common.ErrNotFound) {
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": "submission not found"})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get submission details",
		})
	}

	contestID := ctx.Param("contestId")
	if contestID == "" {
		contestID = ctx.Param("contestid")
	}
	if contestID != "" && details.ContestID != contestID {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": "submission not found"})
	}

	return ctx.JSON(http.StatusOK, details)
}
