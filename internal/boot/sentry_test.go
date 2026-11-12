package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	"net/url"
	"reflect"
	"strings"
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

func TestSentryRedactsReportPathsWithoutDroppingCauses(t *testing.T) {
	capability := "https://provider.test/reports/" + "synthetic-private-id/data/pairs.csv"
	cause := errors.Join(fmt.Errorf("retrieval: %w", &url.Error{Op: "Get", URL: capability, Err: context.DeadlineExceeded}), errors.New("independent failure"))
	breadcrumb := &sentry.Breadcrumb{Message: capability, Data: map[string]interface{}{"nested": []interface{}{capability}}}
	event := &sentry.Event{Message: capability, Breadcrumbs: []*sentry.Breadcrumb{breadcrumb}, Extra: map[string]interface{}{"url": capability}, Contexts: map[string]sentry.Context{"provider": {"url": capability}}, Request: &sentry.Request{URL: capability}, Tags: map[string]string{"url": capability}}
	event.SetException(cause, 20)
	event = scrubSentryRequest(event, nil)
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-private-id") {
		t.Fatal("provider evidence leaked", string(encoded))
	}
	if !strings.Contains(string(encoded), "independent failure") || !strings.Contains(string(encoded), "context deadline exceeded") {
		t.Fatal("lost error causes", string(encoded))
	}
	if breadcrumb.Message != capability || breadcrumb.Data["nested"].([]interface{})[0] != capability {
		t.Fatal("shared breadcrumb was mutated")
	}
	cyclic := map[string]interface{}{}
	cyclic["self"] = cyclic
	if _, err := json.Marshal(scrubReportData(cyclic, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestSentryLogRedactsReportPaths(t *testing.T) {
	previous := sentry.CurrentHub().Client()
	defer sentry.CurrentHub().BindClient(previous)
	t.Setenv("SENTRY_BACKEND_DSN", "http://public@127.0.0.1:1/1")
	if err := InitSentry(); err != nil {
		t.Fatal(err)
	}
	defer sentry.CurrentHub().Client().Close()
	capability := "https://provider.test/reports/" + "synthetic-private-id/data/pairs.csv"
	entry := &sentry.Log{Body: capability, Attributes: map[string]attribute.Value{
		"url": attribute.StringValue(capability), "urls": attribute.StringSliceValue([]string{capability}), "user.id": attribute.StringValue("private-user"),
	}}
	entry = sentry.CurrentHub().Client().Options().BeforeSendLog(entry)
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-private-id") || strings.Contains(string(encoded), "private-user") {
		t.Fatal("log leaked evidence or identity", string(encoded))
	}
}
