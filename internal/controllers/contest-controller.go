package controllers

import (
	"app/internal/common"
	_ "app/internal/middleware"
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

// ModifyRegistration godoc
// @Summary      Register or unregister for a contest
// @Description  Register or unregister the authenticated user for a specific contest
// @Tags         Contests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Contest ID"
// @Param        request body dto.ModifyRegistrationRequest true "Action (register or unregister)"
// @Success      200 "Registration status modified"
// @Failure      400 {object} middleware.ValidationErrors "Validation error"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Registration closed or invalid student year"
// @Failure      404 {object} map[string]string "Contest or user not found"
// @Failure      409 {object} map[string]string "User already registered"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /contests/{id}/registration [post]
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

// ListContests godoc
// @Summary      List all contests
// @Description  Get a paginated list of all contests
// @Tags         Contests
// @Produce      json
// @Security     BearerAuth
// @Param        page query int false "Page number (defaults to 0)"
// @Success      200 {array} models.Contest
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /contests/list [get]
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

// HandleListContestsAdmin godoc
// @Summary      List all contests (Admin)
// @Description  Get a paginated list of all contests for admin
// @Tags         Admin - Contests
// @Produce      json
// @Security     BearerAuth
// @Param        page query int false "Page number (defaults to 0)"
// @Success      200 {array} models.Contest
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/contests/list [get]
func (cc *ContestController) HandleListContestsAdmin(ctx echo.Context) error {
	return cc.ListContests(ctx)
}

// HandleGetContestAdmin godoc
// @Summary      Get contest details (Admin)
// @Description  Get details of a contest for admin
// @Tags         Admin - Contests
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Contest ID"
// @Success      200 {object} dto.GetContestResponse
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      404 {object} map[string]string "Contest not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/contest/{id} [get]
func (cc *ContestController) HandleGetContestAdmin(ctx echo.Context) error {
	return cc.GetContest(ctx)
}

// HandleCreateContest godoc
// @Summary      Create contest (Admin)
// @Description  Create a new contest with registration and contest timelines
// @Tags         Admin - Contests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request body dto.UpsertContestRequest true "Contest details"
// @Success      201 {object} models.Contest
// @Failure      400 {object} middleware.ValidationErrors "Validation error"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/contest [post]
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

// HandleUpdateContest godoc
// @Summary      Update contest (Admin)
// @Description  Update details of an existing contest
// @Tags         Admin - Contests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Contest ID"
// @Param        request body dto.UpsertContestRequest true "Contest payload"
// @Success      200 {object} models.Contest
// @Failure      400 {object} middleware.ValidationErrors "Validation error"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      404 "Contest not found"
// @Failure      409 {object} map[string]string "Start time cannot be changed after the contest starts"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/contest/{id} [put]
func (cc *ContestController) HandleUpdateContest(ctx echo.Context) error {
	req := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.UpsertContestRequest)
	if req.Name == "" || req.StartTime == 0 || req.EndTime == 0 {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "name, start_time, and end_time are required fields",
		})
	}

	// Verify contest exists
	id := ctx.Param("id")
	contest, err := cc.contestService.GetContest(ctx.Request().Context(), id, "")
	if err != nil {
		if errors.Is(err, common.ContestNotFoundError) {
			return ctx.NoContent(http.StatusNotFound)
		}
		return ctx.NoContent(http.StatusInternalServerError)
	}
	if contest.GetRunningStatus() != models.ContestRunningUpcoming && req.StartTime != contest.StartTime {
		return ctx.JSON(http.StatusConflict, map[string]string{
			"error": "start time cannot be changed after the contest starts",
		})
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

// HandleDeleteContest godoc
// @Summary      Delete contest (Admin)
// @Description  Delete an existing contest and its associated data
// @Tags         Admin - Contests
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Contest ID"
// @Success      200 {object} map[string]string "Contest deleted successfully"
// @Failure      400 {object} map[string]string "Contest ID required"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/contest/{id} [delete]
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

// HandleCreateProblem godoc
// @Summary      Create problem in contest (Admin)
// @Description  Create a new MCQ or Code problem in a contest
// @Tags         Admin - Problems
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        contestid path string true "Contest ID"
// @Param        request body dto.CreateProblemRequest true "Problem details"
// @Success      201 {object} models.Problem
// @Failure      400 {object} middleware.ValidationErrors "Validation error or invalid answer"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/{contestid}/problem [post]
func (cc *ContestController) HandleCreateProblem(ctx echo.Context) error {

	contestID := ctx.Param("contestid")
	if contestID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID is required",
		})
	}

	req := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.CreateProblemRequest)

	createdProblem, err := cc.contestService.CreateProblem(ctx.Request().Context(), contestID, req)
	if err != nil {
		if errors.Is(err, common.ErrInvalidAnswer) {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to create problem",
		})
	}

	return ctx.JSON(http.StatusCreated, createdProblem)
}

