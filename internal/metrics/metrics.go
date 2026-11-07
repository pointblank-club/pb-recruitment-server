package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// HttpRequestsTotal counts incoming HTTP requests.
	// LABELS:
	// - method: "GET", "POST", etc.
	// - route: Normalized route pattern (e.g. "/submission/:id/status", NOT concrete IDs)
	// - status: "200", "400", "500", etc.
	HttpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests processed by pb-recruitment-server",
		},
		[]string{"method", "route", "status"},
	)

	// HttpRequestDuration measures latency distributions.
	// Buckets range from 25ms up to 30s to account for long-running submit operations.
	HttpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request execution latency in seconds",
			Buckets: []float64{0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30},
		},
		[]string{"method", "route", "status"},
	)

	// SubmissionsTotal counts solutions submitted by candidates.
	// LABELS:
	// - type: "code" | "mcq"
	// - language: "python", "cpp", "java", "go", etc.
	SubmissionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pb_submissions_total",
			Help: "Total submissions received across all contests",
		},
		[]string{"type", "language"},
	)

	// InFlightExecutions tracks currently active judge jobs.
	InFlightExecutions = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "pb_judge_in_flight_submissions",
			Help: "Current count of testcase executions waiting for or executing in Judge0",
		},
	)

	// Judge0RoundTripDuration measures time between backend dispatch and verdict receipt.
	Judge0RoundTripDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "pb_judge0_roundtrip_duration_seconds",
			Help:    "Execution roundtrip latency through Judge0 workers",
			Buckets: []float64{0.5, 1, 2, 4, 8, 15, 25, 40},
		},
		[]string{"action"}, // "run" vs "submit"
	)

	// VerdictsTotal tracks execution outcomes.
	// LABELS:
	// - outcome: "accepted", "wrong_answer", "tle", "mle", "rte", "judge_error", etc.
	// - type: "code" | "mcq"
	VerdictsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pb_submission_verdicts_total",
			Help: "Submission verdicts graduated by result category",
		},
		[]string{"outcome", "type"},
	)
)
