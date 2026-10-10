package services

import (
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/stores"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"
)

type recoveryTestStore struct {
	*stores.ExecutionStore
	items []models.Execution
}

func (*recoveryTestStore) StaleTokenlessExecutions(context.Context, int64, int) ([]string, error) {
	return nil, nil
}
func (*recoveryTestStore) TerminalPendingParents(context.Context, int) ([]stores.FinalExecutionResult, error) {
	return nil, nil
}
func (*recoveryTestStore) ProcessFinal(context.Context, stores.FinalExecutionResult) error {
	return nil
}
func (s *recoveryTestStore) PendingWithTokens(_ context.Context, limit int, created int64, id string) ([]models.Execution, error) {
	var out []models.Execution
	for _, item := range s.items {
		if item.CreatedAt > created || item.CreatedAt == created && item.ID > id {
			out = append(out, item)
		}
	}
	return out[:min(limit, len(out))], nil
}

func TestRecoveryCursorSurvivesTimeout(t *testing.T) {
	store := &recoveryTestStore{items: []models.Execution{
		{ID: "a", Judge0Token: "slow", CreatedAt: 1},
		{ID: "b", Judge0Token: "missing", CreatedAt: 1},
		{ID: "c", Judge0Token: "queued", CreatedAt: 2},
	}}
	var mu sync.Mutex
	var requests []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, "/submissions/")
		mu.Lock()
		requests = append(requests, token)
		mu.Unlock()
		if token == "slow" {
			cancel()
			<-r.Context().Done()
			return
		}
		if token == "missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"status":{"id":1}}`))
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	t.Setenv("JUDGE0_AUTH_TOKEN", "")
	ss := NewSubmissionService(&stores.Storage{Executions: store}, nil, judge0.NewClient())
	ss.RecoverJudge0(ctx)
	if ss.recoveryCursorID != "a" {
		t.Fatalf("cursor advanced to %q", ss.recoveryCursorID)
	}
	ss.RecoverJudge0(context.Background())
	mu.Lock()
	got := slices.Clone(requests)
	mu.Unlock()
	if !slices.Equal(got, []string{"slow", "missing", "queued"}) {
		t.Fatalf("requests = %v", got)
	}
	if ss.recoveryCursorID != "c" {
		t.Fatalf("cursor = %q", ss.recoveryCursorID)
	}
	ss.RecoverJudge0(context.Background())
	if ss.recoveryCursorID != "" || ss.recoveryCursorTime != 0 {
		t.Fatal("empty page must reset the cursor")
	}
}

type testLifecycle struct{ hooks []fx.Hook }

func (l *testLifecycle) Append(h fx.Hook) { l.hooks = append(l.hooks, h) }

func TestRecoveryWorkerStops(t *testing.T) {
	lc := &testLifecycle{}
	NewJudge0Recovery(lc, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lc.hooks[0].OnStart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lc.hooks[0].OnStop(ctx); err != nil {
		t.Fatalf("worker failed to stop: %v", err)
	}
}

func TestJudge0StatusAndRuntime(t *testing.T) {
	want := []string{"pending", "pending", "accepted", "wrong_answer", "tle", "failed_to_process", "rte", "rte", "rte", "rte", "rte", "rte", "judge_error", "judge_error"}
	for id, status := range want {
		got, final := mapJudgeStatus(id + 1)
		if got != status || final != (id >= 2) {
			t.Fatalf("status %d = %q/%v", id+1, got, final)
		}
	}
	for _, tc := range []struct {
		raw     string
		want    int64
		invalid bool
	}{
		{`null`, 0, false}, {`"0.125"`, 125, false}, {`0.1256`, 126, false}, {`"0"`, 0, false},
		{`"invalid"`, 0, true}, {`"-1"`, 0, true},
	} {
		got, err := judgeRuntimeMillis(json.RawMessage(tc.raw))
		if (err != nil) != tc.invalid || err == nil && got != tc.want {
			t.Fatalf("runtime %s = %d, %v", tc.raw, got, err)
		}
	}
}
