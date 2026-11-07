package boot

import (
	"testing"
)

func TestInitSentryWithoutDSN(t *testing.T) {
	t.Setenv("SENTRY_BACKEND_DSN", "")
	t.Setenv("SENTRY_DSN", "")

	err := InitSentry()
	if err != nil {
		t.Fatalf("expected nil error when DSN is empty, got: %v", err)
	}
}

func TestInitSentryWithInvalidDSN(t *testing.T) {
	t.Setenv("SENTRY_BACKEND_DSN", "invalid-dsn")
	t.Setenv("SENTRY_DSN", "")

	err := InitSentry()
	if err == nil {
		t.Fatal("expected error with invalid DSN, got nil")
	}
}

func TestInitSentryWithValidDSN(t *testing.T) {
	t.Setenv("SENTRY_BACKEND_DSN", "https://public@sentry.example.com/1")
	t.Setenv("STAGE", "production")
	t.Setenv("SENTRY_TRACES_SAMPLE_RATE", "0.5")

	err := InitSentry()
	if err != nil {
		t.Fatalf("unexpected error initializing sentry: %v", err)
	}
}
