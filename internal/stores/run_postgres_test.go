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
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
)

func runRequest(t *testing.T, controller *controllers.SubmissionController, request *http.Request, userID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c := echo.New().NewContext(request, w)
	c.Set(common.AUTH_USER_ID, userID)
	if err := controller.Run(c); err != nil {
		t.Error(err)
	}
	return w
}

func assertNoRunWrites(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"submissions", "submission_executions", "test_case_results", "rankings"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("Run wrote %s: %d %v", table, count, err)
		}
	}
}

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
		result := map[string]any{"token": "run-job", "status": map[string]int{"id": 3}, "time": "0.125", "memory": 2048, "stdout": base64.StdEncoding.EncodeToString([]byte(output)), "stderr": base64.StdEncoding.EncodeToString([]byte("warning\x00")), "compile_output": base64.StdEncoding.EncodeToString([]byte("compiler\n"))}
		fields := strings.Split(r.URL.Query().Get("fields"), ",")
		for field := range result {
			if !slices.Contains(fields, field) {
				delete(result, field)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"submissions": []any{result}})
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
			responses[i] = runRequest(t, controller, httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(string(body))), "runner")
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
		if result.Status != "accepted" || result.Runtime != 125 || result.Memory != 2048 || result.CompileOutput != "compiler\n" || result.Stderr != "warning" || len(result.Stdout) < 4093 || len(result.Stdout) > 4096 || !utf8.ValidString(result.Stdout) || strings.ContainsRune(result.Stdout, 0) {
			t.Fatalf("invalid run result: %+v", result)
		}
	}
	if success != 1 || dispatches.Load() != 1 {
		t.Fatalf("replica throttle: successes=%d dispatches=%d", success, dispatches.Load())
	}
	assertNoRunWrites(t, db)
	// Running does not consume the contest submission cooldown.
	if err := stores.NewSubmissionStore(db).CheckSubmissionRateLimit(context.Background(), "runner", 5); err != nil {
		t.Fatal(err)
	}
}

func TestRunAuthenticationAndInvalidInput(t *testing.T) {
	for _, setting := range []string{"", "false", "true"} {
		t.Setenv("ENABLE_RUN", setting)
		e := echo.New()
		routes.AddSubmissionRoutes(e, nil, controllers.NewSubmissionController(nil, nil, nil))
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/submission/run", nil))
		want := http.StatusNotFound
		if setting == "true" {
			want = http.StatusUnauthorized
		}
		if w.Code != want {
			t.Fatalf("ENABLE_RUN=%q: status=%d want=%d", setting, w.Code, want)
		}
	}
	controller := controllers.NewSubmissionController(services.NewSubmissionService(nil, nil, nil), nil, nil)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{`, 400}, {`null`, 400}, {`{"language":"ruby","code":"puts(1)"}`, 400}, {`{"language":"python","code":" "}`, 400}, {`{"language":"python","code":"print(1)","stdin":32}`, 400}, {`{"language":"python","code":"print(1)","network":true}`, 400}, {`{} {}`, 400},
		{`{"language":"python","code":"` + strings.Repeat("x", 65537) + `"}`, 400},
		{`{"language":"python","code":"print(1)","stdin":"` + strings.Repeat("x", 65537) + `"}`, 400},
		{`{"language":"python","code":"` + string([]byte{0xff}) + `"}`, 400},
		{`{"language":"python","code":"pass","stdin":"` + string([]byte{0xff}) + `"}`, 400},
		{strings.Repeat(" ", 1<<20) + `{}`, 413},
		{`{"language":"python","code":"pass"}` + strings.Repeat(" ", 1<<20), 413},
	} {
		w := runRequest(t, controller, httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(tc.body)), "runner")
		if w.Code != tc.want {
			t.Fatalf("input length=%d HTTP %d want %d", len(tc.body), w.Code, tc.want)
		}
	}
}

func TestRunCapacityAcrossUsersAndBacklogPostgres(t *testing.T) {
	db := postgresTestDB(t)
	var dispatches atomic.Int32
	started, finish := make(chan struct{}), make(chan struct{})
	var release sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if dispatches.Add(1) == 1 {
				close(started)
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`[{"token":"run-job"}]`))
			return
		}
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		w.Write([]byte(`{"submissions":[{"token":"run-job","status":{"id":3},"stdout":null,"stderr":null,"compile_output":null}]}`))
	}))
	defer func() {
		release.Do(func() { close(finish) })
		server.Close()
	}()
	t.Setenv("JUDGE0_URL", server.URL)
	request := func(user string) *httptest.ResponseRecorder {
		controller := controllers.NewSubmissionController(services.NewSubmissionService(stores.NewStorage(db), nil, judge0.NewClient()), nil, nil)
		w := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(`{"language":"python","code":"pass"}`)), w)
		c.Set(common.AUTH_USER_ID, user)
		if err := controller.Run(c); err != nil {
			t.Error(err)
		}
		return w
	}
	setCapacity := func(value string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE submission_rate_limits SET last_submission_at=` + value + ` WHERE user_id='run:capacity'`); err != nil {
			t.Fatal(err)
		}
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- request("alice") }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first Run did not dispatch")
	}
	// Freeze the cooldown so a slow race-enabled test cannot cross its second boundary.
	setCapacity("floor(extract(epoch from clock_timestamp()))::bigint+60")
	if w := request("bob"); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "5" {
		t.Fatal("second replica bypassed fleet limit", w.Code, w.Body.String())
	}
	_, _, _, sub, _ := executionFixture(t, db, 0)
	setCapacity("0")
	if w := request("carol"); w.Code != http.StatusTooManyRequests || dispatches.Load() != 1 {
		t.Fatal("Run bypassed pending parent without executions", w.Code, dispatches.Load())
	}
	if _, err := db.Exec(`UPDATE submissions SET status='accepted' WHERE id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(finish) })
	if w := <-first; w.Code != http.StatusOK {
		t.Fatal("admitted Run lost its response", w.Code, w.Body.String())
	}
	setCapacity("0")
	if w := request("dave"); w.Code != http.StatusOK || dispatches.Load() != 2 {
		t.Fatal("Run did not resume after backlog drained", w.Code, dispatches.Load())
	}
	setCapacity("0")
	if w := request("dave"); w.Code != http.StatusTooManyRequests || dispatches.Load() != 2 {
		t.Fatal("per-user cooldown bypassed", w.Code, dispatches.Load())
	}
}

func TestRunQueueFullPostgres(t *testing.T) {
	db := postgresTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"queue is full"}`))
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	controller := controllers.NewSubmissionController(services.NewSubmissionService(stores.NewStorage(db), nil, judge0.NewClient()), nil, nil)
	w := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(`{"language":"python","code":"pass"}`)), w)
	c.Set(common.AUTH_USER_ID, "runner")
	if err := controller.Run(c); err != nil || w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "5" {
		t.Fatal("explicit rejection must be retryable", w.Code, err)
	}
}

