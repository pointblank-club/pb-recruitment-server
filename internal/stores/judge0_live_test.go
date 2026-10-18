package stores_test

import (
	"app/internal/controllers"
	"app/internal/judge0"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
	db := postgresTestDB(t)
	storage := stores.NewStorage(db)
	controller := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, client), nil, client)
	for _, tc := range []struct {
		name, language, code, stdin, wantStatus, wantStdout string
		bounded                                             bool
	}{
		{"unicode run", "python", "import sys\nsys.stdout.write(sys.stdin.read())\n", "नमस्ते 🌍\nsecond line\n", "accepted", "नमस्ते 🌍\nsecond line\n", false},
		{"cpp run", "cpp", "#include <iostream>\nint main(){std::cout << \"empty\";}\n", "", "accepted", "empty", false},
		{"c run", "c", "#include <stdio.h>\nint main(){int c;while((c=getchar())!=EOF)putchar(c);return 0;}\n", "line one\nline two\n", "accepted", "line one\nline two\n", false},
		{"java run", "java", "public class Main { public static void main(String[] args) throws Exception { int c; while((c=System.in.read())!=-1) System.out.write(c); System.out.flush(); } }\n", "Java नमस्ते\n", "accepted", "Java नमस्ते\n", false},
		{"javascript run", "javascript", "process.stdout.write(require('fs').readFileSync(0));\n", "Node 🌍\n", "accepted", "Node 🌍\n", false},
		{"go run", "go", "package main\nimport(\"io\";\"os\")\nfunc main(){io.Copy(os.Stdout,os.Stdin)}\n", "Go नमस्ते\n", "accepted", "Go नमस्ते\n", false},
		{"empty stdin", "python", "import sys\nprint(len(sys.stdin.read()))\n", "", "accepted", "0\n", false},
		{"raw source looks base64", "python", "pass", "", "accepted", "", false},
		{"output bounds", "python", "print('न' * 3000)\n", "", "accepted", "", true},
		{"compilation diagnostics", "cpp", "int main( {\n", "", "failed_to_process", "", false},
		{"CPU timeout", "python", "while True: pass\n", "", "tle", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// This smoke test checks execution; admission is covered with a disposable fake judge.
			if _, err := db.Exec(`DELETE FROM submission_rate_limits WHERE user_id='run:capacity'`); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(dto.RunRequest{Language: tc.language, Code: tc.code, Stdin: tc.stdin})
			start := time.Now()
			w := runRequest(t, controller, httptest.NewRequest(http.MethodPost, "/submission/run", strings.NewReader(string(body))), "live-smoke-"+tc.name)
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
	assertNoRunWrites(t, db)
	// Crash before initial dispatch; recover from the persisted job snapshot against the real judge.
	_, _, _, submissionID, _ := executionFixture(t, db, 0)
	expected := "66\n"
	_, err := storage.Executions.InsertBatch(context.Background(), submissionID, []int{0}, []judge0.SubmissionRequest{{LanguageID: 71, SourceCode: "print(66)\n", Stdin: "", ExpectedOutput: &expected, CPUTimeLimit: 1, MemoryLimit: 262144}})
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
