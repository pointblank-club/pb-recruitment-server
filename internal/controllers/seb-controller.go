package controllers

import (
	"app/internal/common"
	"app/internal/models/dto"
	"app/internal/services"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/gommon/log"
)

func browserCookieName(contestID, userID string) string {
	scope := sha256.Sum256([]byte(contestID + "\x00" + userID))
	return "__Host-pb-exam-" + hex.EncodeToString(scope[:12])
}

func browserSessionError(err error) error {
	var stale *common.BrowserSessionStaleError
	if errors.As(err, &stale) {
		return echo.NewHTTPError(http.StatusConflict, map[string]any{"error": stale.Error(), "code": "BROWSER_SESSION_STALE", "generation": stale.Generation})
	}
	status, code := http.StatusServiceUnavailable, "BROWSER_SESSION_UNAVAILABLE"
	message := "Unable to verify your exam session. Please retry."
	switch {
	case errors.Is(err, common.ErrBrowserSessionTerminated):
		status, code, message = http.StatusForbidden, "BROWSER_SESSION_LIMIT", err.Error()
	case errors.Is(err, common.ErrBrowserSessionRequired):
		status, code, message = http.StatusPreconditionRequired, "BROWSER_SESSION_REQUIRED", err.Error()
	case errors.Is(err, common.ErrBrowserSessionAttemptUsed):
		status, code, message = http.StatusConflict, "BROWSER_SESSION_CONFLICT", err.Error()
	case errors.Is(err, common.UserNotRegisteredError), errors.Is(err, common.ContestNotRunningError),
		errors.Is(err, common.ErrContestSessionEnded), errors.Is(err, common.ErrTabSwitchTerminated):
		status, code, message = http.StatusForbidden, "BROWSER_SESSION_FORBIDDEN", err.Error()
	case errors.Is(err, common.ContestNotFoundError):
		status, code, message = http.StatusNotFound, "CONTEST_NOT_FOUND", err.Error()
	default:
		log.Errorf("exam session verification failed: %v", err)
	}
	return echo.NewHTTPError(status, map[string]string{"error": message, "code": code})
}

func observeBrowserSession(c echo.Context, service *services.ContestService, contestID, userID, attemptID string, create bool, generation *int) (*dto.BrowserSessionResponse, error) {
	name := browserCookieName(contestID, userID)
	token := ""
	if cookie, err := c.Cookie(name); err == nil {
		token = cookie.Value
	}
	res, savedToken, err := service.BrowserSession(c.Request().Context(), contestID, userID, token, attemptID, c.Request().UserAgent(), create, generation)
	if err != nil {
		return nil, browserSessionError(err)
	}
	if create && savedToken != "" && !res.IsTerminated {
		c.SetCookie(&http.Cookie{Name: name, Value: savedToken, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode})
	}
	return res, nil
}

func requireBrowserSession(c echo.Context, service *services.ContestService, contestID, userID string) error {
	res, err := observeBrowserSession(c, service, contestID, userID, "", false, nil)
	if err != nil {
		return err
	}
	if res.IsTerminated {
		return browserSessionError(common.ErrBrowserSessionTerminated)
	}
	return nil
}

// StartBrowserSession starts or resumes a cookie session; retries reuse attempt_id until the cookie is confirmed.
// @Summary Start or resume an exam browser session
// @Tags Contests
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Contest ID"
// @Param request body dto.StartBrowserSessionRequest true "Startup attempt"
// @Success 200 {object} dto.BrowserSessionResponse
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /contests/{id}/browser-session [post]
func (cc *ContestController) StartBrowserSession(c echo.Context) error {
	c.Response().Header().Set(echo.HeaderCacheControl, "private, no-store")
	userID, _ := c.Get(common.AUTH_USER_ID).(string)
	req := c.Get(common.VALIDATED_REQUEST_BODY).(*dto.StartBrowserSessionRequest)
	res, err := observeBrowserSession(c, cc.contestService, c.Param("id"), userID, req.AttemptID, true, req.Generation)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

// ResetBrowserSessions resets the allowance and lock while retaining browser flags and session history.
// @Summary Reset contestant browser sessions
// @Tags Admin - Contests
// @Security BearerAuth
// @Produce json
// @Param contestId path string true "Contest ID"
// @Param userId path string true "Participant ID"
// @Success 200 {object} dto.ResetBrowserSessionsResponse
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /admin/contests/{contestId}/contestants/{userId}/reset-browser-sessions [post]
func (cc *ContestController) ResetBrowserSessions(c echo.Context) error {
	adminID, _ := c.Get(common.AUTH_USER_ID).(string)
	if adminID == "" {
		return c.NoContent(http.StatusUnauthorized)
	}
	contestID, userID := c.Param("contestId"), c.Param("userId")
	if contestID == "" || userID == "" {
		return c.NoContent(http.StatusBadRequest)
	}
	generation, err := cc.contestService.ResetBrowserSessions(c.Request().Context(), contestID, userID, adminID)
	if err != nil {
		if errors.Is(err, common.UserNotRegisteredError) {
			return echo.NewHTTPError(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return browserSessionError(err)
	}
	return c.JSON(http.StatusOK, dto.ResetBrowserSessionsResponse{Generation: generation})
}
