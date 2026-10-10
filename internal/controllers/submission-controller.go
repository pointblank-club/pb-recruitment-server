package controllers

import (
	"app/internal/common"
	_ "app/internal/middleware"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

type SubmissionController struct {
	submissionService *services.SubmissionService
	contestService    *services.ContestService
}

func NewSubmissionController(submissionService *services.SubmissionService, contestService *services.ContestService) *SubmissionController {
	return &SubmissionController{
		submissionService: submissionService,
		contestService:    contestService,
	}
}

// GetSubmissionStatus godoc
// @Summary      Get submission status
// @Description  Get current status of a specific submission (e.g. pending, accepted, wrong_answer)
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
// @Description  Get full execution details of a specific submission including test cases, runtime, memory, and code
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
// @Description  Get paginated submissions of the authenticated user for a specific problem
// @Tags         Submissions
// @Produce      json
// @Security     BearerAuth
// @Param        problem_id query string true "Problem ID"
// @Param        page query int false "Page number (defaults to 0)"
// @Success      200 {object} dto.ListProblemSubmissionsResponse
// @Failure      400 {object} middleware.ValidationErrors "Validation error"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /submission/list [get]
func (sc *SubmissionController) ListUserSubmissions(ctx echo.Context) error {
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
// @Success      201 {object} dto.SubmitSubmissionResponse
// @Failure      400 {object} middleware.ValidationErrors "Validation error"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 "Forbidden (not registered for contest or contest not running)"
// @Failure      404 "Contest or problem not found"
// @Failure      409 {object} map[string]string "Submission already exists"
// @Failure      500 "Internal server error"
// @Router       /submission/submit [post]
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

	contest_response, err := sc.contestService.GetContest(reqCtx, req.ContestID, userID)
	if err != nil {
		if errors.Is(err, common.ContestNotFoundError) {
			return ctx.NoContent(http.StatusNotFound)
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to check contest registration",
		})
	}
	if !*contest_response.IsRegistered {
		return ctx.NoContent(http.StatusForbidden)
	}
	if contest_response.GetRunningStatus() != models.ContestRunningOpen {
		return ctx.JSON(http.StatusForbidden, map[string]string{"error": common.ContestNotRunningError.Error()})
	}

	submissionType := req.Type

	submissionID, err := sc.submissionService.CreateSubmission(reqCtx, userID, submissionType, req)
	if err != nil {
		if errors.Is(err, common.ErrNotFound) {
			return ctx.NoContent(http.StatusNotFound)
		}
		if errors.Is(err, common.KeyAlreadyExistsError) {
			return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
		if errors.Is(err, common.ErrUnsupportedLanguage) || errors.Is(err, common.ErrNoTestcases) || errors.Is(err, common.ErrInvalidCode) {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return ctx.NoContent(http.StatusInternalServerError)
	}

	if submissionType == models.MCQ {
		judgeCtx, cancelJudge := context.WithTimeout(context.WithoutCancel(ctx.Request().Context()), 5*time.Second)
		defer cancelJudge()
		if err := sc.submissionService.JudgeMCQ(judgeCtx, submissionID); err != nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{
				"error": "failed to judge MCQ submission",
			})
		}
	}

	return ctx.JSON(http.StatusCreated, dto.SubmitSubmissionResponse{
		SubmissionID: submissionID,
	})
}
