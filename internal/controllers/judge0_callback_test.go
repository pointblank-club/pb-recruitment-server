package controllers

import (
	"app/internal/judge0"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type callbackTestStore struct {
	*stores.ExecutionStore
	called bool
}

func (s *callbackTestStore) ProcessFinal(context.Context, stores.FinalExecutionResult) error {
	s.called = true
	return nil
}

func TestJudge0CallbackValidation(t *testing.T) {
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "https://example.test")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "test-secret")
	client := judge0.NewClient()
	id := uuid.NewString()
	body := `{"token":"test-token","time":"0.1","memory":1024,"status":{"id":3}}`
	for _, tc := range []struct {
		name, id, sig, body string
		want                int
	}{
		{"missing signature", id, "", body, 401},
		{"wrong signature", id, "00", body, 401},
		{"invalid execution", "invalid", "", body, 400},
		{"malformed body", id, client.CallbackSignature(id), "{", 400},
		{"missing token", id, client.CallbackSignature(id), `{"status":{"id":3}}`, 400},
		{"invalid status", id, client.CallbackSignature(id), `{"token":"test","status":{"id":15}}`, 400},
		{"valid", id, client.CallbackSignature(id), body, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &callbackTestStore{}
			controller := NewSubmissionController(services.NewSubmissionService(&stores.Storage{Executions: store}, nil, client), nil, client)
			w := httptest.NewRecorder()
			ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/internal/judge0/callback/"+tc.id+"?sig="+tc.sig, strings.NewReader(tc.body)), w)
			ctx.SetParamNames("execution_id")
			ctx.SetParamValues(tc.id)
			if err := controller.Judge0Callback(ctx); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.want || store.called != (tc.want == 200) {
				t.Fatalf("HTTP %d, store called=%v", w.Code, store.called)
			}
		})
	}
	t.Setenv("JUDGE0_CALLBACK_SECRET", "")
	disabled := judge0.NewClient()
	controller := NewSubmissionController(nil, nil, disabled)
	w := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/?sig="+client.CallbackSignature(id), strings.NewReader(body)), w)
	ctx.SetParamNames("execution_id")
	ctx.SetParamValues(id)
	if err := controller.Judge0Callback(ctx); err != nil || w.Code != 401 {
		t.Fatalf("disabled callbacks: HTTP %d, %v", w.Code, err)
	}
}
