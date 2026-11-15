package routes

import (
	"app/internal/middleware"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestAdminRoutesNoStoreBeforeAuthentication(t *testing.T) {
	// Mount without the application's global middleware so it cannot mask missing group guards.
	e := echo.New()
	AddAdminRoutes(e, nil, nil, nil, nil, nil, allowRequests{})
	for _, route := range e.Routes() {
		if !strings.HasPrefix(route.Path, "/admin") || route.Method == echo.RouteNotFound {
			continue
		}
		segments := strings.Split(route.Path, "/")
		for i, segment := range segments {
			if strings.HasPrefix(segment, ":") {
				segments[i] = "fixture"
			}
		}
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest(route.Method, strings.Join(segments, "/"), nil))
			if w.Code != http.StatusUnauthorized || w.Header().Get(echo.HeaderCacheControl) != "private, no-store" {
				t.Fatalf("HTTP %d, headers=%v, body=%s", w.Code, w.Header(), w.Body)
			}
		})
	}
}

func TestPrivateNoStoreSuccessAndErrors(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusInternalServerError} {
		e := echo.New()
		admin := e.Group("/admin", middleware.PrivateNoStore)
		admin.GET("/fixture", func(c echo.Context) error {
			if status != http.StatusOK {
				return echo.NewHTTPError(status, "fixture denied")
			}
			return c.String(status, "private fixture")
		})
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/fixture", nil))
		if w.Code != status || w.Header().Get(echo.HeaderCacheControl) != "private, no-store" {
			t.Fatalf("HTTP %d, want %d; headers=%v", w.Code, status, w.Header())
		}
	}
}
