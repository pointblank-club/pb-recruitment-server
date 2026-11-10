package boot

import (
	"github.com/getsentry/sentry-go"
	"reflect"
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
	previous := sentry.CurrentHub().Client()
	t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })

	err := InitSentry()
	if err != nil {
		t.Fatalf("unexpected error initializing sentry: %v", err)
	}
}

func TestSentryScrubsErrorsAndTransactions(t *testing.T) {
	previous := sentry.CurrentHub().Client()
	t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })
	t.Setenv("SENTRY_BACKEND_DSN", "http://public@127.0.0.1:1/1")
	if err := InitSentry(); err != nil {
		t.Fatal(err)
	}
	options := sentry.CurrentHub().Client().Options()
	for _, scrub := range []func(*sentry.Event, *sentry.EventHint) *sentry.Event{options.BeforeSend, options.BeforeSendTransaction} {
		original := &sentry.Request{URL: "http://example.test/auth/signup", Method: "POST", Data: `{"password":"secret"}`, QueryString: "sig=secret", Cookies: "session=secret", Headers: map[string]string{"X-Api-Key": "secret"}, Env: map[string]string{"REMOTE_ADDR": "192.0.2.1"}}
		event := scrub(&sentry.Event{Request: original}, nil)
		if !reflect.DeepEqual(event.Request, &sentry.Request{URL: original.URL, Method: original.Method}) {
			t.Fatalf("request data was retained: %+v", event.Request)
		}
		if original.Data == "" {
			t.Fatal("scrubber modified shared scope request")
		}
		if scrub(&sentry.Event{}, nil).Request != nil {
			t.Fatal("request added to background event")
		}
	}
}

func TestTraceSamplingAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source sentry.TransactionSource
		rate   float64
	}{
		{"GET /health", sentry.SourceRoute, 0},
		{"OPTIONS /contests", sentry.SourceRoute, 0},
		{"POST /auth/signup", sentry.SourceRoute, 0.05},
		{"GET /unknown-sensitive-path", sentry.SourceURL, 0},
	} {
		if got := sampleSentryTrace(sentry.SamplingContext{Span: &sentry.Span{Name: tc.name, Source: tc.source}}); got != tc.rate {
			t.Fatalf("%s: %v", tc.name, got)
		}
	}
	event := scrubSentryRequest(&sentry.Event{Type: "transaction", Request: &sentry.Request{Data: "secret"}, User: sentry.User{ID: "participant"}, Tags: map[string]string{"id": "participant"}}, nil)
	if event.Request != nil || !event.User.IsEmpty() || len(event.Tags) != 0 {
		t.Fatalf("private trace data: %+v", event)
	}
}
