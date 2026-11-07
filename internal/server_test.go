package internal

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestCORSExposesResponseHeaders(t *testing.T) {
	headers := map[string]string{
		echo.HeaderRetryAfter:    "5",
		"X-Total-Count":          "1000",
		"X-Truncated":            "true",
		"X-Max-Pairs-Per-Report": "1000",
		"X-Min-Similarity":       "0.20",
	}
	e := NewEchoServer(nil, nil, nil)
	e.POST("/submission/run", func(c echo.Context) error {
		for name, value := range headers {
			c.Response().Header().Set(name, value)
		}
		return c.NoContent(http.StatusTooManyRequests)
	})
	req := httptest.NewRequest(http.MethodPost, "/submission/run", nil)
	req.Header.Set(echo.HeaderOrigin, "http://localhost:5173")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatal("lost rate-limit response", w.Code, w.Header())
	}
	if w.Header().Get(echo.HeaderAccessControlAllowOrigin) != "http://localhost:5173" {
		t.Fatal("frontend origin not allowed", w.Header())
	}
	exposed := strings.Split(w.Header().Get(echo.HeaderAccessControlExposeHeaders), ",")
	for name, value := range headers {
		if w.Header().Get(name) != value || !slices.Contains(exposed, name) {
			t.Errorf("browser cannot read %s: %v", name, w.Header())
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

			e := NewEchoServer(nil, nil, nil)

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

func TestHealthAndMetricsEndpoints(t *testing.T) {
	e := NewEchoServer(nil, nil, nil)

	// Test /health
	reqHealth := httptest.NewRequest(http.MethodGet, "/health", nil)
	recHealth := httptest.NewRecorder()
	e.ServeHTTP(recHealth, reqHealth)
	if recHealth.Code != http.StatusOK {
		t.Fatalf("expected 200 for /health, got %d", recHealth.Code)
	}

	// Test /healthz
	reqHealthz := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recHealthz := httptest.NewRecorder()
	e.ServeHTTP(recHealthz, reqHealthz)
	if recHealthz.Code != http.StatusOK {
		t.Fatalf("expected 200 for /healthz, got %d", recHealthz.Code)
	}

	// Test /metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recMetrics := httptest.NewRecorder()
	e.ServeHTTP(recMetrics, reqMetrics)
	if recMetrics.Code != http.StatusOK {
		t.Fatalf("expected 200 for /metrics, got %d", recMetrics.Code)
	}
	if !strings.Contains(recMetrics.Body.String(), "http_requests_total") {
		t.Errorf("expected /metrics response to contain http_requests_total")
	}
}
