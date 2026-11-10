package middleware

import (
	"app/internal/metrics"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

// PrometheusMetrics returns an Echo middleware that records request throughput
// and latency distributions into Prometheus instruments using normalized route patterns.
func PrometheusMetrics() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			if req.URL.Path == "/metrics" {
				return next(c)
			}

			start := time.Now()
			err := next(c)

			status := c.Response().Status
			if err != nil {
				var he *echo.HTTPError
				if errors.As(err, &he) {
					status = he.Code
				} else {
					status = http.StatusInternalServerError
				}
			}
			if status == 0 {
				status = http.StatusOK
			}

			route := c.Path()
			if route == "" {
				route = "unmatched"
			}

			duration := time.Since(start).Seconds()
			statusStr := strconv.Itoa(status)

			metrics.HttpRequestsTotal.WithLabelValues(req.Method, route, statusStr).Inc()
			metrics.HttpRequestDuration.WithLabelValues(req.Method, route, statusStr).Observe(duration)

			return err
		}
	}
}
