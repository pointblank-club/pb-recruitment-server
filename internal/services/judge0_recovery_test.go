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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recoveryTestStore struct {
	*stores.ExecutionStore
	mu           sync.Mutex
	items        []models.Execution
	finals       []stores.FinalExecutionResult
	healthy      []bool
	tokens       map[string]string
	saveToken    func(context.Context, string, string, string) error
	processFinal func(context.Context, stores.FinalExecutionResult) error
}

func (*recoveryTestStore) TerminalPendingParents(context.Context, int) ([]stores.FinalExecutionResult, error) {
	return nil, nil
}
func (s *recoveryTestStore) ClaimRecovery(_ context.Context, limit int, withToken bool) ([]models.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var claimed, remaining []models.Execution
	for _, e := range s.items {
		if (e.Judge0Token != "") == withToken && len(claimed) < limit {
			claimed = append(claimed, e)
		} else {
			remaining = append(remaining, e)
		}
	}
	s.items = remaining
	return claimed, nil
}

func (s *recoveryTestStore) ReleaseRecovery(_ context.Context, _ []models.Execution, healthy []bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthy = append(s.healthy, healthy...)
	return nil
}
func (s *recoveryTestStore) ProcessFinal(ctx context.Context, r stores.FinalExecutionResult) error {
	if s.processFinal != nil {
		return s.processFinal(ctx, r)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finals = append(s.finals, r)
	return nil
}
func (s *recoveryTestStore) SaveAttemptToken(ctx context.Context, id, attempt, token string) error {
	if s.saveToken != nil {
		return s.saveToken(ctx, id, attempt, token)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
			payload := []byte(`{"source_code":"pass","expected_output":"required"}`)
			store := &recoveryTestStore{items: []models.Execution{{ID: "exec", Judge0Token: "job", Attempt: "attempt", Claim: "claim", RecoveryFailures: tc.failures, DispatchPayload: payload}}}
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

func TestRecoveryPollsInBatches(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens := strings.Split(r.URL.Query().Get("tokens"), ",")
		if len(tokens) > 20 {
			t.Error("batch exceeds Judge0 limit")
		}
		requests.Add(1)
		results := make([]judge0.SubmissionStatusResponse, len(tokens))
		for i, token := range tokens {
			results[i] = judge0.SubmissionStatusResponse{Token: token, Status: judge0.StatusResponse{ID: 3}}
		}
		json.NewEncoder(w).Encode(map[string]any{"submissions": results})
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	t.Setenv("JUDGE0_BATCH_SIZE", "20")
	store := &recoveryTestStore{}
	for i := range 121 {
		id := fmt.Sprint(i)
		store.items = append(store.items, models.Execution{ID: id, Judge0Token: id, Attempt: "attempt", Claim: "claim"})
	}
	NewSubmissionService(&stores.Storage{Executions: store}, nil, judge0.NewClient()).RecoverJudge0(context.Background())
	if len(store.finals) != 121 || requests.Load() != 7 {
		t.Fatalf("finals=%d requests=%d, want121 finals in7 requests", len(store.finals), requests.Load())
	}
	for _, r := range store.finals {
		if r.ExecutionID != r.Token || r.Attempt != "attempt" || r.Claim != "claim" {
			t.Fatal("result mapping/fence lost", r)
		}
	}
}

func TestRecoveryPersistsKnownTokenAfterCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte(`[{"token":"known"}]`))
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	saved := false
	store := &recoveryTestStore{saveToken: func(persistCtx context.Context, id, attempt, token string) error {
		cancel() // Shutdown starts just as the successful response reaches persistence.
		if err := persistCtx.Err(); err != nil {
			return err
		}
		if deadline, ok := persistCtx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
			t.Error("token persistence must have its own bounded context")
		}
		saved = id == "exec" && attempt == "attempt" && token == "known"
		return nil
	}}
	healthy, err := NewSubmissionService(&stores.Storage{Executions: store}, nil, judge0.NewClient()).dispatchRecovery(ctx, models.Execution{ID: "exec", Attempt: "attempt"}, judge0.SubmissionRequest{SourceCode: "pass", LanguageID: 71})
	if err != nil || !healthy || !saved {
		t.Fatalf("known token lost: saved=%v healthy=%v error=%v", saved, healthy, err)
	}
}

