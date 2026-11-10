package internal

import (
	"app/internal/boot"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/labstack/echo/v4"
)

type capturedSentryEvents struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (*capturedSentryEvents) Configure(sentry.ClientOptions) {}
func (*capturedSentryEvents) Flush(time.Duration) bool       { return true }
func (*capturedSentryEvents) Close()                         {}
func (tr *capturedSentryEvents) SendEvent(event *sentry.Event) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.events = append(tr.events, event)
}

func TestSentryHTTPReportsOnceWithoutRequestSecrets(t *testing.T) {
	previous := sentry.CurrentHub().Client()
	t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })
	t.Setenv("SENTRY_BACKEND_DSN", "http://public@127.0.0.1:1/1")
	t.Setenv("STAGE", "production")
	if err := boot.InitSentry(); err != nil {
		t.Fatal(err)
	}
	options := sentry.CurrentHub().Client().Options()
	if options.EnableTracing || options.TracesSampleRate != 0 {
		t.Fatal("tracing must remain disabled")
	}
	tr := &capturedSentryEvents{}
	options.Transport = tr
	client, err := sentry.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	sentry.CurrentHub().BindClient(client)

	for _, tc := range []struct {
		name            string
		status, reports int
		handler         echo.HandlerFunc
	}{
		{"success", 201, 0, func(c echo.Context) error { return c.NoContent(201) }},
		{"validation", 400, 0, func(c echo.Context) error { return echo.NewHTTPError(400) }},
		{"generic 5xx", 503, 1, func(c echo.Context) error { return c.NoContent(503) }},
		{"returned 5xx", 500, 1, func(c echo.Context) error { return echo.NewHTTPError(500) }},
		{"unexpected error", 500, 1, func(c echo.Context) error { return errors.New("synthetic failure") }},
		{"captured 5xx", 500, 1, func(c echo.Context) error {
			sentryecho.GetHubFromContext(c).CaptureException(errors.New("synthetic store failure"))
			return c.NoContent(500)
		}},
		{"panic", 500, 1, func(c echo.Context) error { panic("synthetic panic") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr.events = nil
			e := NewEchoServer(nil, nil)
			body := `{"password":"synthetic-password","code":"synthetic-solution","option":[2]}`
			e.POST("/synthetic/:id", func(c echo.Context) error {
				data, err := io.ReadAll(c.Request().Body)
				if err != nil || string(data) != body {
					t.Fatalf("request body changed: %q %v", data, err)
				}
				sentryecho.GetHubFromContext(c).ConfigureScope(func(scope *sentry.Scope) { scope.SetUser(sentry.User{ID: "synthetic-uid"}) })
				return tc.handler(c)
			})
			req := httptest.NewRequest(http.MethodPost, "/synthetic/item?sig=synthetic-signature", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer synthetic-token")
			req.Header.Set("Cookie", "session=synthetic-cookie")
			req.Header.Set("X-Api-Key", "synthetic-key")
			w := httptest.NewRecorder()
			e.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d, want %d", w.Code, tc.status)
			}
			if len(tr.events) != tc.reports {
				t.Fatalf("reports=%d, want %d", len(tr.events), tc.reports)
			}
			for _, event := range tr.events {
				if event.Type == "transaction" {
					t.Fatal("unexpected transaction")
				}
				if event.User.ID != "synthetic-uid" {
					t.Fatalf("lost UID: %+v", event.User)
				}
				if event.Request == nil || event.Request.Method != http.MethodPost || event.Request.URL != "http://example.com/synthetic/item" {
					t.Fatalf("lost request metadata: %+v", event.Request)
				}
				encoded, _ := json.Marshal(event.Request)
				for _, secret := range []string{"synthetic-password", "synthetic-solution", "synthetic-signature", "synthetic-token", "synthetic-cookie", "synthetic-key"} {
					if strings.Contains(string(encoded), secret) {
						t.Fatalf("request telemetry contains %s", secret)
					}
				}
			}
		})
	}
}
