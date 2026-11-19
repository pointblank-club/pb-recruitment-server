package controllers

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

type yandexRegistrationUsers struct{ *stores.UserStore }

func (yandexRegistrationUsers) GetUserProfile(_ context.Context, userID string) (*models.User, error) {
	return &models.User{ID: userID, CurrentYear: 2}, nil
}

type noYandexAccounts struct{}

func (noYandexAccounts) GetYandexAccount(context.Context, string) (*models.YandexAccount, error) {
	return nil, common.ErrNotFound
}
func (noYandexAccounts) HasYandexAccount(context.Context, string) (bool, error) { return false, nil }
func (noYandexAccounts) SaveYandexAccount(context.Context, *models.YandexAccount, int64) error {
	return errors.New("not used")
}

func TestRegistrationWithoutYandexReturns428(t *testing.T) {
	t.Setenv("REQUIRE_YANDEX_CONNECT", "true")
	now := time.Now().UnixMilli()
	contests := submissionContestStore{contest: models.Contest{
		ID: "c1", RegistrationStartTime: now - 60_000, RegistrationEndTime: now + 60_000,
		StartTime: now + 120_000, EndTime: now + 180_000, EligibleTo: []int{2},
	}}
	storage := &stores.Storage{Contests: contests, Users: yandexRegistrationUsers{}, YandexAccounts: noYandexAccounts{}}
	controller := NewContestController(services.NewContestService(storage, nil), nil)

	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/contests/c1/registration", nil), rec)
	ctx.SetParamNames("id")
	ctx.SetParamValues("c1")
	ctx.Set(common.AUTH_USER_ID, "candidate-1")
	ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.ModifyRegistrationRequest{Action: dto.RegisterAction})
	if err := controller.ModifyRegistration(ctx); err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusPreconditionRequired || body["code"] != "yandex_not_connected" || body["error"] == "" {
		t.Fatalf("HTTP %d %s", rec.Code, rec.Body.String())
	}
}

func TestYandexErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{common.ErrYandexNotConfigured, http.StatusServiceUnavailable, "yandex_not_configured"},
		{common.ErrYandexConsentRequired, http.StatusBadRequest, "consent_required"},
		{common.ErrYandexInvalidState, http.StatusBadRequest, "invalid_state"},
		{common.ErrYandexInvalidCode, http.StatusBadRequest, "invalid_code"},
		{common.ErrYandexAccountTaken, http.StatusConflict, "yandex_account_taken"},
		{errors.Join(common.ErrYandexUnavailable, errors.New("dial tcp: secret detail")), http.StatusBadGateway, "yandex_unavailable"},
		{errors.New("database down"), http.StatusInternalServerError, "internal_error"},
	} {
		rec := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/yandex/connection/complete", nil), rec)
		if err := yandexError(ctx, tc.err); err != nil {
			t.Fatal(err)
		}
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tc.status || body["code"] != tc.code {
			t.Fatalf("%v: HTTP %d %s", tc.err, rec.Code, rec.Body.String())
		}
		if tc.code == "yandex_unavailable" && body["error"] != common.ErrYandexUnavailable.Error() {
			t.Fatalf("upstream detail leaked to the candidate: %s", body["error"])
		}
	}
}