// HandleUpdateProblem godoc
// @Summary      Update problem in contest (Admin)
// @Description  Update details of a problem within a contest
// @Tags         Admin - Problems
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        contestid path string true "Contest ID"
// @Param        problemid path string true "Problem ID"
// @Param        request body dto.CreateProblemRequest true "Problem details"
// @Success      200 {object} models.Problem
// @Failure      400 {object} middleware.ValidationErrors "Validation error or invalid answer"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      409 {object} map[string]string "Problems are locked"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/{contestid}/problem/{problemid} [put]
func (cc *ContestController) HandleUpdateProblem(ctx echo.Context) error {

	contestID := ctx.Param("contestid")
	problemID := ctx.Param("problemid")
	if contestID == "" || problemID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID and problem ID are required",
		})
	}

	req := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.CreateProblemRequest)

	updatedProblem, err := cc.contestService.UpdateProblem(ctx.Request().Context(), contestID, problemID, req)
	if err != nil {
		if errors.Is(err, common.ErrInvalidAnswer) {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		if errors.Is(err, common.ErrProblemsLocked) {
			return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to update problem",
		})
	}

	return ctx.JSON(http.StatusOK, updatedProblem)
}

// HandleDeleteProblem godoc
// @Summary      Delete problem from contest (Admin)
// @Description  Delete a problem from a contest
// @Tags         Admin - Problems
// @Produce      json
// @Security     BearerAuth
// @Param        contestid path string true "Contest ID"
// @Param        problemid path string true "Problem ID"
// @Success      200 {object} map[string]string "Problem deleted successfully"
// @Failure      400 {object} map[string]string "Invalid IDs"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      404 {object} map[string]string "Problem not found"
// @Failure      409 {object} map[string]string "Problems are locked"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/{contestid}/problem/{problemid} [delete]
func (cc *ContestController) HandleDeleteProblem(ctx echo.Context) error {

	contestID := ctx.Param("contestid")
	problemID := ctx.Param("problemid")
	if contestID == "" || problemID == "" || problemID == "undefined" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID and problem ID are required",
		})
	}

	err := cc.contestService.DeleteProblem(ctx.Request().Context(), contestID, problemID)
	if err != nil {
		if errors.Is(err, common.ErrProblemsLocked) {
			return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
		if errors.Is(err, common.ContestNotFoundError) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "problem not found",
			})
		}
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

// HandleUpdateLeaderboardUser godoc
// @Summary      Update user status on leaderboard (Admin)
// @Description  Hide or disqualify a user from the contest leaderboard
// @Tags         Admin - Leaderboard
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        contestid path string true "Contest ID"
// @Param        userid path string true "User ID"
// @Param        request body dto.UpdateLeaderboardUserRequest true "Update payload"
// @Success      200 {object} map[string]string "Leaderboard user updated"
// @Failure      400 {object} map[string]string "Invalid input"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/{contestid}/leaderboard/{userid} [put]
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

// GetContest godoc
// @Summary      Get contest details
// @Description  Get details of a contest. If user is authenticated, returns registration status as well.
// @Tags         Contests
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Contest ID"
// @Success      200 {object} dto.GetContestResponse
// @Failure      404 {object} map[string]string "Contest not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /contests/{id} [get]
func (cc *ContestController) GetContest(ctx echo.Context) error {
	contestID := ctx.Param("id")

	userID, ok := ctx.Get(common.AUTH_USER_ID).(string)
	if !ok {
		userID = ""
	}

	contest, err := cc.contestService.GetContest(ctx.Request().Context(), contestID, userID)
	if err != nil {
		if errors.Is(err, common.ContestNotFoundError) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": common.ContestNotFoundError.Error(),
			})
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

// GetContestProblemsList godoc
// @Summary      Get list of problems in contest
// @Description  Get problems overview (names, score, type) for authenticated and registered user
// @Tags         Contests
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Contest ID"
// @Success      200 {array} dto.ProblemOverview
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Not registered or contest not running"
// @Failure      404 {object} map[string]string "Contest not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /contests/{id}/problems [get]
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

// GetContestProblem godoc
// @Summary      Get problem statement
// @Description  Get detailed problem statement and options/sample testcases for authenticated and registered user
// @Tags         Contests
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Contest ID"
// @Param        problem_id path string true "Problem ID"
// @Success      200 {object} dto.GetProblemStatementResponse
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Not registered or contest not running"
// @Failure      404 {object} map[string]string "Contest or problem not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /contests/{id}/problems/{problem_id} [get]
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

	problem, err := cc.contestService.GetContestProblem(ctx.Request().Context(), contestID, problemID, false)
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

// GetContestRegistrations godoc
// @Summary      Get contest registrations (Admin)
// @Description  Get list of all registered participants for a contest
// @Tags         Admin - Contests
// @Produce      json
// @Security     BearerAuth
// @Param        contestId path string true "Contest ID"
// @Success      200 {array} dto.ContestRegistration
// @Failure      400 {object} map[string]string "Contest ID required"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/contests/{contestId}/registrations [get]
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

