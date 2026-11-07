package boot

import (
	"log"
	"os"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
)

// InitSentry initializes the Sentry Go SDK using SENTRY_BACKEND_DSN or SENTRY_DSN.
// If no DSN is provided, Sentry remains disabled gracefully without blocking server startup.
func InitSentry() error {
	dsn := os.Getenv("SENTRY_BACKEND_DSN")
	if dsn == "" {
		dsn = os.Getenv("SENTRY_DSN")
	}

	if dsn == "" {
		log.Println("[Sentry] No SENTRY_BACKEND_DSN or SENTRY_DSN configured; Sentry monitoring is disabled.")
		return nil
	}

	stage := os.Getenv("STAGE")
	if stage == "" {
		stage = "development"
	}

	tracesSampleRate := 1.0
	if stage == "production" {
		tracesSampleRate = 0.2
	}
	if rateStr := os.Getenv("SENTRY_TRACES_SAMPLE_RATE"); rateStr != "" {
		if rate, err := strconv.ParseFloat(rateStr, 64); err == nil && rate >= 0 && rate <= 1.0 {
			tracesSampleRate = rate
		}
	}

	release := os.Getenv("RELEASE_VERSION")
	if release == "" {
		release = os.Getenv("APP_VERSION")
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      stage,
		Release:          release,
		AttachStacktrace: true,
		EnableTracing:    true,
		TracesSampleRate: tracesSampleRate,
		Debug:            stage == "dev" || os.Getenv("SENTRY_DEBUG") == "true",
	})
	if err != nil {
		return err
	}

	log.Printf("[Sentry] Initialized successfully (environment: %s, sample rate: %.2f)", stage, tracesSampleRate)
	return nil
}

// FlushSentry flushes any buffered events to Sentry within the given timeout.
func FlushSentry(timeout time.Duration) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	sentry.Flush(timeout)
}
