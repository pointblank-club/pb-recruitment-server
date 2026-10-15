package stores_test

import (
	"app/internal/common"
	"app/internal/controllers"
	"app/internal/judge0"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
)

func TestLiveJudge0Smoke(t *testing.T) {
	url := os.Getenv("LIVE_JUDGE0_URL")
	if url == "" || os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("explicit live Judge0 URL and disposable PostgreSQL URL required")
	}
	t.Setenv("JUDGE0_URL", url)
	t.Setenv("JUDGE0_AUTH_TOKEN", "")
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "")
	t.Setenv("JUDGE0_BATCH_SIZE", "2")
	client := judge0.NewClient()
	cases := []struct{ language, source, stdin string }{
		{"python", "import sys\nsys.stdout.write(sys.stdin.read())\n", "नमस्ते 🌍\nsecond line\n"},
		{"cpp", "#include <iostream>\nint main(){std::cout << \"empty\";}\n", ""},
		{"c", "#include <stdio.h>\nint main(){int c;while((c=getchar())!=EOF)putchar(c);return 0;}\n", "line one\nline two\n"},
		{"java", "public class Main { public static void main(String[] args) throws Exception { int c; while((c=System.in.read())!=-1) System.out.write(c); System.out.flush(); } }\n", "Java नमस्ते\n"},
		{"javascript", "process.stdout.write(require('fs').readFileSync(0));\n", "Node 🌍\n"},
		{"go", "package main\nimport(\"io\";\"os\")\nfunc main(){io.Copy(os.Stdout,os.Stdin)}\n", "Go नमस्ते\n"},
	}
	jobs := make([]judge0.SubmissionRequest, len(cases))
	for i, tc := range cases {
		id, err := judge0.LanguageID(tc.language)
		if err != nil {
			t.Fatal(err)
		}
		expected := tc.stdin
		if tc.language == "cpp" {
			expected = "empty"
		}
		jobs[i] = judge0.SubmissionRequest{SourceCode: tc.source, LanguageID: id, Stdin: tc.stdin, ExpectedOutput: &expected, CPUTimeLimit: 1, MemoryLimit: 262144, CompilerOptions: judge0.CompilerOptions(id)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dispatch, err := client.CreateBatch(ctx, jobs)
	if err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, len(dispatch))
	for i, result := range dispatch {
		if result.Error != nil || result.Token == "" {
			t.Fatalf("dispatch %s: %+v", cases[i].language, result)
		}
		tokens[i] = result.Token
	}
	for {
		results, err := client.GetSubmissions(ctx, tokens)
		if err != nil {
			t.Fatal(err)
		}
		pending := false
		for i, result := range results {
			if result == nil {
				t.Fatalf("missing %s result", cases[i].language)
			}
			if result.Status.ID <= 2 {
				pending = true
				continue
			}
			var raw string
			json.Unmarshal(result.Stdout, &raw)
			stdout, err := base64.StdEncoding.DecodeString(raw)
			if result.Status.ID != 3 || err != nil || string(stdout) != *jobs[i].ExpectedOutput {
				t.Fatalf("%s status=%+v stdout=%q stderr=%s compiler=%s err=%v", cases[i].language, result.Status, stdout, result.Stderr, result.CompileOutput, err)
			}
		}
		if !pending {
			for i, result := range results {
				t.Logf("LIVE language=%s status=%s time=%s", cases[i].language, result.Status.Description, result.Time)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	db := postgresTestDB(t)
	storage := stores.NewStorage(db)
	controller := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, client), nil, client)
	for _, tc := range []struct {
		name, language, code, stdin, wantStatus, wantStdout string
		bounded                                             bool
	}{
		{"unicode run", "python", "print(input())\n", "नमस्ते 🌍\n", "accepted", "नमस्ते 🌍\n", false},
		{"empty stdin", "python", "import sys\nprint(len(sys.stdin.read()))\n", "", "accepted", "0\n", false},
		{"raw source looks base64", "python", "pass", "", "accepted", "", false},
		{"output bounds", "python", "print('न' * 3000)\n", "", "accepted", "", true},
		{"compilation diagnostics", "cpp", "int main( {\n", "", "failed_to_process", "", false},
		{"CPU timeout", "python", "while True: pass\n", "", "tle", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(dto.RunRequest{Language: tc.language, Code: tc.code, Stdin: tc.stdin})
			w := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(string(body))), w)
			c.Set(common.AUTH_USER_ID, "live-smoke-"+tc.name)
			start := time.Now()
			if err := controller.Run(c); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			var result dto.RunResponse
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("result=%+v", result)
			}
			if tc.bounded {
				if len(result.Stdout) < 4093 || len(result.Stdout) > 4096 || !utf8.ValidString(result.Stdout) {
					t.Fatalf("output bytes=%d valid=%v", len(result.Stdout), utf8.ValidString(result.Stdout))
				}
			} else if result.Stdout != tc.wantStdout {
				t.Fatalf("stdout=%q want=%q", result.Stdout, tc.wantStdout)
			}
			if tc.wantStatus == "failed_to_process" && result.CompileOutput == "" {
				t.Fatal("compiler diagnostics missing")
			}
			t.Logf("LIVE run=%s status=%s stdout_bytes=%d compiler_bytes=%d runtime_ms=%d memory_kib=%d elapsed=%s", tc.name, result.Status, len(result.Stdout), len(result.CompileOutput), result.Runtime, result.Memory, time.Since(start))
		})
	}
	for _, table := range []string{"submissions", "submission_executions", "test_case_results", "rankings"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("run wrote %s: count=%d err=%v", table, count, err)
		}
	}
	// Crash before initial dispatch; recover from the persisted job snapshot against the real judge.
	_, _, _, submissionID, _ := executionFixture(t, db, 0)
	expected := "66\n"
	_, err = storage.Executions.InsertBatch(context.Background(), submissionID, []int{0}, []judge0.SubmissionRequest{{LanguageID: 71, SourceCode: "print(66)\n", Stdin: "", ExpectedOutput: &expected, CPUTimeLimit: 1, MemoryLimit: 262144}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		readyExecutions(t, db, submissionID)
		services.NewSubmissionService(storage, nil, client).RecoverJudge0(context.Background())
		sub, err := storage.Submissions.GetSubmissionStatusByID(context.Background(), submissionID)
		if err != nil {
			t.Fatal(err)
		}
		if string(sub.Status) == "accepted" {
			t.Log("LIVE recovered crashed dispatch, polled real token, finalized accepted and scored once")
			break
		}
		if string(sub.Status) != "pending" || time.Now().After(deadline) {
			t.Fatalf("live recovery status=%s", sub.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	var score, resultsCount int
	if err := db.QueryRow(`SELECT sum(score) FROM rankings`).Scan(&score); err != nil || score != 10 {
		t.Fatal("score", score, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM test_case_results WHERE submission_id=$1`, submissionID).Scan(&resultsCount); err != nil || resultsCount != 1 {
		t.Fatal("testcase result count", resultsCount, err)
	}

}
