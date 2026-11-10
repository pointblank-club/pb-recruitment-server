package internal

import (
	"app/internal/boot"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	if !options.EnableTracing || !options.EnableLogs {
		t.Fatal("traces and logs must be enabled")
	}
	options.EnableTracing = false
	options.EnableLogs = false
	options.DisableMetrics = true
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

func (tr *capturedSentryEvents) FlushWithContext(context.Context) bool { return tr.Flush(time.Second) }

func TestSentryTraceAndLogEnvelopes(t *testing.T) {
	var mu sync.Mutex
	var payloads []string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		payloads = append(payloads, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	previous := sentry.CurrentHub().Client()
	t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })
	t.Setenv("SENTRY_BACKEND_DSN", strings.Replace(collector.URL, "://", "://public@", 1)+"/1")
	t.Setenv("STAGE", "production")
	if err := boot.InitSentry(); err != nil {
		t.Fatal(err)
	}
	options := sentry.CurrentHub().Client().Options()
	sampler := options.TracesSampler
	options.TracesSampler = func(ctx sentry.SamplingContext) float64 {
		if sampler(ctx) > 0 {
			return 1
		}
		return 0
	}
	client, err := sentry.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sentry.CurrentHub().BindClient(client)
	e := NewEchoServer(nil, nil)
	e.POST("/synthetic/:id", func(c echo.Context) error {
		sentryecho.GetHubFromContext(c).Scope().SetUser(sentry.User{ID: "synthetic-user", Email: "synthetic-email"})
		return c.NoContent(201)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/synthetic/synthetic-private-id?sig=synthetic-sig", strings.NewReader(`{"password":"synthetic-password","code":"synthetic-code"}`))
	req.Header.Set("Authorization", "Bearer synthetic-token")
	e.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("response: %d", w.Code)
	}
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/health", nil))
	if !client.Flush(3 * time.Second) {
		t.Fatal("telemetry did not flush")
	}
	mu.Lock()
	defer mu.Unlock()
	combined := strings.Join(payloads, "\n")
	for _, secret := range []string{"synthetic-password", "synthetic-code", "synthetic-token", "synthetic-sig", "synthetic-private-id", "synthetic-user", "synthetic-email"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("envelope leaked %s", secret)
		}
	}
	var transaction map[string]interface{}
	var logEntry map[string]interface{}
	var metricEntry map[string]interface{}
	for _, payload := range payloads {
		lines := strings.Split(strings.TrimSpace(payload), "\n")
		if len(lines) < 3 {
			t.Fatalf("invalid envelope: %s", payload)
		}
		var item map[string]interface{}
		if err := json.Unmarshal([]byte(lines[2]), &item); err != nil {
			t.Fatal(err)
		}
		if item["type"] == "transaction" {
			transaction = item
		}
		if entries, ok := item["items"].([]interface{}); ok && len(entries) > 0 {
			entry, _ := entries[0].(map[string]interface{})
			if entry["body"] != nil {
				logEntry = entry
			}
			if entry["name"] == "http.server.duration" {
				metricEntry = entry
			}
		}
	}
	if transaction == nil || logEntry == nil || metricEntry == nil {
		t.Fatalf("missing transaction, log or metric: %s", combined)
	}
	trace := transaction["contexts"].(map[string]interface{})["trace"].(map[string]interface{})["trace_id"]
	if metricEntry["unit"] != "millisecond" || metricEntry["value"].(float64) <= 0 {
		t.Fatalf("invalid duration: %+v", metricEntry)
	}
	if trace != metricEntry["trace_id"] {
		t.Fatal("metric not correlated with trace")
	}
	if trace != logEntry["trace_id"] {
		t.Fatalf("log not correlated with trace: %s", combined)
	}
	if strings.Contains(combined, "GET /health") || strings.Contains(combined, `"http.route":{"type":"string","value":"/health"}`) {
		t.Fatal("health telemetry was sent")
	}
}

