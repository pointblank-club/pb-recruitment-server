package internal

import (
	"app/internal/middleware"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

type clockRejectLimiter struct{}

func (clockRejectLimiter) Allow(context.Context, string, int, int) (int, error) { return 1, nil }

func TestCORSExposesResponseHeaders(t *testing.T) {
	headers := map[string]string{
		echo.HeaderRetryAfter:    "5",
		"X-Total-Count":          "1000",
		"X-Total-Pages":          "10",
		"X-Truncated":            "true",
		"X-Max-Pairs-Per-Report": "1000",
		"X-Min-Similarity":       "0.20",
	}
	e := NewEchoServer(nil, nil)
	e.POST("/submission/run", func(c echo.Context) error {
		for name, value := range headers {
			c.Response().Header().Set(name, value)
		}
		return c.NoContent(http.StatusTooManyRequests)
	})
	req := httptest.NewRequest(http.MethodPost, "/submission/run", nil)
	req.Header.Set(echo.HeaderOrigin, "https://recruitment1.pointblank.club")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatal("lost rate-limit response", w.Code, w.Header())
	}
	if w.Header().Get(echo.HeaderAccessControlAllowOrigin) != "https://recruitment1.pointblank.club" {
		t.Fatal("frontend origin not allowed", w.Header())
	}
	exposed := strings.Split(w.Header().Get(echo.HeaderAccessControlExposeHeaders), ",")
	if !slices.Contains(exposed, "X-Server-Time-Ms") {
		t.Fatal("server time is not exposed to browsers", exposed)
	}
	for name, value := range headers {
		if w.Header().Get(name) != value || !slices.Contains(exposed, name) {
			t.Errorf("browser cannot read %s: %v", name, w.Header())
		}
	}
}

func TestServerTimeHeader(t *testing.T) {
	e := NewEchoServer(nil, nil)
	var sampledAfter int64
	e.GET("/clock-error", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusForbidden, "not open")
	})
	e.GET("/clock-final-headers", func(c echo.Context) error {
		c.Response().Header().Set("X-Server-Time-Ms", "0")
		c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=3600")
		return c.NoContent(http.StatusNoContent)
	})
	e.GET("/clock-delayed", func(c echo.Context) error {
		time.Sleep(10 * time.Millisecond)
		sampledAfter = time.Now().UnixMilli()
		return c.NoContent(http.StatusOK)
	})
	e.GET("/clock-panic", func(c echo.Context) error { panic("fixture panic") })
	e.GET("/clock-auth", func(c echo.Context) error { return c.NoContent(http.StatusOK) }, middleware.RequireFirebaseAuth(nil))
	e.GET("/clock-rate", func(c echo.Context) error { return c.NoContent(http.StatusOK) }, middleware.LimitRequests(clockRejectLimiter{}, "fixture", 1, 60))
	e.GET("/clock-admission", func(c echo.Context) error { return c.NoContent(http.StatusOK) }, middleware.LimitRequests(nil, "fixture", 1, 60))
	stream := func(c echo.Context) error {
		time.Sleep(10 * time.Millisecond)
		sampledAfter = time.Now().UnixMilli()
		c.Response().WriteHeader(http.StatusOK)
		c.Response().Flush()
		// Mutations after commitment must not change the headers the client received.
		c.Response().Header().Set("X-Server-Time-Ms", "0")
		c.Response().Header().Set(echo.HeaderCacheControl, "public")
		if c.Path() == "/clock-committed-panic" {
			panic("committed fixture panic")
		}
		return c.String(http.StatusOK, "streamed fixture")
	}
	e.GET("/clock-stream", stream)
	e.GET("/clock-committed-panic", stream)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/health", http.StatusOK},
		{"/clock-error", http.StatusForbidden},
		{"/missing", http.StatusNotFound},
		{"/clock-final-headers", http.StatusNoContent},
		{"/clock-delayed", http.StatusOK},
		{"/clock-panic", http.StatusInternalServerError},
		{"/clock-auth", http.StatusUnauthorized},
		{"/clock-rate", http.StatusTooManyRequests},
		{"/clock-admission", http.StatusServiceUnavailable},
		{"/clock-stream", http.StatusOK},
		{"/clock-committed-panic", http.StatusOK},
	} {
		t.Run(tc.path, func(t *testing.T) {
			before := time.Now().UnixMilli()
			sampledAfter = before
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			headers := w.Result().Header
			stamp, err := strconv.ParseInt(headers.Get("X-Server-Time-Ms"), 10, 64)
			if err != nil || stamp < sampledAfter || stamp > time.Now().UnixMilli() {
				t.Fatalf("invalid committed timestamp %q, want >= %d: %v", headers.Get("X-Server-Time-Ms"), sampledAfter, err)
			}
			if w.Code != tc.status || headers.Get(echo.HeaderCacheControl) != "private, no-store" {
				t.Fatalf("status=%d headers=%v", w.Code, headers)
			}
		})
	}
}

