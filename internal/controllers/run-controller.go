package controllers

import (
	"app/internal/common"
	"app/internal/models/dto"
	"app/internal/services"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
)

// Run godoc
// @Summary Run code with custom stdin
// @Description Available only with ENABLE_RUN=true; otherwise returns 404. Limited to one attempt per user per five seconds and one fleet admission per database clock second. Returns 429 while scored code submissions are pending or Judge0 rejects a full queue. Polls for up to 15 seconds. No contest submission or ranking changes. Code and stdin are plain UTF-8. Each output field is capped at 4096 UTF-8 bytes. Runtime is milliseconds; memory is KiB. A timeout does not cancel the Judge0 job; do not automatically retry.
// @Tags Submissions
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.RunRequest true "Language, code (64 KiB), stdin (64 KiB)"
// @Success 200 {object} dto.RunResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 "Run is disabled"
// @Failure 413 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Failure 502 {object} map[string]string
// @Failure 504 {object} map[string]string
// @Router /submission/run [post]
func (sc *SubmissionController) Run(c echo.Context) error {
	userID, _ := c.Get(common.AUTH_USER_ID).(string)
	if userID == "" {
		return c.NoContent(http.StatusUnauthorized)
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, 1<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return c.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "request too large"})
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid run request"})
	}
	if !utf8.Valid(body) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid UTF-8 run request"})
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var req dto.RunRequest
	if err := decoder.Decode(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid run request"})
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "expected one JSON object"})
	}
	result, err := sc.submissionService.Run(c.Request().Context(), userID, req)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidRun):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, common.ErrRateLimitExceeded):
			c.Response().Header().Set("Retry-After", "5")
			return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "run capacity busy; wait five seconds before trying again"})
		case errors.Is(err, context.DeadlineExceeded), c.Request().Context().Err() != nil:
			return c.JSON(http.StatusGatewayTimeout, map[string]string{"error": "run timed out; execution may still finish"})
		default:
			return c.JSON(http.StatusBadGateway, map[string]string{"error": "judge unavailable; execution may have been accepted"})
		}
	}
	return c.JSON(http.StatusOK, result)
}
