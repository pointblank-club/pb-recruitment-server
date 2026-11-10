package services

import (
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/stores"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/getsentry/sentry-go"
)

type workerCaptureTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
	fatal  bool
}

func (*workerCaptureTransport) Configure(sentry.ClientOptions) {}
func (*workerCaptureTransport) Close()                         {}
func (tr *workerCaptureTransport) Flush(time.Duration) bool {
	if tr.fatal {
		fmt.Fprintln(os.Stderr, "telemetry flushed before fatal exit")
	}
	return true
}
func (tr *workerCaptureTransport) SendEvent(event *sentry.Event) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.events = append(tr.events, event)
	if tr.fatal {
		fmt.Fprintln(os.Stderr, "panic captured before fatal exit")
	}
}

type failingClaimStore struct{ recoveryTestStore }

func (*failingClaimStore) ClaimRecovery(context.Context, int, bool) ([]models.Execution, error) {
	return nil, errors.New("synthetic claim failure")
}

func TestWorkerCapturesDoNotChangeGlobalScope(t *testing.T) {
	previous := sentry.CurrentHub().Client()
	t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })
	entered, release := make(chan struct{}, 2), make(chan struct{})
	tr := &workerCaptureTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{DisableMetrics: true,
		Dsn: "http://public@127.0.0.1:1/1", Transport: tr,
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			if event.Message != "global probe" {
				entered <- struct{}{}
				<-release
			}
			return event
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sentry.CurrentHub().BindClient(client)
	service := NewSubmissionService(&stores.Storage{Executions: &failingClaimStore{}}, nil, judge0.NewClient())
	done := make(chan struct{})
	go func() { defer close(done); service.RecoverJudge0(context.Background()) }()
	<-entered
	<-entered
	sentry.CaptureMessage("global probe")
	close(release)
	<-done
	if len(tr.events) != 3 {
		t.Fatalf("events=%d, want 3", len(tr.events))
	}
	for _, event := range tr.events {
		if event.Message == "global probe" {
			if event.Tags["worker"] != "" || event.Tags["action"] != "" {
				t.Fatalf("worker scope leaked: %v", event.Tags)
			}
		} else if event.Tags["worker"] != "judge0_recovery" || event.Tags["action"] != "claim_recovery" {
			t.Fatalf("lost worker tags: %v", event.Tags)
		}
	}
}

func TestWorkerPanicsRemainFatal(t *testing.T) {
	if worker := os.Getenv("SENTRY_TEST_FATAL_WORKER"); worker != "" {
		client, err := sentry.NewClient(sentry.ClientOptions{DisableMetrics: true, Dsn: "http://public@127.0.0.1:1/1", Transport: &workerCaptureTransport{fatal: true}})
		if err != nil {
			t.Fatal(err)
		}
		sentry.CurrentHub().BindClient(client)
		synctest.Test(t, func(t *testing.T) {
			lc := &testLifecycle{}
			if worker == "judge0" {
				NewJudge0Recovery(lc, nil)
			} else {
				NewDolosWorker(lc, nil)
			}
			if err := lc.hooks[0].OnStart(context.Background()); err != nil {
				t.Fatal(err)
			}
			time.Sleep(16 * time.Second)
		})
		t.Fatal("worker panic was swallowed")
	}
	for _, worker := range []string{"judge0", "dolos"} {
		t.Run(worker, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerPanicsRemainFatal$")
			cmd.Env = append(os.Environ(), "SENTRY_TEST_FATAL_WORKER="+worker)
			output, err := cmd.CombinedOutput()
			if err == nil || ctx.Err() != nil {
				t.Fatalf("worker did not exit on panic: %v\n%s", err, output)
			}
			for _, marker := range []string{"panic captured before fatal exit", "telemetry flushed before fatal exit", "panic:"} {
				if !strings.Contains(string(output), marker) {
					t.Fatalf("missing %q in fatal output:\n%s", marker, output)
				}
			}
		})
	}
}

func (tr *workerCaptureTransport) FlushWithContext(context.Context) bool {
	return tr.Flush(time.Second)
}
