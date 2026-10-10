package controllers

import (
	"app/internal/common"
	_ "app/internal/middleware"
	_ "app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
)

type UserController struct {
	userService *services.UserService
}

func NewUserController(userService *services.UserService) *UserController {
	return &UserController{
		userService: userService,
	}
}

func validateUserInput(usn string, mobile string, currentYear int) error {
	usnPattern := `^1DS2[4-5](AI|AE|AU|BT|CG|MD|ET|EC|ME|EE|CH|CB|IC|CD|CS|CV|IS|RI|EI|CY)[0-9]{3}$` // Example: 1DS24IC015
	appNumberPattern := `^26UGDS[0-9]{4}$`                                                           // Example: 26UGDS1234
	phonePattern := `^[0-9]{10}$`                                                                    // Example: 9234567890

	usnUpper := strings.ToUpper(usn)

	if currentYear == 1 {
		if matched, _ := regexp.MatchString(appNumberPattern, usnUpper); !matched {
			return common.InvalidApplicationNumberError
		}
	} else {
		if matched, _ := regexp.MatchString(usnPattern, usnUpper); !matched {
			return common.InvalidUSNError
		}
	}

	if matched, _ := regexp.MatchString(phonePattern, mobile); !matched {
		return common.InvalidMobileNumberError
	}
	return nil
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
// @Failure      400 {object} middleware.ValidationErrors "Validation error"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      409 {object} map[string]string "User already exists"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /users/create [post]
func (uc *UserController) CreateUser(ctx echo.Context) error {
	reqBody := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.CreateUserRequest)
	userID := ctx.Get(common.AUTH_USER_ID).(string)

	if err := validateUserInput(reqBody.USN, reqBody.MobileNumber, reqBody.CurrentYear); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := uc.userService.CreateUser(ctx.Request().Context(), userID, reqBody); err != nil {
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
			return ctx.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
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
// @Failure      400 {object} middleware.ValidationErrors "Validation error"
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
// @Failure      400 {object} middleware.ValidationErrors "Validation error"
// @Failure      409 {object} map[string]string "User already exists"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /auth/signup [post]
func (uc *UserController) Signup(ctx echo.Context) error {
	reqBody := ctx.Get(common.VALIDATED_REQUEST_BODY).(*dto.SignupRequest)

	if err := validateUserInput(reqBody.USN, reqBody.MobileNumber, reqBody.CurrentYear); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	resp, err := uc.userService.Signup(ctx.Request().Context(), reqBody)
	if err != nil {
		if errors.Is(err, common.UserAlreadyExistsError) {
			return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to sign up user"})
	}

	return ctx.JSON(http.StatusCreated, resp)
}
