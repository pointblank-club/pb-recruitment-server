package boot

import (
	"app/internal/common"
	"github.com/getsentry/sentry-go/attribute"
	"log"
	"os"
	"strings"
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
		Dsn:              dsn,
		Environment:      stage,
		Release:          release,
		AttachStacktrace: true,
		EnableTracing:    true,
		TracesSampler:    sampleSentryTrace,
		EnableLogs:       true,
		BeforeSendLog: func(entry *sentry.Log) *sentry.Log {
			entry.Body = common.RedactReportURLs(entry.Body)
			for key, value := range entry.Attributes {
				if value.Type() == attribute.STRING {
					entry.Attributes[key] = attribute.StringValue(common.RedactReportURLs(value.AsString()))
				}
				if value.Type() == attribute.STRINGSLICE {
					values := value.AsStringSlice()
					for i := range values {
						values[i] = common.RedactReportURLs(values[i])
					}
					entry.Attributes[key] = attribute.StringSliceValue(values)
				}
				if strings.HasPrefix(key, "user.") {
					delete(entry.Attributes, key)
				}
			}
			return entry
		},
		BeforeSendMetric: func(metric *sentry.Metric) *sentry.Metric {
			for key := range metric.Attributes {
				if strings.HasPrefix(key, "user.") {
					delete(metric.Attributes, key)
				}
			}
			return metric
		},
		BeforeSend:            scrubSentryRequest,
		BeforeSendTransaction: scrubSentryRequest,
		Debug:                 stage == "dev" || os.Getenv("SENTRY_DEBUG") == "true",
	})
	if err != nil {
		return err
	}

	log.Printf("[Sentry] Initialized errors, traces, logs and request metrics (environment: %s)", stage)
	return nil
}

func scrubSentryRequest(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	event.Message = common.RedactReportURLs(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = common.RedactReportURLs(event.Exception[i].Value)
	}
	event.Breadcrumbs = append([]*sentry.Breadcrumb(nil), event.Breadcrumbs...)
	for i, breadcrumb := range event.Breadcrumbs {
		if breadcrumb == nil {
			continue
		}
		// Scope breadcrumbs may be shared by concurrent events.
		copy := *breadcrumb
		copy.Message = common.RedactReportURLs(copy.Message)
		copy.Data, _ = scrubReportData(copy.Data, 0).(map[string]interface{})
		event.Breadcrumbs[i] = &copy
	}
	event.Extra, _ = scrubReportData(event.Extra, 0).(map[string]interface{})
	for key, value := range event.Contexts {
		event.Contexts[key] = sentry.Context(scrubReportData(map[string]interface{}(value), 0).(map[string]interface{}))
	}
	for key, value := range event.Tags {
		event.Tags[key] = common.RedactReportURLs(value)
	}
	if event.Type == "transaction" {
		event.Request = nil
		event.User = sentry.User{}
		event.Tags = nil
		return event
	}
	if event.Request != nil {
		event.Request = &sentry.Request{URL: common.RedactReportURLs(event.Request.URL), Method: event.Request.Method}
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

func sampleSentryTrace(ctx sentry.SamplingContext) float64 {
	if ctx.Span.Source != sentry.SourceRoute || ctx.Span.Name == "GET /health" || strings.HasPrefix(ctx.Span.Name, "OPTIONS ") {
		return 0
	}
	return 0.05
}

func scrubReportData(value interface{}, depth int) interface{} {
	// Bound arbitrary scope data, including cyclic maps.
	if depth >= 16 {
		return "[redacted]"
	}
	switch value := value.(type) {
	case string:
		return common.RedactReportURLs(value)
	case map[string]interface{}:
		if value == nil {
			return value
		}
		out := make(map[string]interface{}, len(value))
		for key, item := range value {
			out[key] = scrubReportData(item, depth+1)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(value))
		for key, item := range value {
			out[key] = common.RedactReportURLs(item)
		}
		return out
	case []string:
		out := make([]string, len(value))
		for i, item := range value {
			out[i] = common.RedactReportURLs(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(value))
		for i, item := range value {
			out[i] = scrubReportData(item, depth+1)
		}
		return out
	default:
		return value
	}
}
