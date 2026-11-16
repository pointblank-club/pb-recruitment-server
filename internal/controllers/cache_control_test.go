package controllers

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/services"
	"app/internal/stores"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestPrivateContestResponseCacheControl(t *testing.T) {
	for _, tc := range []struct {
		name       string
		handler    func(*ContestController, echo.Context) error
		user       string
		registered bool
		lookupErr  error
		wantStatus int
	}{
		{"contest guest", (*ContestController).GetContest, "", false, nil, http.StatusOK},
		{"contest participant", (*ContestController).GetContest, "participant", true, nil, http.StatusOK},
		{"contest missing", (*ContestController).GetContest, "", false, common.ContestNotFoundError, http.StatusNotFound},
		{"contest failure", (*ContestController).GetContest, "", false, errors.New("fixture failure"), http.StatusInternalServerError},
		{"problem allowed", (*ContestController).GetContestProblem, "participant", true, nil, http.StatusOK},
		{"problem guest", (*ContestController).GetContestProblem, "", false, nil, http.StatusForbidden},
		{"problem missing", (*ContestController).GetContestProblem, "participant", true, common.ContestNotFoundError, http.StatusNotFound},
		{"session allowed", (*ContestController).GetCandidateSessionStatus, "participant", true, nil, http.StatusOK},
		{"session guest", (*ContestController).GetCandidateSessionStatus, "", false, nil, http.StatusUnauthorized},
		{"session forbidden", (*ContestController).GetCandidateSessionStatus, "participant", false, nil, http.StatusForbidden},
		{"session missing", (*ContestController).GetCandidateSessionStatus, "participant", true, common.ContestNotFoundError, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UnixMilli()
			storage := &stores.Storage{SEB: browserSessionTestStore{},
				Contests: &tabSwitchMockStore{submissionContestStore: submissionContestStore{
					contest:    models.Contest{ID: "contest", StartTime: now - 60000, EndTime: now + 60000},
					registered: tc.registered, err: tc.lookupErr,
				}},
				Problems: visibilityProblemStore{},
			}
			controller := NewContestController(services.NewContestService(storage, nil), nil)
			w := httptest.NewRecorder()
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), w)
			ctx.SetParamNames("id", "problem_id")
			ctx.SetParamValues("contest", "problem")
			if tc.user != "" {
				ctx.Set(common.AUTH_USER_ID, tc.user)
			}
			if err := tc.handler(controller, ctx); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.wantStatus || w.Header().Get(echo.HeaderCacheControl) != "private, no-store" {
				t.Fatalf("HTTP %d, want %d; headers=%v, body=%s", w.Code, tc.wantStatus, w.Header(), w.Body)
			}
		})
	}
}
