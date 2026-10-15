package stores_test

import (
	"app/internal/common"
	"app/internal/controllers"
	"app/internal/judge0"
	"app/internal/models/dto"
	"app/internal/routes"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
)

func TestRunPostgresReplicaLimitsAndNoContestWrites(t *testing.T) {
	db := postgresTestDB(t)
	var dispatches atomic.Int32
	var polls atomic.Int32
	output := strings.Repeat("न", 2000) + "\x00" + string([]byte{0xff})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			dispatches.Add(1)
			var body struct {
				Submissions []map[string]any `json:"submissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Submissions) != 1 {
				t.Error(body, err)
				w.WriteHeader(500)
				return
			}
			job := body.Submissions[0]
			if _, ok := job["expected_output"]; ok {
				t.Error("Run included expected output")
			}
			if _, ok := job["callback_url"]; ok {
				t.Error("Run included submission callback")
			}
			code, _ := base64.StdEncoding.DecodeString(job["source_code"].(string))
			stdin, _ := base64.StdEncoding.DecodeString(job["stdin"].(string))
			if string(code) != "print('नमस्ते')\n" || string(stdin) != "नमस्ते\n" || job["enable_network"] != false || job["cpu_time_limit"] != float64(2) || job["wall_time_limit"] != float64(5) || job["memory_limit"] != float64(262144) || job["max_file_size"] != float64(64) {
				t.Errorf("unsafe or corrupt run request: %+v", job)
			}
			w.WriteHeader(201)
			w.Write([]byte(`[{"token":"run-job"}]`))
			return
		}
		if polls.Add(1) == 1 {
			w.Write([]byte(`{"submissions":[{"token":"run-job","status":{"id":2}}]}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"submissions": []any{map[string]any{"token": "run-job", "status": map[string]int{"id": 3}, "time": "0.125", "memory": 2048, "stdout": base64.StdEncoding.EncodeToString([]byte(output)), "stderr": base64.StdEncoding.EncodeToString([]byte("warning\x00")), "compile_output": base64.StdEncoding.EncodeToString([]byte("compiler\n"))}}})
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	body, _ := json.Marshal(dto.RunRequest{Language: "python", Code: "print('नमस्ते')\n", Stdin: "नमस्ते\n"})
	const n = 12
	responses := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			storage := stores.NewStorage(db)
			client := judge0.NewClient()
			controller := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, client), nil, client)
			recorder := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(string(body))), recorder)
			c.Set(common.AUTH_USER_ID, "runner")
			if err := controller.Run(c); err != nil {
				t.Error(err)
			}
			responses[i] = recorder
		}(i)
	}
	close(start)
	wg.Wait()
	success := 0
	for _, response := range responses {
		if response.Code == 429 {
			if response.Header().Get("Retry-After") != "5" {
				t.Error("missing retry guidance")
			}
			continue
		}
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		success++
		var result dto.RunResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != "accepted" || result.Runtime != 125 || result.Memory != 2048 || result.CompileOutput != "compiler\n" || result.Stderr != "warning" || len(result.Stdout) > 4096 || !utf8.ValidString(result.Stdout) || strings.ContainsRune(result.Stdout, 0) {
			t.Fatalf("invalid run result: %+v", result)
		}
	}
	if success != 1 || dispatches.Load() != 1 {
		t.Fatalf("replica throttle: successes=%d dispatches=%d", success, dispatches.Load())
	}
	for _, table := range []string{"submissions", "submission_executions", "test_case_results", "rankings"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("Run wrote %s: %d %v", table, count, err)
		}
	}
	// Running does not consume the contest submission cooldown.
	if err := stores.NewSubmissionStore(db).CheckSubmissionRateLimit(context.Background(), "runner", 5); err != nil {
		t.Fatal(err)
	}
}

func TestRunAuthenticationAndInvalidInput(t *testing.T) {
	e := echo.New()
	routes.AddSubmissionRoutes(e, nil, controllers.NewSubmissionController(nil, nil, nil))
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/submission/run", nil))
	if w.Code != 401 {
		t.Fatal("unauthenticated route", w.Code)
	}
	controller := controllers.NewSubmissionController(services.NewSubmissionService(nil, nil, nil), nil, nil)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{`, 400}, {`null`, 400}, {`{"language":"ruby","code":"puts(1)"}`, 400}, {`{"language":"python","code":" "}`, 400}, {`{"language":"python","code":"print(1)","stdin":32}`, 400}, {`{"language":"python","code":"print(1)","network":true}`, 400}, {`{} {}`, 400},
		{`{"language":"python","code":"` + strings.Repeat("x", 65537) + `"}`, 400},
		{`{"language":"python","code":"print(1)","stdin":"` + strings.Repeat("x", 65537) + `"}`, 400},
		{strings.Repeat(" ", 1<<20) + `{}`, 413},
		{`{"language":"python","code":"pass"}` + strings.Repeat(" ", 1<<20), 413},
	} {
		w := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(tc.body)), w)
		c.Set(common.AUTH_USER_ID, "runner")
		if err := controller.Run(c); err != nil || w.Code != tc.want {
			t.Fatalf("input length=%d HTTP %d want %d err=%v", len(tc.body), w.Code, tc.want, err)
		}
	}
}

func TestRunPollingDeadlineAndCompilationPostgres(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "compilation"}[terminal], func(t *testing.T) {
			db := postgresTestDB(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					w.WriteHeader(201)
					w.Write([]byte(`[{"token":"job"}]`))
					return
				}
				if terminal {
					w.Write([]byte(`{"submissions":[{"token":"job","status":{"id":6},"compile_output":"ZXJyb3I="}]}`))
				} else {
					w.Write([]byte(`{"submissions":[{"token":"job","status":{"id":2}}]}`))
				}
			}))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			client := judge0.NewClient()
			controller := controllers.NewSubmissionController(services.NewSubmissionService(stores.NewStorage(db), nil, client), nil, client)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(`{"language":"python","code":"print(1)"}`))
			ctx, cancel := context.WithTimeout(r.Context(), 80*time.Millisecond)
			defer cancel()
			r = r.WithContext(ctx)
			c := echo.New().NewContext(r, w)
			c.Set(common.AUTH_USER_ID, "runner")
			start := time.Now()
			if err := controller.Run(c); err != nil {
				t.Fatal(err)
			}
			if terminal {
				var result dto.RunResponse
				json.Unmarshal(w.Body.Bytes(), &result)
				if w.Code != 200 || result.Status != "failed_to_process" || result.CompileOutput != "error" {
					t.Fatal(w.Code, result)
				}
			} else if w.Code != 504 || time.Since(start) > time.Second {
				t.Fatal(w.Code, w.Body.String(), time.Since(start))
			}
		})
	}
}
