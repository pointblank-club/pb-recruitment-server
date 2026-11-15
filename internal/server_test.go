package internal

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

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
	e.GET("/clock-error", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusForbidden, "not open")
	})
	e.GET("/clock-final-headers", func(c echo.Context) error {
		c.Response().Header().Set("X-Server-Time-Ms", "0")
		c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=3600")
		return c.NoContent(http.StatusNoContent)
	})
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/health", http.StatusOK},
		{"/clock-error", http.StatusForbidden},
		{"/missing", http.StatusNotFound},
		{"/clock-final-headers", http.StatusNoContent},
	} {
		t.Run(tc.path, func(t *testing.T) {
			before := time.Now().UnixMilli()
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			stamp, err := strconv.ParseInt(w.Header().Get("X-Server-Time-Ms"), 10, 64)
			if err != nil || stamp < before || stamp > time.Now().UnixMilli() {
				t.Fatalf("invalid server timestamp %q: %v", w.Header().Get("X-Server-Time-Ms"), err)
			}
			if w.Code != tc.status || w.Header().Get(echo.HeaderCacheControl) != "private, no-store" {
				t.Fatalf("status=%d headers=%v", w.Code, w.Header())
			}
		})
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
