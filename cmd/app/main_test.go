package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"go.uber.org/fx"
)

type flushRecorder struct {
	mu         sync.Mutex
	operations []string
}

func (*flushRecorder) Configure(sentry.ClientOptions) {}
func (*flushRecorder) Close()                         {}
func (tr *flushRecorder) SendEvent(*sentry.Event) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.operations = append(tr.operations, "capture")
}
func (tr *flushRecorder) Flush(time.Duration) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.operations = append(tr.operations, "flush")
	return true
}

func TestRunFlushesAfterLifecycleFailures(t *testing.T) {
	for _, scenario := range []string{"success", "start error", "stop error", "stop timeout"} {
		t.Run(scenario, func(t *testing.T) {
			previous := sentry.CurrentHub().Client()
			t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })
			tr := &flushRecorder{}
			client, err := sentry.NewClient(sentry.ClientOptions{Dsn: "http://public@127.0.0.1:1/1", Transport: tr})
			if err != nil {
				t.Fatal(err)
			}
			sentry.CurrentHub().BindClient(client)
			app := fx.New(fx.NopLogger, fx.StopTimeout(20*time.Millisecond), fx.Invoke(func(lc fx.Lifecycle, shutdown fx.Shutdowner) {
				lc.Append(fx.Hook{
					OnStart: func(context.Context) error {
						if scenario == "start error" {
							sentry.CaptureMessage("synthetic startup failure")
							return errors.New("synthetic startup failure")
						}
						return shutdown.Shutdown()
					},
					OnStop: func(ctx context.Context) error {
						sentry.CaptureMessage("synthetic shutdown event")
						if scenario == "stop timeout" {
							<-ctx.Done()
							return ctx.Err()
						}
						if scenario == "stop error" {
							return errors.New("synthetic shutdown failure")
						}
						return nil
					},
				})
			}))
			wantCode := 1
			if scenario == "success" {
				wantCode = 0
			}
			if code := run(app); code != wantCode {
				t.Fatalf("exit code=%d, want %d", code, wantCode)
			}
			tr.mu.Lock()
			defer tr.mu.Unlock()
			if len(tr.operations) != 2 || tr.operations[0] != "capture" || tr.operations[1] != "flush" {
				t.Fatalf("flush order: %v", tr.operations)
			}
		})
	}
}
