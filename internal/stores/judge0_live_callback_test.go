package stores_test

import (
	"app/internal/controllers"
	"app/internal/judge0"
	"app/internal/services"
	"app/internal/stores"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func TestLiveJudge0Callbacks(t *testing.T)          { testLiveJudge0Finalization(t, true) }
func TestLiveJudge0BlankOutputPolling(t *testing.T) { testLiveJudge0Finalization(t, false) }

func testLiveJudge0Finalization(t *testing.T, callbacks bool) {
	base, callbackBase := os.Getenv("LIVE_JUDGE0_URL"), os.Getenv("LIVE_JUDGE0_CALLBACK_BASE_URL")
	if base == "" || os.Getenv("TEST_DATABASE_URL") == "" || callbacks && callbackBase == "" {
		t.Skip("explicit live Judge0 URL, disposable PostgreSQL, and callback route when enabled required")
	}
	if !callbacks {
		callbackBase = ""
	}
	t.Setenv("JUDGE0_URL", base)
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", callbackBase)
	secret := ""
	if callbacks {
		secret = uuid.NewString()
	}
	t.Setenv("JUDGE0_CALLBACK_SECRET", secret)
	db := postgresTestDB(t)
	storage, client := stores.NewStorage(db), judge0.NewClient()
	service := services.NewSubmissionService(storage, nil, client)
	controller := controllers.NewSubmissionController(service, nil, client)
	type event struct {
		path string
		body []byte
		code int
	}
	received := make(chan event, 16)
	var callbackURL string
	if callbacks {
		e := echo.New()
		e.PUT("/internal/judge0/callback/:execution_id", func(c echo.Context) error {
			body, err := io.ReadAll(io.LimitReader(c.Request().Body, 2<<20))
			if err != nil {
				return err
			}
			c.Request().Body = io.NopCloser(bytes.NewReader(body))
			err = controller.Judge0Callback(c)
			received <- event{c.Request().URL.RequestURI(), body, c.Response().Status}
			return err
		})
		listener, err := net.Listen("tcp", "127.0.0.1:23582")
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewUnstartedServer(e)
		server.Listener.Close()
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)
		callbackURL = server.URL
	}
	for _, tc := range []struct {
		name, source, expected, want string
		language                     int
	}{
		{"empty expected", "pass", "", "accepted", 71},
		{"newline expected", "pass", "\n", "accepted", 71},
		{"real wrong answer", "pass", "required\n", "wrong_answer", 71},
		{"unexpected output", "print('wrong')", "", "wrong_answer", 71},
		{"runtime error", "raise RuntimeError()", "", "rte", 71},
		{"compiler diagnostics", "int main( {", "", "failed_to_process", 54},
		{"binary stderr", "import sys\nsys.stderr.buffer.write(b'warning\\x00\\xff')\nprint('ok')", "ok\n", "accepted", 71},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, contest, _, sub, _ := executionFixture(t, db, 0)
			job := judge0.SubmissionRequest{SourceCode: tc.source, LanguageID: tc.language, ExpectedOutput: &tc.expected, CPUTimeLimit: 1, WallTimeLimit: 5, MemoryLimit: 262144, CompilerOptions: judge0.CompilerOptions(tc.language)}
			items, err := storage.Executions.InsertBatch(context.Background(), sub, []int{0}, []judge0.SubmissionRequest{job})
			if err != nil {
				t.Fatal(err)
			}
			execution := items[0]
			job.CallbackURL = client.CallbackURL(execution.ID, execution.Attempt)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			results, err := client.CreateBatch(ctx, []judge0.SubmissionRequest{job})
			if err != nil || len(results) != 1 || results[0].Token == "" {
				t.Fatal(results, err)
			}
			if err := storage.Executions.SaveAttemptToken(ctx, execution.ID, execution.Attempt, results[0].Token); err != nil {
				t.Fatal(err)
			}
			if callbacks {
				select {
				case delivered := <-received:
					if delivered.code != 200 {
						t.Fatal("callback rejected", delivered.code)
					}
					// Check authentication and replay on a real callback, once per suite.
					if tc.name == "empty expected" {
						for _, valid := range []bool{false, true} {
							path := delivered.path
							if !valid {
								path = strings.ReplaceAll(path, "sig=", "sig=00")
							}
							req, _ := http.NewRequest(http.MethodPut, callbackURL+path, bytes.NewReader(delivered.body))
							req.Header.Set("Content-Type", "application/json")
							response, err := http.DefaultClient.Do(req)
							if err != nil {
								t.Fatal(err)
							}
							response.Body.Close()
							want := 401
							if valid {
								want = 200
							}
							if response.StatusCode != want {
								t.Fatal(response.StatusCode, want)
							}
							<-received
						}
					}
				case <-ctx.Done():
					t.Fatal("callback not delivered", ctx.Err())
				}
			} else {
				for {
					readyExecutions(t, db, sub)
					service.RecoverJudge0(ctx)
					result, err := storage.Submissions.GetSubmissionStatusByID(ctx, sub)
					if err != nil {
						t.Fatal(err)
					}
					if string(result.Status) != "pending" {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(200 * time.Millisecond):
					}
				}
			}
			result, err := storage.Submissions.GetSubmissionStatusByID(ctx, sub)
			if err != nil || string(result.Status) != tc.want {
				t.Fatal(result, tc.want, err)
			}
			var score, count int
			if err := db.QueryRow(`SELECT coalesce(sum(score),0) FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&score); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM test_case_results WHERE submission_id=$1`, sub).Scan(&count); err != nil {
				t.Fatal(err)
			}
			wantScore := 0
			if tc.want == "accepted" {
				wantScore = 10
			}
			if count != 1 || score != wantScore {
				t.Fatal("score/results", score, count)
			}
			var compile, stderr string
			if err := db.QueryRow(`SELECT coalesce(compile_output,''),coalesce(stderr,'') FROM submission_executions WHERE id=$1`, execution.ID).Scan(&compile, &stderr); err != nil {
				t.Fatal(err)
			}
			if tc.name == "compiler diagnostics" && compile == "" || tc.name == "binary stderr" && stderr != "warning\ufffd" {
				t.Fatal("diagnostics", compile, stderr)
			}
		})
	}
}
