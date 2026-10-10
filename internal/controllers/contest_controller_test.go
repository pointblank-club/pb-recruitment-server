package controllers

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestProblemMutationsAfterStart(t *testing.T) {
	now := time.Now().UnixMilli()
	for _, end := range []int64{now + 60_000, now - 60_000} {
		for _, action := range []string{"update", "delete"} {
			t.Run(action+"/"+time.UnixMilli(end).Format(time.RFC3339), func(t *testing.T) {
				storage := &stores.Storage{Contests: submissionContestStore{contest: models.Contest{StartTime: now - 120_000, EndTime: end}}}
				controller := NewContestController(services.NewContestService(storage, nil))
				recorder := httptest.NewRecorder()
				ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/admin/contest/problem/problem", nil), recorder)
				ctx.SetParamNames("contestid", "problemid")
				ctx.SetParamValues("contest", "problem")
				ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.CreateProblemRequest{Type: models.MCQ, Answer: []int{0}, Options: []string{"a", "b"}})
				handler := controller.HandleUpdateProblem
				if action == "delete" {
					handler = controller.HandleDeleteProblem
				}
				if err := handler(ctx); err != nil {
					t.Fatal(err)
				}
				if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), common.ErrProblemsLocked.Error()) {
					t.Fatalf("HTTP %d %s", recorder.Code, recorder.Body.String())
				}
			})
		}
	}
}

func TestProblemInvalidAnswerResponse(t *testing.T) {
	controller := NewContestController(services.NewContestService(nil, nil))
	for _, action := range []string{"create", "update"} {
		t.Run(action, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/admin/contest/problem", nil), recorder)
			ctx.SetParamNames("contestid", "problemid")
			ctx.SetParamValues("contest", "problem")
			ctx.Set(common.VALIDATED_REQUEST_BODY, &dto.CreateProblemRequest{Type: models.MCQ, Answer: []int{7}, Options: []string{"a", "b"}})
			handler := controller.HandleCreateProblem
			if action == "update" {
				handler = controller.HandleUpdateProblem
			}
			if err := handler(ctx); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("HTTP %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
