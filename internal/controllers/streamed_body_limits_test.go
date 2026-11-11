package controllers

import (
	"app/internal/common"
	"app/internal/judge0"
	"app/internal/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamedBodyLimitsPreserve413(t *testing.T) {
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "http://callback.test")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "test-only-secret")
	client := judge0.NewClient()
	sc := NewSubmissionController(nil, nil, client)
	cc := NewContestController(nil, nil)
	cases := []struct {
		name, path, method, body string
		handler                  echo.HandlerFunc
	}{
		{"run", "/submission/run", "POST", `{"code":"` + strings.Repeat("x", 2<<20) + `"}`, sc.Run},
		{"admin", "/admin/c/p", "PUT", `{"ignored":"` + strings.Repeat("x", 9<<20) + `"}`, cc.HandleUpdateLeaderboardUser},
		{"callback", "/internal/judge0/callback/" + uuid.NewString(), "PUT", `{"stdout":"` + strings.Repeat("x", 13<<20) + `"}`, sc.Judge0Callback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			middleware.ConfigureRequestBounds(e)
			path := tc.path
			target := path
			if tc.name == "callback" {
				id := strings.TrimPrefix(path, "/internal/judge0/callback/")
				path = "/internal/judge0/callback/:execution_id"
				target += "?sig=" + client.CallbackSignature(id)
			}
			if tc.name == "admin" {
				path = "/admin/:contestid/:userid"
			}
			e.Add(tc.method, path, tc.handler, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error { c.Set(common.AUTH_USER_ID, "u"); return next(c) }
			})
			r := httptest.NewRequest(tc.method, target, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.ContentLength = -1
			w := httptest.NewRecorder()
			e.ServeHTTP(w, r)
			if w.Code != 413 {
				t.Fatalf("streamed oversized %s returns %d, want413", tc.name, w.Code)
			}
		})
	}
}