func TestRecoveryCancellationPreservesHealthyObservations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"submissions":[{"token":"a","status":{"id":3}},{"token":"b","status":{"id":2}}]}`))
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &recoveryTestStore{
		items: []models.Execution{{ID: "a", Judge0Token: "a"}, {ID: "b", Judge0Token: "b"}},
		processFinal: func(ctx context.Context, _ stores.FinalExecutionResult) error {
			cancel()
			return ctx.Err()
		},
	}
	NewSubmissionService(&stores.Storage{Executions: store}, nil, judge0.NewClient()).RecoverJudge0(ctx)
	if len(store.healthy) != 2 || !store.healthy[0] || !store.healthy[1] {
		t.Fatal("shutdown counted valid observations as failures", store.healthy)
	}
}

func TestRecoveryFetchesStdoutOnlyForBlankWrongAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, expected, stdout      string
		status, failures, wantCalls int
		wantFinal, wantHealthy      bool
	}{
		{"accepted large output", "", `"bGFyZ2U="`, 3, 1, 1, true, true},
		{"nonblank expected", "required", `null`, 4, 1, 1, true, true},
		{"significant Unicode whitespace", "\u00a0", `null`, 4, 1, 1, true, true},
		{"verified null output", " \t\n\x00", `null`, 4, 1, 2, true, true},
		{"actual wrong output", "", `"d3Jvbmc="`, 4, 1, 2, true, true},
		{"missing stdout is not evidence", "", "", 4, 1, 2, false, false},
		{"evidence fetch unavailable", "", "outage", 4, 1, 2, false, false},
		{"evidence fetch exhausted", "", "outage", 4, 3, 2, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				out := map[string]any{"token": "job", "status": map[string]int{"id": tc.status}, "stderr": "d2FybmluZw=="}
				if strings.Contains(r.URL.Query().Get("fields"), "stdout") {
					if tc.stdout == "outage" {
						w.WriteHeader(503)
						return
					}
					if tc.stdout != "" {
						out["stdout"] = json.RawMessage(tc.stdout)
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"submissions": []any{out}})
			}))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			payload, _ := json.Marshal(judge0.SubmissionRequest{SourceCode: "pass", ExpectedOutput: &tc.expected})
			store := &recoveryTestStore{items: []models.Execution{{ID: "exec", Judge0Token: "job", Attempt: "attempt", Claim: "claim", DispatchPayload: payload, RecoveryFailures: tc.failures}}}
			NewSubmissionService(&stores.Storage{Executions: store}, nil, judge0.NewClient()).RecoverJudge0(context.Background())
			if int(calls.Load()) != tc.wantCalls || (len(store.finals) > 0) != tc.wantFinal || len(store.healthy) != 1 || store.healthy[0] != tc.wantHealthy {
				t.Fatalf("calls=%d finals=%+v healthy=%v", calls.Load(), store.finals, store.healthy)
			}
			if len(store.finals) > 0 {
				r := store.finals[0]
				if r.Attempt != "attempt" || r.Claim != "claim" || r.Token != "job" {
					t.Fatal("fence changed", r)
				}
				if tc.stdout == "outage" {
					if r.Status != "judge_error" {
						t.Fatal(r)
					}
				} else {
					if r.Stderr != "warning" {
						t.Fatal("diagnostic lost", r)
					}
					if tc.wantCalls == 2 && string(r.Stdout) != tc.stdout {
						t.Fatal("stdout evidence lost", r)
					}
				}
			}
		})
	}
}
