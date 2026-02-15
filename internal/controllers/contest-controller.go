package controllers

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/labstack/gommon/log"
	gonanoid "github.com/matoous/go-nanoid/v2"
)

type ContestController struct {
	contestService *services.ContestService
}

func NewContestController(contestService *services.ContestService) *ContestController {
	return &ContestController{
		contestService: contestService,
	}
}

func (cc *ContestController) ModifyRegistration(ctx echo.Context) error {
	contestID := ctx.Param("id")
	userID := ctx.Get(common.AUTH_USER_ID).(string)
	reqBody := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.ModifyRegistrationRequest)

	if err := cc.contestService.ModifyRegistration(ctx.Request().Context(), contestID, userID, reqBody.Action); err != nil {
		if err == common.ContestRegistrationClosedError ||
			err == common.InvalidYearError {
			return ctx.JSON(http.StatusForbidden, map[string]string{
				"error": err.Error(),
			})
		} else if err == common.ContestNotFoundError ||
			err == common.UserNotFoundError {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": err.Error(),
			})
		} else if err == common.UserAlreadyExistsError {
			return ctx.JSON(http.StatusConflict, map[string]string{
				"error": err.Error(),
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to modify registration",
		})
	}

	return ctx.NoContent(http.StatusOK)
}

func (cc *ContestController) ListContests(ctx echo.Context) error {
	pageStr := ctx.QueryParam("page")

	page, err := strconv.Atoi(pageStr)
	if err != nil {
		page = 0
	}

	contests, err := cc.contestService.ListContests(ctx.Request().Context(), page)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list contests"})
	}
	return ctx.JSON(http.StatusOK, contests)
}

// Admin Handlers
func (cc *ContestController) HandleCreateContest(ctx echo.Context) error {
	request := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.UpsertContestRequest)
	if request.Name == "" || request.StartTime == 0 || request.EndTime == 0 {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "name, start_time, and end_time are required fields",
		})
	}

	id, err := gonanoid.Generate("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", 10)
	if err != nil {
		log.Errorf("failed to generate contest ID: %v", err)
		return ctx.NoContent(http.StatusInternalServerError)
	}
	newContest := models.Contest{
		ID:                    id,
		Name:                  request.Name,
		Description:           request.Description,
		RegistrationStartTime: request.RegistrationStartTime,
		RegistrationEndTime:   request.RegistrationEndTime,
		StartTime:             request.StartTime,
		EndTime:               request.EndTime,
		EligibleTo:            request.EligibleTo,
	}
	createdContest, err := cc.contestService.CreateContest(ctx.Request().Context(), &newContest)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to create contest",
		})
	}

	return ctx.JSON(http.StatusCreated, createdContest)
}

func (cc *ContestController) HandleUpdateContest(ctx echo.Context) error {
	req := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.UpsertContestRequest)
	if req.Name == "" || req.StartTime == 0 || req.EndTime == 0 {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "name, start_time, and end_time are required fields",
		})
	}

	// Verify contest exists
	id := ctx.Param("id")
	_, err := cc.contestService.GetContest(ctx.Request().Context(), id, "")
	if err != nil {
		if errors.Is(err, common.ContestNotFoundError) {
			return ctx.NoContent(http.StatusNotFound)
		}
		return ctx.NoContent(http.StatusInternalServerError)
	}

	contestToUpdate := models.Contest{
		ID:                    id,
		Name:                  req.Name,
		Description:           req.Description,
		RegistrationStartTime: req.RegistrationStartTime,
		RegistrationEndTime:   req.RegistrationEndTime,
		StartTime:             req.StartTime,
		EndTime:               req.EndTime,
		EligibleTo:            req.EligibleTo,
	}
	updatedContest, err := cc.contestService.UpdateContest(ctx.Request().Context(), &contestToUpdate)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to update contest",
		})
	}

	return ctx.JSON(http.StatusOK, updatedContest)
}

func (cc *ContestController) HandleDeleteContest(ctx echo.Context) error {

	contestID := ctx.Param("id")
	if contestID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID is required",
		})
	}

	err := cc.contestService.DeleteContest(ctx.Request().Context(), contestID)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to delete contest",
		})
	}

	return ctx.JSON(http.StatusOK, map[string]string{
		"message":   "contest deleted successfully",
		"contestID": contestID,
	})
}

func (cc *ContestController) HandleCreateProblem(ctx echo.Context) error {

	contestID := ctx.Param("contestid")
	if contestID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID is required",
		})
	}

	var req dto.CreateProblemRequest
	if err := ctx.Bind(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if req.Name == "" || req.Score <= 0 || req.Type == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "name, score, and type are required fields",
		})
	}

	createdProblem, err := cc.contestService.CreateProblem(ctx.Request().Context(), contestID, &req)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to create problem",
		})
	}

	return ctx.JSON(http.StatusCreated, createdProblem)
}

func (cc *ContestController) HandleUpdateProblem(ctx echo.Context) error {

	contestID := ctx.Param("contestid")
	problemID := ctx.Param("problemid")
	if contestID == "" || problemID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID and problem ID are required",
		})
	}

	var req dto.CreateProblemRequest
	if err := ctx.Bind(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if req.Name == "" || req.Score <= 0 || req.Type == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "name, score, and type are required fields",
		})
	}

	updatedProblem, err := cc.contestService.UpdateProblem(ctx.Request().Context(), contestID, problemID, &req)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to update problem",
		})
	}

	return ctx.JSON(http.StatusOK, updatedProblem)
}

