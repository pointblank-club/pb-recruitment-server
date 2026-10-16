package services

import (
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/stores"
	"context"
	"encoding/json"
	"fmt"
	"go.uber.org/fx"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type recoveryTestStore struct {
	*stores.ExecutionStore
	items   []models.Execution
	finals  []stores.FinalExecutionResult
	healthy []bool
	tokens  map[string]string
}

func (*recoveryTestStore) TerminalPendingParents(context.Context, int) ([]stores.FinalExecutionResult, error) {
	return nil, nil
}
func (s *recoveryTestStore) ClaimRecovery(context.Context, int) ([]models.Execution, error) {
	if len(s.items) == 0 {
		return nil, nil
	}
	e := s.items[0]
	s.items = s.items[1:]
	return []models.Execution{e}, nil
}
func (s *recoveryTestStore) ReleaseRecovery(_ context.Context, _ models.Execution, healthy bool) error {
	s.healthy = append(s.healthy, healthy)
	return nil
}
func (s *recoveryTestStore) ProcessFinal(_ context.Context, r stores.FinalExecutionResult) error {
	s.finals = append(s.finals, r)
	return nil
}
func (s *recoveryTestStore) SaveAttemptToken(_ context.Context, id, attempt, token string) error {
	if s.tokens == nil {
		s.tokens = map[string]string{}
	}
	s.tokens[id] = token
	return nil
}

func TestRecoveryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		failures         int
		healthy          bool
	}{
		{"queued", `{"token":"job","status":{"id":1}}`, "", 3, true},
		{"processing", `{"token":"job","status":{"id":2}}`, "", 3, true},
		{"accepted", `{"token":"job","status":{"id":3},"time":"0.125","memory":256}`, "accepted", 1, true},
		{"wrong answer", `{"token":"job","status":{"id":4}}`, "wrong_answer", 1, true},
		{"timeout", `{"token":"job","status":{"id":5}}`, "tle", 1, true},
		{"compilation", `{"token":"job","status":{"id":6}}`, "failed_to_process", 1, true},
		{"missing retry", "null", "", 1, false},
		{"missing exhausted", "null", "judge_error", 3, false},
		{"outage retry", "outage", "", 1, false},
		{"outage exhausted", "outage", "judge_error", 3, false},
		{"internal error retry", `{"token":"job","status":{"id":13}}`, "", 1, false},
		{"internal error exhausted", `{"token":"job","status":{"id":13}}`, "judge_error", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/workers" {
					t.Error("recovery must not consult worker statistics")
				}
				if tc.body == "outage" {
					w.WriteHeader(502)
					return
				}
				fmt.Fprintf(w, `{"submissions":[%s]}`, tc.body)
			}))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			store := &recoveryTestStore{items: []models.Execution{{ID: "exec", Judge0Token: "job", Attempt: "attempt", Claim: "claim", RecoveryFailures: tc.failures}}}
			ss := NewSubmissionService(&stores.Storage{Executions: store}, nil, judge0.NewClient())
			ss.RecoverJudge0(context.Background())
			if len(store.healthy) != 1 || store.healthy[0] != tc.healthy {
				t.Fatalf("health=%v", store.healthy)
			}
			if tc.want == "" {
				if len(store.finals) != 0 {
					t.Fatal(store.finals)
				}
			} else if len(store.finals) != 1 || store.finals[0].Status != tc.want || store.finals[0].Attempt != "attempt" || store.finals[0].Claim != "claim" {
				t.Fatalf("final=%+v", store.finals)
			}
		})
	}
}
func TestTokenlessRecoveryDispatch(t *testing.T) {
	for _, count := range []int{2, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(201)
				w.Write([]byte(`[{"token":"recovered"}]`))
			}))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			payload, _ := json.Marshal(judge0.SubmissionRequest{SourceCode: "print(1)", LanguageID: 71})
			store := &recoveryTestStore{items: []models.Execution{{ID: "exec", Attempt: "new", Claim: "claim", DispatchCount: count, DispatchPayload: payload}}}
			NewSubmissionService(&stores.Storage{Executions: store}, nil, judge0.NewClient()).RecoverJudge0(context.Background())
			if count == 2 {
				if calls != 1 || store.tokens["exec"] != "recovered" || len(store.finals) != 0 {
					t.Fatalf("calls=%d tokens=%v finals=%v", calls, store.tokens, store.finals)
				}
			} else if calls != 0 || len(store.finals) != 1 || store.finals[0].Status != "judge_error" {
				t.Fatalf("calls=%d finals=%v", calls, store.finals)
			}
		})
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
