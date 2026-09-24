package controllers

import (
	"app/internal/common"
	"app/internal/models/dto"
	"app/internal/services"
	"errors"
	"log"
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
	usnPattern := `^1DS2[3-5](AI|AE|AU|BT|CG|MD|ET|EC|ME|EE|CH|CB|IC|CD|CS|CV|IS|RI|EI|CY)[0-9]{3}$` // Example: 1DS24IC015
	appNumberPattern := `^26UGDS[0-9]{4}$`                                                           // Example: 25UGDS1234
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

	return ctx.NoContent(http.StatusCreated)
}

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
		log.Printf("Signup failed: %v", err)
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to sign up user"})
	}

	return ctx.JSON(http.StatusCreated, resp)
}