func (cc *ContestController) HandleDeleteProblem(ctx echo.Context) error {

	contestID := ctx.Param("contestid")
	problemID := ctx.Param("problemid")
	if contestID == "" || problemID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID and problem ID are required",
		})
	}

	err := cc.contestService.DeleteProblem(ctx.Request().Context(), contestID, problemID)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to delete problem",
		})
	}

	return ctx.JSON(http.StatusOK, map[string]string{
		"message":   "problem deleted successfully",
		"contestID": contestID,
		"problemID": problemID,
	})
}

func (cc *ContestController) HandleUpdateLeaderboardUser(ctx echo.Context) error {

	contestID := ctx.Param("contestid")
	userID := ctx.Param("userid")
	if contestID == "" || userID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID and user ID are required",
		})
	}

	var req dto.UpdateLeaderboardUserRequest
	if err := ctx.Bind(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if req.Hidden == nil && req.Disqualified == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "at least one field (hidden or disqualified) must be provided",
		})
	}

	err := cc.contestService.UpdateLeaderboardUser(ctx.Request().Context(), contestID, userID, &req)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to update leaderboard",
		})
	}

	return ctx.JSON(http.StatusOK, map[string]string{
		"message":   "leaderboard user updated successfully",
		"contestID": contestID,
		"userID":    userID,
	})
}
func (cc *ContestController) GetContest(ctx echo.Context) error {
	contestID := ctx.Param("id")

	userID, ok := ctx.Get(common.AUTH_USER_ID).(string)
	if !ok {
		userID = ""
	}

	contest, err := cc.contestService.GetContest(ctx.Request().Context(), contestID, userID)
	if err != nil {
		if errors.Is(err, common.ContestNotFoundError) {
			return ctx.NoContent(http.StatusNotFound)
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": common.FetchContestFailedError.Error(),
		})
	}

	if contest == nil {
		return ctx.JSON(http.StatusNotFound, map[string]string{
			"error": common.ContestNotFoundError.Error(),
		})
	}

	return ctx.JSON(http.StatusOK, contest)
}

func (cc *ContestController) GetContestProblemsList(ctx echo.Context) error {
	contestID := ctx.Param("id")
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	err := cc.contestService.GetProblemVisibility(ctx.Request().Context(), contestID, userID)
	if err != nil {
		if err == common.ContestNotFoundError {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": common.ContestNotFoundError.Error(),
			})
		} else if err == common.UserNotRegisteredError ||
			err == common.ContestNotRunningError {
			return ctx.JSON(http.StatusForbidden, map[string]string{
				"error": err.Error(),
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": common.FetchContestFailedError.Error(),
		})
	}

	problems, err := cc.contestService.GetContestProblemsList(ctx.Request().Context(), contestID)
	if err != nil {
		if err == common.ContestNotFoundError {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": common.ContestNotFoundError.Error(),
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get contest problems",
		})
	}

	return ctx.JSON(http.StatusOK, problems)
}

func (cc *ContestController) GetContestProblem(ctx echo.Context) error {
	contestID := ctx.Param("id")
	problemID := ctx.Param("problem_id")
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	err := cc.contestService.GetProblemVisibility(ctx.Request().Context(), contestID, userID)
	if err != nil {
		if err == common.ContestNotFoundError {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": common.ContestNotFoundError.Error(),
			})
		} else if err == common.UserNotRegisteredError ||
			err == common.ContestNotRunningError {
			return ctx.JSON(http.StatusForbidden, map[string]string{
				"error": err.Error(),
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": common.FetchContestFailedError.Error(),
		})
	}

	problem, err := cc.contestService.GetContestProblem(ctx.Request().Context(), contestID, problemID)
	if err != nil {
		if err == common.ContestNotFoundError {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": common.ContestNotFoundError.Error(),
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get problem statement",
		})
	}

	return ctx.JSON(http.StatusOK, problem)
}

func (cc *ContestController) GetContestRegistrations(ctx echo.Context) error {
	contestID := ctx.Param("contestId")
	if contestID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID is required",
		})
	}

	registrations, err := cc.contestService.GetContestRegistrations(ctx.Request().Context(), contestID)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get contest registrations",
		})
	}

	return ctx.JSON(http.StatusOK, registrations)
}

func (cc *ContestController) GetProblemTestcases(ctx echo.Context) error {
	contestID := ctx.Param("contestid")
	problemID := ctx.Param("problemid")
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	err := cc.contestService.GetProblemVisibility(ctx.Request().Context(), contestID, userID)
	if err != nil {
		if err == common.ContestNotFoundError {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": common.ContestNotFoundError.Error(),
			})
		} else if err == common.UserNotRegisteredError ||
			err == common.ContestNotRunningError {
			return ctx.JSON(http.StatusForbidden, map[string]string{
				"error": err.Error(),
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": common.FetchContestFailedError.Error(),
		})
	}

	testcases, err := cc.contestService.GetProblemTestcases(ctx.Request().Context(), contestID, problemID)
	if err != nil {
		if err == common.ContestNotFoundError {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": common.ContestNotFoundError.Error(),
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to load problem testcases",
		})
	}

	return ctx.JSON(http.StatusOK, testcases)
}

func (cc *ContestController) GetProblemAnswers(ctx echo.Context) error {
	contestID := ctx.Param("contestid")
	problemID := ctx.Param("problemid")

	answers, err := cc.contestService.GetProblemAnswers(ctx.Request().Context(), contestID, problemID)
	if err != nil {
		if err == common.ContestNotFoundError {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": common.ContestNotFoundError.Error(),
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get problem answers",
		})
	}

	return ctx.JSON(http.StatusOK, answers)
}
