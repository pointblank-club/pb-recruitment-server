package boot

import (
	"log"
	"os"
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

	release := os.Getenv("RELEASE_VERSION")
	if release == "" {
		release = os.Getenv("APP_VERSION")
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:                   dsn,
		Environment:           stage,
		Release:               release,
		AttachStacktrace:      true,
		BeforeSend:            scrubSentryRequest,
		BeforeSendTransaction: scrubSentryRequest,
		Debug:                 stage == "dev" || os.Getenv("SENTRY_DEBUG") == "true",
	})
	if err != nil {
		return err
	}

	log.Printf("[Sentry] Initialized crash reporting (environment: %s)", stage)
	return nil
}

func scrubSentryRequest(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event.Request != nil {
		event.Request = &sentry.Request{URL: event.Request.URL, Method: event.Request.Method}
	}
	return event
}

// FlushSentry flushes any buffered events to Sentry within the given timeout.
func FlushSentry(timeout time.Duration) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	sentry.Flush(timeout)
}