func TestSentryLogsUnsampledServerFailures(t *testing.T) {
	previous := sentry.CurrentHub().Client()
	defer sentry.CurrentHub().BindClient(previous)
	var logs []*sentry.Log
	tr := &capturedSentryEvents{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: "http://public@127.0.0.1:1/1", Transport: tr,
		EnableLogs: true, DisableMetrics: true,
		BeforeSendLog: func(entry *sentry.Log) *sentry.Log { logs = append(logs, entry); return entry },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sentry.CurrentHub().BindClient(client)
	e := NewEchoServer(nil, nil)
	for _, status := range []int{201, 400, 503} {
		route := "/synthetic/" + strconv.Itoa(status)
		e.GET(route, func(c echo.Context) error { return c.NoContent(status) })
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != status {
			t.Fatalf("status changed: %d", w.Code)
		}
	}
	e.GET("/synthetic/returned", func(c echo.Context) error { return echo.NewHTTPError(500, "synthetic-sensitive-error") })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/synthetic/returned", nil))
	if len(logs) != 2 {
		t.Fatalf("logs: %d, expected two 5xx logs", len(logs))
	}
	for _, entry := range logs {
		if entry.Level != sentry.LogLevelError || entry.Body != "HTTP request completed" {
			t.Fatalf("unexpected log: %+v", entry)
		}
	}
}

func TestSentryMetricsCoverUnsampledRequests(t *testing.T) {
	previous := sentry.CurrentHub().Client()
	defer sentry.CurrentHub().BindClient(previous)
	var metrics []*sentry.Metric
	tr := &capturedSentryEvents{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: "http://public@127.0.0.1:1/1", Transport: tr,
		BeforeSendMetric: func(metric *sentry.Metric) *sentry.Metric { metrics = append(metrics, metric); return metric },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sentry.CurrentHub().BindClient(client)
	for _, tc := range []struct {
		name, method, path, route string
		status                    int
		handler                   echo.HandlerFunc
		wantMetric                bool
	}{
		{"success", "POST", "/synthetic/private-id?token=secret", "/synthetic/:id", 201, func(c echo.Context) error { return c.NoContent(201) }, true},
		{"validation", "POST", "/synthetic/private-id", "/synthetic/:id", 400, func(c echo.Context) error { return echo.NewHTTPError(400) }, true},
		{"5xx", "POST", "/synthetic/private-id", "/synthetic/:id", 503, func(c echo.Context) error { return c.NoContent(503) }, true},
		{"returned error", "POST", "/synthetic/private-id", "/synthetic/:id", 500, func(c echo.Context) error { return errors.New("synthetic failure") }, true},
		{"panic", "POST", "/synthetic/private-id", "/synthetic/:id", 500, func(c echo.Context) error { panic("synthetic failure") }, true},
		{"unmatched", "GET", "/private-path?token=secret", "unmatched", 404, nil, true},
		{"health", "GET", "/health", "/health", 200, nil, false},
		{"preflight", "OPTIONS", "/synthetic/private-id", "/synthetic/:id", 204, func(c echo.Context) error { return c.NoContent(204) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics = nil
			e := NewEchoServer(nil, nil)
			if tc.handler != nil {
				e.Add(tc.method, tc.route, tc.handler)
			}
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("response changed: %d", w.Code)
			}
			want := 0
			if tc.wantMetric {
				want = 1
			}
			if len(metrics) != want {
				t.Fatalf("metrics=%d, want %d", len(metrics), want)
			}
			if want == 0 {
				return
			}
			m := metrics[0]
			if m.Name != "http.server.duration" || m.Unit != "millisecond" || m.Attributes["http.route"].AsInterface() != tc.route || m.Attributes["http.request.method"].AsInterface() != tc.method || m.Attributes["http.response.status_code"].AsInterface() != int64(tc.status) {
				t.Fatalf("wrong metric: %+v", m)
			}
			value, ok := m.Value.Float64()
			if !ok || value <= 0 {
				t.Fatalf("invalid duration: %v", value)
			}
		})
	}
}

func TestSentryConcurrentRequests(t *testing.T) {
	transport := &capturedSentryEvents{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: "http://public@127.0.0.1:1/1", Transport: transport,
		EnableLogs: true, EnableTracing: true, TracesSampleRate: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := sentry.CurrentHub().Client()
	t.Cleanup(func() { client.Close(); sentry.CurrentHub().BindClient(previous) })
	sentry.CurrentHub().BindClient(client)
	e := NewEchoServer(nil, nil)
	e.Logger.SetOutput(io.Discard)
	e.GET("/synthetic/:id", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 600 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response := httptest.NewRecorder()
			e.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/synthetic/item", nil))
			if response.Code != http.StatusOK {
				t.Errorf("response changed: %d", response.Code)
			}
		}()
	}
	close(start)
	wg.Wait()
	if !client.Flush(3 * time.Second) {
		t.Fatal("telemetry did not flush")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	var traces, logs, metrics int
	for _, event := range transport.events {
		if event.Type == "transaction" {
			traces++
		}
		logs += len(event.Logs)
		metrics += len(event.Metrics)
	}
	if traces == 0 || logs == 0 || metrics == 0 {
		t.Fatalf("missing telemetry: traces=%d logs=%d metrics=%d", traces, logs, metrics)
	}
}