// HandleListProblemsAdmin godoc
// @Summary      List problems in contest (Admin)
// @Description  Get all problems of a contest including unreleased items
// @Tags         Admin - Problems
// @Produce      json
// @Security     BearerAuth
// @Param        contestid path string true "Contest ID"
// @Success      200 {array} dto.ProblemOverview
// @Failure      400 {object} map[string]string "Contest ID required"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/{contestid}/problems [get]
func (cc *ContestController) HandleListProblemsAdmin(ctx echo.Context) error {
	contestID := ctx.Param("contestid")
	if contestID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID is required",
		})
	}

	problems, err := cc.contestService.GetContestProblemsListAdmin(ctx.Request().Context(), contestID)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to list contest problems",
		})
	}

	return ctx.JSON(http.StatusOK, problems)
}

// HandleGetProblemAdmin godoc
// @Summary      Get problem details (Admin)
// @Description  Get full problem statement and answer/test cases for admin
// @Tags         Admin - Problems
// @Produce      json
// @Security     BearerAuth
// @Param        contestid path string true "Contest ID"
// @Param        problemid path string true "Problem ID"
// @Success      200 {object} dto.GetProblemStatementResponse
// @Failure      400 {object} map[string]string "Invalid parameters"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      404 {object} map[string]string "Problem not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/{contestid}/problem/{problemid} [get]
func (cc *ContestController) HandleGetProblemAdmin(ctx echo.Context) error {
	contestID := ctx.Param("contestid")
	problemID := ctx.Param("problemid")
	if contestID == "" || problemID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "contest ID and problem ID are required",
		})
	}

	problem, err := cc.contestService.GetContestProblem(ctx.Request().Context(), contestID, problemID, true)
	if err != nil {
		if errors.Is(err, common.ContestNotFoundError) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "problem not found",
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get problem",
		})
	}

	return ctx.JSON(http.StatusOK, problem)
}

// GetProblemTestcases godoc
// @Summary      Get problem testcases (Admin)
// @Description  Get all testcases for a code problem
// @Tags         Admin - Problems
// @Produce      json
// @Security     BearerAuth
// @Param        contestid path string true "Contest ID"
// @Param        problemid path string true "Problem ID"
// @Success      200 {array} dto.TestCaseResponse
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      404 {object} map[string]string "Contest not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/{contestid}/{problemid}/testcases [get]
func (cc *ContestController) GetProblemTestcases(ctx echo.Context) error {
	contestID := ctx.Param("contestid")
	problemID := ctx.Param("problemid")

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

// GetProblemAnswers godoc
// @Summary      Get problem answers (Admin)
// @Description  Get answers for a problem
// @Tags         Admin - Problems
// @Produce      json
// @Security     BearerAuth
// @Param        contestid path string true "Contest ID"
// @Param        problemid path string true "Problem ID"
// @Success      200 {array} string
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      403 {object} map[string]string "Forbidden - Admin access required"
// @Failure      404 {object} map[string]string "Contest not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /admin/{contestid}/{problemid}/answers [get]
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

// GetLeaderboard godoc
// @Summary      Get contest leaderboard
// @Description  Get ranked participants and scores for a contest with pagination
// @Tags         Contests
// @Produce      json
// @Param        id path string true "Contest ID"
// @Param        page query int false "Page number (defaults to 0)"
// @Success      200 {array} dto.LeaderboardEntry
// @Header       200 {string} X-Total-Count "Total count of leaderboard entries"
// @Header       200 {string} X-Total-Pages "Total pages"
// @Header       200 {string} X-Current-Page "Current page"
// @Failure      404 {object} map[string]string "Contest not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /contests/{id}/leaderboard [get]
func (cc *ContestController) GetLeaderboard(ctx echo.Context) error {
	contestID := ctx.Param("id")

	pageStr := ctx.QueryParam("page")
	page, err := strconv.Atoi(pageStr)
	if err != nil {
		page = 0
	}

	leaderboard, err := cc.contestService.GetLeaderboard(ctx.Request().Context(), contestID, page)
	if err != nil {
		if errors.Is(err, common.ContestNotFoundError) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": common.ContestNotFoundError.Error(),
			})
		}
		log.Errorf("failed to get leaderboard for contest %s: %v", contestID, err)
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get leaderboard",
		})
	}

	ctx.Response().Header().Set("X-Total-Count", strconv.Itoa(leaderboard.TotalCount))
	ctx.Response().Header().Set("X-Total-Pages", strconv.Itoa(leaderboard.TotalPages))
	ctx.Response().Header().Set("X-Current-Page", strconv.Itoa(leaderboard.Page))

	return ctx.JSON(http.StatusOK, leaderboard.Entries)
}