func TestOrdinaryOptionsKeepsRouterMetadata(t *testing.T) {
	e := NewEchoServer(nil, nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodOptions, "/health", nil))
	if w.Code != http.StatusNoContent || w.Result().Header.Get(echo.HeaderAllow) != "OPTIONS, GET" {
		t.Fatalf("OPTIONS status=%d headers=%v", w.Code, w.Result().Header)
	}
}

func TestServerTimeHeaderOnEarlyRejectionAndPreflight(t *testing.T) {
	e := NewEchoServer(nil, nil)
	e.POST("/bounded", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	for _, tc := range []struct {
		method string
		size   int64
		status int
	}{
		{http.MethodPost, 2 << 20, http.StatusRequestEntityTooLarge},
		{http.MethodOptions, 0, http.StatusNoContent},
		{http.MethodOptions, 2 << 20, http.StatusNoContent},
	} {
		req := httptest.NewRequest(tc.method, "/bounded", nil)
		req.ContentLength = tc.size
		req.Header.Set(echo.HeaderOrigin, "http://localhost:5173")
		req.Header.Set(echo.HeaderAccessControlRequestMethod, http.MethodPost)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		if w.Code != tc.status || w.Header().Get("X-Server-Time-Ms") == "" || w.Header().Get(echo.HeaderAccessControlAllowOrigin) != "http://localhost:5173" {
			t.Fatalf("%s status=%d headers=%v", tc.method, w.Code, w.Header())
		}
	}
}

func TestSwaggerGating(t *testing.T) {
	tests := []struct {
		name          string
		stage         string
		enableSwagger string
		wantMounted   bool
	}{
		{
			name:          "dev environment without flag",
			stage:         "dev",
			enableSwagger: "",
			wantMounted:   true,
		},
		{
			name:          "dev environment with flag",
			stage:         "dev",
			enableSwagger: "true",
			wantMounted:   true,
		},
		{
			name:          "production with enable flag",
			stage:         "production",
			enableSwagger: "true",
			wantMounted:   true,
		},
		{
			name:          "production without flag",
			stage:         "production",
			enableSwagger: "",
			wantMounted:   false,
		},
		{
			name:          "production with flag false",
			stage:         "production",
			enableSwagger: "false",
			wantMounted:   false,
		},
		{
			name:          "unset STAGE fails closed",
			stage:         "",
			enableSwagger: "",
			wantMounted:   false,
		},
		{
			name:          "mistyped STAGE fails closed",
			stage:         "staging",
			enableSwagger: "",
			wantMounted:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STAGE", tt.stage)
			t.Setenv("ENABLE_SWAGGER", tt.enableSwagger)

			e := NewEchoServer(nil, nil)

			mounted := false
			for _, route := range e.Routes() {
				if strings.HasPrefix(route.Path, "/swagger") {
					mounted = true
					break
				}
			}

			if mounted != tt.wantMounted {
				t.Errorf("Swagger mounted = %v, want %v (STAGE=%q, ENABLE_SWAGGER=%q)",
					mounted, tt.wantMounted, tt.stage, tt.enableSwagger)
			}
		})
	}
}

func TestSentryDebugEndpointAbsent(t *testing.T) {
	for _, stage := range []string{"dev", "production"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("STAGE", stage)
			t.Setenv("ENABLE_SENTRY_TEST", "true")
			e := NewEchoServer(nil, nil)
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/debug/sentry-test?type=panic", nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("debug endpoint must not be exposed: HTTP %d", w.Code)
			}
		})
	}
}