func TestRunResultsPostgres(t *testing.T) {
	db := postgresTestDB(t)
	for _, tc := range []struct {
		name, result string
		want         int
	}{
		{"deadline", `{"token":"job","status":{"id":2}}`, 504},
		{"compilation", `{"token":"job","status":{"id":6},"stdout":null,"stderr":null,"compile_output":"ZXJyb3I="}`, 200},
		{"malformed stdout", `{"token":"job","status":{"id":3},"stdout":"%%%invalid","stderr":null,"compile_output":null}`, 502},
		{"malformed stderr", `{"token":"job","status":{"id":3},"stdout":null,"stderr":"%%%invalid","compile_output":null}`, 502},
		{"malformed compile output", `{"token":"job","status":{"id":3},"stdout":null,"stderr":null,"compile_output":"%%%invalid"}`, 502},
		{"missing stdout", `{"token":"job","status":{"id":3},"stderr":null,"compile_output":null}`, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`DELETE FROM submission_rate_limits WHERE user_id='run:capacity'`); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`[{"token":"job"}]`))
					return
				}
				w.Write([]byte(`{"submissions":[` + tc.result + `]}`))
			}))
			defer server.Close()
			t.Setenv("JUDGE0_URL", server.URL)
			client := judge0.NewClient()
			controller := controllers.NewSubmissionController(services.NewSubmissionService(stores.NewStorage(db), nil, client), nil, client)
			r := httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(`{"language":"python","code":"pass"}`))
			if tc.want == http.StatusGatewayTimeout {
				ctx, cancel := context.WithTimeout(r.Context(), 80*time.Millisecond)
				defer cancel()
				r = r.WithContext(ctx)
			}
			start := time.Now()
			w := runRequest(t, controller, r, tc.name)
			if w.Code != tc.want || tc.want == http.StatusGatewayTimeout && time.Since(start) > time.Second {
				t.Fatal(w.Code, w.Body.String(), time.Since(start))
			}
			if w.Code == http.StatusOK {
				var result dto.RunResponse
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Status != "failed_to_process" || result.CompileOutput != "error" {
					t.Fatal(result, err)
				}
			}
			assertNoRunWrites(t, db)
		})
	}
}
