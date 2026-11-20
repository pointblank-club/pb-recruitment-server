package controllers

import (
	"app/internal/common"
	"app/internal/models/dto"
	"app/internal/services"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/gommon/log"
)

type YandexController struct {
	yandexService *services.YandexService
}

func NewYandexController(yandexService *services.YandexService) *YandexController {
	return &YandexController{yandexService: yandexService}
}

// yandexError maps connect errors to a status and a stable code for the website.
func yandexError(c echo.Context, err error) error {
	status, code := http.StatusInternalServerError, "internal_error"
	message := "failed to connect Yandex account"
	switch {
	case errors.Is(err, common.ErrYandexNotConfigured):
		status, code, message = http.StatusServiceUnavailable, "yandex_not_configured", err.Error()
	case errors.Is(err, common.ErrYandexConsentRequired):
		status, code, message = http.StatusBadRequest, "consent_required", err.Error()
	case errors.Is(err, common.ErrYandexInvalidContest):
		status, code, message = http.StatusBadRequest, "invalid_contest", err.Error()
	case errors.Is(err, common.ErrYandexInvalidState):
		status, code, message = http.StatusBadRequest, "invalid_state", err.Error()
	case errors.Is(err, common.ErrYandexInvalidCode):
		status, code, message = http.StatusBadRequest, "invalid_code", err.Error()
	case errors.Is(err, common.ErrYandexAccountTaken):
		status, code, message = http.StatusConflict, "yandex_account_taken", err.Error()
	case errors.Is(err, common.ErrYandexUnavailable):
		status, code, message = http.StatusBadGateway, "yandex_unavailable", common.ErrYandexUnavailable.Error()
	case errors.Is(err, common.UserNotFoundError):
		status, code, message = http.StatusNotFound, "user_not_found", err.Error()
	default:
		log.Errorf("yandex connection failed: %v", err)
	}
	return c.JSON(status, map[string]string{"error": message, "code": code})
}

// GetConnection godoc
// @Summary      Get the candidate's backup Yandex connection
// @Description  Whether the authenticated candidate has connected their own Yandex account for the backup contest, and whether registration requires it. token_state needs_reauth means the candidate must connect again. yandex_login can be empty; show yandex_email instead.
// @Tags         Yandex
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} dto.YandexConnectionResponse
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /yandex/connection [get]
func (yc *YandexController) GetConnection(c echo.Context) error {
	userID := c.Get(common.AUTH_USER_ID).(string)
	res, err := yc.yandexService.Connection(c.Request().Context(), userID)
	if err != nil {
		return yandexError(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// StartConnection godoc
// @Summary      Start connecting a Yandex account
// @Description  Records the candidate's consent and returns the Yandex page to send them to. Yandex redirects back to the website's /yandex/callback with code and state, which the website posts to /yandex/connection/complete. The state is signed and bound to the authenticated candidate.
// @Tags         Yandex
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request body dto.StartYandexConnectionRequest true "Consent and the contest to return to"
// @Success      200 {object} dto.StartYandexConnectionResponse
// @Failure      400 {object} map[string]string "consent_required or invalid_contest"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      503 {object} map[string]string "yandex_not_configured"
// @Router       /yandex/connection/start [post]
func (yc *YandexController) StartConnection(c echo.Context) error {
	userID := c.Get(common.AUTH_USER_ID).(string)
	req := c.Get(common.VALIDATED_REQUEST_BODY).(*dto.StartYandexConnectionRequest)
	authorizeURL, err := yc.yandexService.StartConnection(userID, req.ContestID, req.Consent)
	if err != nil {
		return yandexError(c, err)
	}
	return c.JSON(http.StatusOK, dto.StartYandexConnectionResponse{AuthorizeURL: authorizeURL})
}

// CompleteConnection godoc
// @Summary      Finish connecting a Yandex account
// @Description  Exchanges the code Yandex returned, reads the Yandex account it belongs to, and saves it with encrypted tokens. Connecting again replaces the previous account. One Yandex account can belong to only one candidate.
// @Tags         Yandex
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request body dto.CompleteYandexConnectionRequest true "Code and state from the Yandex redirect"
// @Success      200 {object} dto.CompleteYandexConnectionResponse
// @Failure      400 {object} map[string]string "invalid_state or invalid_code"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      404 {object} map[string]string "user_not_found"
// @Failure      409 {object} map[string]string "yandex_account_taken"
// @Failure      502 {object} map[string]string "yandex_unavailable"
// @Failure      503 {object} map[string]string "yandex_not_configured"
// @Router       /yandex/connection/complete [post]
func (yc *YandexController) CompleteConnection(c echo.Context) error {
	userID := c.Get(common.AUTH_USER_ID).(string)
	req := c.Get(common.VALIDATED_REQUEST_BODY).(*dto.CompleteYandexConnectionRequest)
	res, contestID, err := yc.yandexService.CompleteConnection(c.Request().Context(), userID, req.Code, req.State)
	if err != nil {
		return yandexError(c, err)
	}
	return c.JSON(http.StatusOK, dto.CompleteYandexConnectionResponse{YandexConnectionResponse: *res, ContestID: contestID})
}
