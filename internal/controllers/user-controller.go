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
	"reflect"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
)

type userServicer interface {
	CreateUser(ctx context.Context, userID string, req *dto.CreateUserRequest) error
	Signup(ctx context.Context, req *dto.SignupRequest) (*dto.SignupResponse, error)
	GetUserProfile(ctx context.Context, userID string) (*models.User, error)
	UpdateUserProfile(ctx context.Context, userID string, req *dto.UpdateUserProfileRequest) error
}

type UserController struct {
	userService userServicer
}

func NewUserController(userService *services.UserService) *UserController {
	return &UserController{
		userService: userService,
	}
}

func resolveBranchCode(dept string) (string, bool) {
	return common.ResolveBranchCode(dept)
}

func validateUserInput(usn string, mobile string, currentYear int, department string) (error, string) {
	return common.ValidateUserInput(usn, mobile, currentYear, department)
}

// CreateUser godoc
// @Summary      Create user profile
// @Description  Create a new user profile after Firebase registration
// @Tags         Users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request body dto.CreateUserRequest true "User details"
// @Success      201 "User created successfully"
// @Failure      400 {object} middleware.ValidationErrors "Bad request: JSON object with either an errors array (field validation) or an error string (malformed input or handler validation)"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      409 {object} map[string]string "User already exists"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /users/create [post]
func (uc *UserController) CreateUser(ctx echo.Context) error {
	reqBody := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.CreateUserRequest)
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	if ctx.Get(common.USER_INPUT_VALIDATED) != true {
		if err, field := validateUserInput(reqBody.USN, reqBody.MobileNumber, reqBody.CurrentYear, reqBody.Department); err != nil {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error(), "field": field})
		}

		reqBody.USN = strings.ToUpper(strings.TrimSpace(reqBody.USN))
		reqBody.MobileNumber = strings.TrimSpace(reqBody.MobileNumber)
	}

	if err := uc.userService.CreateUser(ctx.Request().Context(), userID, reqBody); err != nil {
		if errors.Is(err, common.USNAlreadyExistsError) {
			return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error(), "field": "id"})
		}
		if errors.Is(err, common.EmailAlreadyExistsError) {
			return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error(), "field": "email"})
		}
		if errors.Is(err, common.UserAlreadyExistsError) {
			return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": common.CreateUserFailedError.Error()})
	}

	return ctx.NoContent(http.StatusCreated)
}

// GetUserProfile godoc
// @Summary      Get current user profile
// @Description  Retrieve the profile of the authenticated user
// @Tags         Users
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} models.User
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      404 {object} map[string]string "User not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /users/profile [get]
func (uc *UserController) GetUserProfile(ctx echo.Context) error {
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	user, err := uc.userService.GetUserProfile(ctx.Request().Context(), userID)
	if err != nil {
		if errors.Is(err, common.UserNotFoundError) {
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": common.UserNotFoundError.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": common.FetchUserFailedError.Error()})
	}
	if user == nil {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": common.UserNotFoundError.Error()})
	}

	return ctx.JSON(http.StatusOK, user)
}

// UpdateUserProfile godoc
// @Summary      Update user profile
// @Description  Update the profile details of the authenticated user
// @Tags         Users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request body dto.UpdateUserProfileRequest true "Updated user details"
// @Success      204 "User profile updated successfully"
// @Failure      400 {object} middleware.ValidationErrors "Bad request: JSON object with either an errors array (field validation) or an error string (malformed input or handler validation)"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      404 {object} map[string]string "User not found"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /users/profile [post]
func (uc *UserController) UpdateUserProfile(ctx echo.Context) error {
	userID := ctx.Get(common.AUTH_USER_ID).(string)
	reqBody := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.UpdateUserProfileRequest)

	// mob no. validation
	phonePattern := `^[0-9]{10}$`
	if matched, _ := regexp.MatchString(phonePattern, reqBody.MobileNumber); !matched {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": common.InvalidMobileNumberError.Error()})
	}

	user, err := uc.userService.GetUserProfile(ctx.Request().Context(), userID)
	if err != nil {
		if errors.Is(err, common.UserNotFoundError) {
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": common.UserNotFoundError.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": common.FetchUserFailedError.Error()})
	}
	if user == nil {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": common.UserNotFoundError.Error()})
	}

	if reqBody.Department != "" {
		curDept := strings.TrimSpace(user.Department)
		newDept := strings.TrimSpace(reqBody.Department)
		if curDept == "" || !strings.EqualFold(curDept, newDept) {
			codeCur, okCur := resolveBranchCode(curDept)
			codeNew, okNew := resolveBranchCode(newDept)
			if !okCur || !okNew || codeNew != codeCur {
				return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "department cannot be changed", "field": "department"})
			}
		}
	}

	if err := uc.userService.UpdateUserProfile(ctx.Request().Context(), userID, reqBody); err != nil {
		if errors.Is(err, common.UserNotFoundError) {
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": common.UpdateUserFailedError.Error()})
	}

	return ctx.NoContent(http.StatusNoContent)
}

// Signup godoc
// @Summary      Sign up user
// @Description  Register a new user account with credentials and profile details
// @Tags         Auth
// @Accept       json
// @Produce      json
// @Param        request body dto.SignupRequest true "Signup payload"
// @Success      201 {object} dto.SignupResponse
// @Failure      400 {object} middleware.ValidationErrors "Bad request: JSON object with either an errors array (field validation) or an error string (malformed input or handler validation)"
// @Failure      409 {object} map[string]string "User already exists"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /auth/signup [post]
func (uc *UserController) Signup(ctx echo.Context) error {
	reqBody := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.SignupRequest)

	if ctx.Get(common.USER_INPUT_VALIDATED) != true {
		if err, field := validateUserInput(reqBody.USN, reqBody.MobileNumber, reqBody.CurrentYear, reqBody.Department); err != nil {
			return ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error(), "field": field})
		}

		reqBody.USN = strings.ToUpper(strings.TrimSpace(reqBody.USN))
		reqBody.MobileNumber = strings.TrimSpace(reqBody.MobileNumber)
	}

	if uc.userService == nil || reflect.ValueOf(uc.userService).IsNil() {
		return ctx.JSON(http.StatusServiceUnavailable, map[string]string{"error": "user service unavailable"})
	}

	resp, err := uc.userService.Signup(ctx.Request().Context(), reqBody)
	if err != nil {
		if errors.Is(err, common.USNAlreadyExistsError) {
			return ctx.JSON(http.StatusConflict, map[string]string{
				"error": common.USNAlreadyExistsError.Error(),
				"field": "id",
			})
		}
		if errors.Is(err, common.EmailAlreadyExistsError) {
			return ctx.JSON(http.StatusConflict, map[string]string{
				"error": common.EmailAlreadyExistsError.Error(),
				"field": "email",
			})
		}
		if errors.Is(err, common.UserAlreadyExistsError) {
			return ctx.JSON(http.StatusConflict, map[string]string{
				"error": common.UserAlreadyExistsError.Error(),
			})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to sign up user"})
	}

	return ctx.JSON(http.StatusCreated, resp)
}
