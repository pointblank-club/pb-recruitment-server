package middleware

import (
	"app/internal/metrics"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	dto "github.com/prometheus/client_model/go"
)

func TestPrometheusMetricsMiddleware(t *testing.T) {
	e := echo.New()
	e.Use(PrometheusMetrics())

	e.GET("/test/:id", func(c echo.Context) error {
		return c.String(http.StatusOK, "success")
	})

	e.GET("/error", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusBadRequest, "bad request")
	})

	e.GET("/metrics", func(c echo.Context) error {
		return c.String(http.StatusOK, "prom metrics")
	})

	// 1. Test normal route pattern matching
	req := httptest.NewRequest(http.MethodGet, "/test/123", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	metric := &dto.Metric{}
	counter, err := metrics.HttpRequestsTotal.GetMetricWithLabelValues(http.MethodGet, "/test/:id", "200")
	if err != nil {
		t.Fatalf("failed to get metric: %v", err)
	}
	if err := counter.Write(metric); err != nil {
		t.Fatalf("failed to write metric: %v", err)
	}
	if metric.Counter.GetValue() < 1 {
		t.Errorf("expected counter >= 1, got %f", metric.Counter.GetValue())
	}

	// 2. Test HTTP error code tracking
	reqErr := httptest.NewRequest(http.MethodGet, "/error", nil)
	recErr := httptest.NewRecorder()
	e.ServeHTTP(recErr, reqErr)

	if recErr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recErr.Code)
	}

	counterErr, err := metrics.HttpRequestsTotal.GetMetricWithLabelValues(http.MethodGet, "/error", "400")
	if err != nil {
		t.Fatalf("failed to get error metric: %v", err)
	}
	metricErr := &dto.Metric{}
	if err := counterErr.Write(metricErr); err != nil {
		t.Fatalf("failed to write metric: %v", err)
	}
	if metricErr.Counter.GetValue() < 1 {
		t.Errorf("expected counter >= 1, got %f", metricErr.Counter.GetValue())
	}

	// 3. Test /metrics is excluded from self-observation
	reqProm := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recProm := httptest.NewRecorder()
	e.ServeHTTP(recProm, reqProm)

	counterProm, err := metrics.HttpRequestsTotal.GetMetricWithLabelValues(http.MethodGet, "/metrics", "200")
	if err == nil {
		metricProm := &dto.Metric{}
		_ = counterProm.Write(metricProm)
		if metricProm.Counter.GetValue() != 0 {
			t.Errorf("expected /metrics counter to be 0, got %f", metricProm.Counter.GetValue())
		}
	}
}
