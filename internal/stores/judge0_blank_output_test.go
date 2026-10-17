package stores_test

import (
	"app/internal/controllers"
	"app/internal/judge0"
	"app/internal/s3"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func TestBlankExpectedOutputFinalizationPostgres(t *testing.T) {
	db := postgresTestDB(t)
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "http://callback.test")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "test-secret")
	client := judge0.NewClient()
	storage := stores.NewStorage(db)
	controller := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, client), nil, client)
	for _, tc := range []struct {
		name, expected, stdout, want string
		status                       int
		omitExpected, legacy, stale  bool
	}{
		{name: "empty expected", stdout: "null", status: 4, want: "accepted"},
		{name: "newline expected", expected: "\n", stdout: "null", status: 4, want: "accepted"},
		{name: "ASCII whitespace", expected: " \t\r\n\v\f\x00", stdout: "null", status: 4, want: "accepted"},
		{name: "nonblank expected", expected: "0\n", stdout: "null", status: 4, want: "wrong_answer"},
		{name: "Unicode space is significant", expected: "\u00a0", stdout: "null", status: 4, want: "wrong_answer"},
		{name: "missing stdout is unknown", status: 4, want: "wrong_answer"},
		{name: "malformed stdout", stdout: "false", status: 4, want: "wrong_answer"},
		{name: "actual output", stdout: `"d3Jvbmc="`, status: 4, want: "wrong_answer"},
		{name: "no expected output", stdout: "null", status: 4, omitExpected: true, want: "wrong_answer"},
		{name: "legacy no snapshot", stdout: "null", status: 4, legacy: true, want: "pending"},
		{name: "compilation error", stdout: "null", status: 6, want: "failed_to_process"},
		{name: "CPU timeout", stdout: "null", status: 5, want: "tle"},
		{name: "runtime error", stdout: "null", status: 7, want: "rte"},
		{name: "stale attempt", stdout: "null", status: 4, stale: true, want: "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, contest, _, sub, _ := executionFixture(t, db, 0)
			expected := tc.expected
			job := judge0.SubmissionRequest{SourceCode: "pass", LanguageID: 71, ExpectedOutput: &expected, CPUTimeLimit: 1, MemoryLimit: 262144}
			if tc.omitExpected {
				job.ExpectedOutput = nil
			}
			var jobs [][]judge0.SubmissionRequest
			if !tc.legacy {
				jobs = append(jobs, []judge0.SubmissionRequest{job})
			}
			executions, err := storage.Executions.InsertBatch(context.Background(), sub, []int{0}, jobs...)
			if err != nil {
				t.Fatal(err)
			}
			e := executions[0]
			attempt := e.Attempt
			if tc.stale {
				attempt = uuid.NewString()
			}
			payload := map[string]any{"token": uuid.NewString(), "status": map[string]int{"id": tc.status}, "time": "0.01", "memory": 1024}
			if tc.stdout != "" {
				payload["stdout"] = json.RawMessage(tc.stdout)
			}
			body, _ := json.Marshal(payload)
			for range 2 {
				w := httptest.NewRecorder()
				c := echo.New().NewContext(httptest.NewRequest(http.MethodPut, client.CallbackURL(e.ID, attempt), strings.NewReader(string(body))), w)
				c.SetParamNames("execution_id")
				c.SetParamValues(e.ID)
				if err := controller.Judge0Callback(c); err != nil || w.Code != 200 {
					t.Fatal(w.Code, err)
				}
			}
			runtime, memory := int64(10), int64(1024)
			if tc.stale || tc.legacy {
				runtime, memory = 0, 0
			}
			assertSubmission(t, db, sub, tc.want, runtime, memory)
			var score, count int
			if err := db.QueryRow(`SELECT coalesce(sum(score),0) FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&score); err != nil {
				t.Fatal(err)
			}
			wantScore := 0
			if tc.want == "accepted" {
				wantScore = 10
			}
			if score != wantScore {
				t.Fatal("score", score, wantScore)
			}
			if err := db.QueryRow(`SELECT count(*) FROM test_case_results WHERE submission_id=$1`, sub).Scan(&count); err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if tc.stale || tc.legacy {
				wantCount = 0
			}
			if count != wantCount {
				t.Fatal("duplicate or stale testcase result", count, wantCount)
			}
		})
	}
}

func TestTerminalBlankOutputCallbacksPostgres(t *testing.T) {
	db := postgresTestDB(t)
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "http://callback.test")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "test-secret")
	client := judge0.NewClient()
	storage := stores.NewStorage(db)
	controller := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, client), nil, client)
	for _, status := range []string{"accepted", "wrong_answer"} {
		t.Run(status, func(t *testing.T) {
			_, _, _, sub, _ := executionFixture(t, db, 0)
			expected := ""
			items, err := storage.Executions.InsertBatch(context.Background(), sub, []int{0}, []judge0.SubmissionRequest{{SourceCode: "pass", LanguageID: 71, ExpectedOutput: &expected, CPUTimeLimit: 1, MemoryLimit: 262144}})
			if err != nil {
				t.Fatal(err)
			}
			e := items[0]
			currentToken := uuid.NewString()
			if err := storage.Executions.ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: e.ID, Attempt: e.Attempt, Token: currentToken, Status: status, Runtime: 10, Memory: 1024}); err != nil {
				t.Fatal(err)
			}
			for _, stale := range []bool{false, true} {
				attempt, token := e.Attempt, currentToken
				if stale {
					attempt, token = uuid.NewString(), uuid.NewString()
				}
				body, _ := json.Marshal(map[string]any{"token": token, "status": map[string]int{"id": 4}, "stdout": nil, "time": "9.9", "memory": 9999})
				w := httptest.NewRecorder()
				c := echo.New().NewContext(httptest.NewRequest(http.MethodPut, client.CallbackURL(e.ID, attempt), strings.NewReader(string(body))), w)
				c.SetParamNames("execution_id")
				c.SetParamValues(e.ID)
				if err := controller.Judge0Callback(c); err != nil || w.Code != 200 {
					t.Fatal("terminal callback must be acknowledged", w.Code, err)
				}
				assertSubmission(t, db, sub, status, 10, 1024)
			}
		})
	}
}

func TestLegacyBlankOutputRecoveryPostgres(t *testing.T) {
	for _, tc := range []struct {
		name, expected, stdout, want string
		storageFailure               bool
	}{
		{"blank", "\n", "", "accepted", false},
		{"real wrong answer", "required", "", "wrong_answer", false},
		{"storage outage", "\n", "", "judge_error", true},
		{"non-null output during storage outage", "\n", "d3Jvbmc=", "wrong_answer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := postgresTestDB(t)
			_, _, _, sub, items := executionFixture(t, db, 1)
			if _, err := db.Exec(`UPDATE submissions SET language='python' WHERE id=$1`, sub); err != nil {
				t.Fatal(err)
			}
			var objectReads, stdoutReads atomic.Int32
			objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				objectReads.Add(1)
				if tc.storageFailure {
					w.Header().Set("Content-Type", "application/xml")
					w.WriteHeader(403)
					w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
					return
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "testcases.json"):
					w.Write([]byte(`[{"index":0,"input":""}]`))
				case strings.HasSuffix(r.URL.Path, "answers.json"):
					json.NewEncoder(w).Encode([]string{tc.expected})
				case strings.HasSuffix(r.URL.Path, sub):
					w.Write([]byte(base64.StdEncoding.EncodeToString([]byte("pass"))))
				default:
					t.Error("unexpected object read", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer objects.Close()
			for key, value := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "", "AWS_REGION": "us-east-1", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null", "AWS_CONFIG_FILE": "/dev/null", "AWS_EC2_METADATA_DISABLED": "true", "S3_SUBMISSIONS_BUCKET": "test", "AWS_ENDPOINT_URL_S3": objects.URL} {
				t.Setenv(key, value)
			}
			token := uuid.NewString()
			judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("legacy token must not be redispatched")
				}
				result := map[string]any{"token": token, "status": map[string]int{"id": 4}, "time": "0.01", "memory": 1024}
				if strings.Contains(r.URL.Query().Get("fields"), "stdout") {
					stdoutReads.Add(1)
					result["stdout"] = nil
					if tc.stdout != "" {
						result["stdout"] = tc.stdout
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"submissions": []any{result}})
			}))
			defer judge.Close()
			t.Setenv("JUDGE0_URL", judge.URL)
			t.Setenv("JUDGE0_CALLBACK_BASE_URL", "http://callback.test")
			t.Setenv("JUDGE0_CALLBACK_SECRET", "test-secret")
			storage := stores.NewStorage(db)
			client := judge0.NewClient()
			service := services.NewSubmissionService(storage, s3.NewS3Client(), client)
			if tc.stdout == "" {
				payload := judge0.SubmissionStatusResponse{Token: token, Stdout: json.RawMessage("null"), Time: json.RawMessage(`"0.01"`)}
				payload.Status.ID = 4
				if err := service.HandleJudge0Callback(context.Background(), items[0].ID, payload); err != nil {
					t.Fatal(err)
				}
			} else if err := storage.Executions.SaveAttemptToken(context.Background(), items[0].ID, items[0].Attempt, token); err != nil {
				t.Fatal(err)
			}
			assertSubmission(t, db, sub, "pending", 0, 0)
			var savedToken string
			if err := db.QueryRow(`SELECT judge0_token FROM submission_executions WHERE id=$1`, items[0].ID).Scan(&savedToken); err != nil || savedToken != token {
				t.Fatal("callback token lost", savedToken, err)
			}
			passes := 1
			exhausted := tc.storageFailure && tc.stdout == ""
			if exhausted {
				passes = 3
			}
			for i := range passes {
				readyExecutions(t, db, sub)
				service.RecoverJudge0(context.Background())
				if exhausted && i < 2 {
					assertSubmission(t, db, sub, "pending", 0, 0)
				}
			}
			runtime, memory := int64(10), int64(1024)
			if exhausted {
				runtime, memory = 0, 0
			}
			assertSubmission(t, db, sub, tc.want, runtime, memory)
			if tc.stdout != "" && (objectReads.Load() != 0 || stdoutReads.Load() != 1) {
				t.Fatalf("genuine wrong answer should fetch stdout once without storage: objects=%d stdout=%d", objectReads.Load(), stdoutReads.Load())
			}
		})
	}
}
