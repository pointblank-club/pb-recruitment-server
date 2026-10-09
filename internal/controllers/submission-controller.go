package controllers

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"context"
	"errors"
	"github.com/labstack/echo/v4"
	"net/http"
	"time"
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
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to check contest registration",
		})
	}
	if !*contest_response.IsRegistered {
		return ctx.NoContent(http.StatusForbidden)
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
