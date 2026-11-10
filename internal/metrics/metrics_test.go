package metrics

import (
	"testing"
)

func TestMetricsRegistrationAndObservation(t *testing.T) {
	// Ensure metrics can be incremented / observed without panicking
	HttpRequestsTotal.WithLabelValues("GET", "/health", "200").Inc()
	HttpRequestDuration.WithLabelValues("GET", "/health", "200").Observe(0.015)

	SubmissionsTotal.WithLabelValues("code", "python").Inc()
	SubmissionsTotal.WithLabelValues("mcq", "none").Inc()

	InFlightExecutions.Inc()
	InFlightExecutions.Dec()

	Judge0RoundTripDuration.WithLabelValues("submit").Observe(1.2)
	Judge0RoundTripDuration.WithLabelValues("run").Observe(0.8)

	VerdictsTotal.WithLabelValues("accepted", "code").Inc()
	VerdictsTotal.WithLabelValues("wrong_answer", "mcq").Inc()
}
