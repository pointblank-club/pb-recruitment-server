package stores_test

import (
	"app/internal/controllers"
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/labstack/echo/v4"
)

func postgresTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	schema := "execution_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(40)
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	paths, err := filepath.Glob("../migrations/*.up.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("migration files: %v", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(data)); err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
	}
	return db
}

func executionFixture(t *testing.T, db *sql.DB, count int) (string, string, string, string, []models.Execution) {
	t.Helper()
	user, contest, problem := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Test',$2,$1,1,'CS')`, user, user+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time) VALUES($1,'Test','',1,4102444800000,1,4102444800000)`, contest); err != nil {
		t.Fatal(err)
	}
	if err := stores.NewProblemStore(db).CreateProblem(context.Background(), &models.Problem{ID: problem, ContestID: contest, Name: "Test", Score: 10, Type: models.Code, Options: []string{}, Answer: []int{}}); err != nil {
		t.Fatal(err)
	}
	sub, executions := executionSubmission(t, db, user, contest, problem, count)
	return user, contest, problem, sub, executions
}

func executionSubmission(t *testing.T, db *sql.DB, user, contest, problem string, count int) (string, []models.Execution) {
	t.Helper()
	sub, err := stores.NewSubmissionStore(db).CreateSubmission(context.Background(), &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code})
	if err != nil {
		t.Fatal(err)
	}
	indexes := make([]int, count)
	for i := range indexes {
		indexes[i] = i
	}
	executions, err := stores.NewExecutionStore(db).InsertBatch(context.Background(), sub, indexes)
	if err != nil {
		t.Fatal(err)
	}
	return sub, executions
}

func assertSubmission(t *testing.T, db *sql.DB, id, status string, runtime, memory int64) {
	t.Helper()
	var got string
	var gotRuntime, gotMemory int64
	if err := db.QueryRow(`SELECT status,COALESCE(runtime,0),COALESCE(memory,0) FROM submissions WHERE id=$1`, id).Scan(&got, &gotRuntime, &gotMemory); err != nil {
		t.Fatal(err)
	}
	if got != status || gotRuntime != runtime || gotMemory != memory {
		t.Fatalf("submission status/runtime/memory = %s/%d/%d, want %s/%d/%d", got, gotRuntime, gotMemory, status, runtime, memory)
	}
}

func TestJudge0FinalResults(t *testing.T) {
	db := postgresTestDB(t)
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "https://example.test")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "test-secret")
	client := judge0.NewClient()
	storage := stores.NewStorage(db)
	controller := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, client), services.NewContestService(storage, nil), client)
	for _, tc := range []struct {
		id                int
		verdict, testcase string
	}{
		{3, "accepted", "pass"}, {4, "wrong_answer", "wrong_answer"}, {5, "tle", "tle"},
		{6, "failed_to_process", "failed_to_process"}, {7, "rte", "rte"}, {12, "rte", "rte"},
		{13, "judge_error", "judge_error"}, {14, "judge_error", "judge_error"},
	} {
		t.Run(tc.verdict+fmt.Sprint(tc.id), func(t *testing.T) {
			user, contest, _, sub, executions := executionFixture(t, db, 1)
			id, token := executions[0].ID, uuid.NewString()
			body := fmt.Sprintf(`{"token":%q,"time":"0.125","memory":2048,"status":{"id":%d}}`, token, tc.id)
			for i := 0; i < 2; i++ {
				w := httptest.NewRecorder()
				ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/internal/judge0/callback/"+id+"?sig="+client.CallbackSignature(id), strings.NewReader(body)), w)
				ctx.SetParamNames("execution_id")
				ctx.SetParamValues(id)
				if err := controller.Judge0Callback(ctx); err != nil || w.Code != http.StatusOK {
					t.Fatalf("callback: HTTP %d, %v", w.Code, err)
				}
			}
			assertSubmission(t, db, sub, tc.verdict, 125, 2048)
			results, err := storage.Submissions.GetTestCaseResultsBySubmissionID(context.Background(), sub)
			if err != nil || len(results) != 1 || results[0].Status != tc.testcase || results[0].TestCaseID != "0" {
				t.Fatalf("testcase results: %+v, %v", results, err)
			}
			if err := storage.Executions.SaveAttemptToken(context.Background(), id, "", token); err != nil {
				t.Fatal(err)
			}

			// Assert that ProcessFinal creates a ranking row with 0 score for non-accepted and full score for accepted
			var rankingScore int
			if err := db.QueryRow(`SELECT score FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&rankingScore); err != nil {
				t.Fatalf("ranking row missing for verdict %s: %v", tc.verdict, err)
			}
			wantScore := 0
			if tc.verdict == "accepted" {
				wantScore = 10
			}
			if rankingScore != wantScore {
				t.Fatalf("ranking score = %d, want %d for verdict %s", rankingScore, wantScore, tc.verdict)
			}
		})
	}
}

func TestConcurrentExecutionScoring(t *testing.T) {
	db := postgresTestDB(t)
	user, contest, problem, _, executions := executionFixture(t, db, 8)
	store := stores.NewExecutionStore(db)
	var wg sync.WaitGroup
	finals := make([]stores.FinalExecutionResult, 0, 32)
	for _, e := range executions {
		r := stores.FinalExecutionResult{ExecutionID: e.ID, Token: uuid.NewString(), Status: "accepted", Runtime: 10, Memory: 1024}
		finals = append(finals, r, r, r)
	}
	for i := 0; i < 8; i++ {
		_, children := executionSubmission(t, db, user, contest, problem, 1)
		finals = append(finals, stores.FinalExecutionResult{ExecutionID: children[0].ID, Token: uuid.NewString(), Status: "accepted"})
	}
	tokens := make(map[string]string)
	for _, r := range finals {
		tokens[r.ExecutionID] = r.Token
	}
	errors := make(chan error, len(finals)+len(tokens))
	wg.Add(1)
	go func() {
		defer wg.Done()
		for id, token := range tokens {
			errors <- store.SaveAttemptToken(context.Background(), id, "", token)
		}
	}()
	for _, result := range finals {
		wg.Add(1)
		go func(r stores.FinalExecutionResult) {
			defer wg.Done()
			errors <- store.ProcessFinal(context.Background(), r)
		}(result)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var score int
	if err := db.QueryRow(`SELECT score FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&score); err != nil || score != 10 {
		t.Fatalf("score = %d, %v", score, err)
	}
	// The MCQ scorer shares the same ranking lock and must preserve the code score.
	mcq := uuid.NewString()
	if err := stores.NewProblemStore(db).CreateProblem(context.Background(), &models.Problem{ID: mcq, ContestID: contest, Name: "MCQ", Score: 20, Type: models.MCQ, Answer: []int{1}, Options: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	id, err := stores.NewSubmissionStore(db).CreateSubmission(context.Background(), &models.Submission{UserID: user, ContestID: contest, ProblemID: mcq, Type: models.MCQ, Option: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	_, children := executionSubmission(t, db, user, contest, problem, 1)
	start := make(chan struct{})
	mixedErrors := make(chan error, 2)
	go func() {
		<-start
		mixedErrors <- stores.NewSubmissionStore(db).JudgeMCQ(context.Background(), id)
	}()
	go func() {
		<-start
		mixedErrors <- store.ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: children[0].ID, Token: uuid.NewString(), Status: "accepted"})
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-mixedErrors; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(`SELECT score FROM rankings WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&score); err != nil || score != 30 {
		t.Fatalf("combined score = %d, %v", score, err)
	}
}

func TestDispatchFailureAndLateResults(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, executions := executionFixture(t, db, 2)
	store := stores.NewExecutionStore(db)
	token := uuid.NewString()
	if err := store.SaveAttemptToken(context.Background(), executions[1].ID, "", token); err != nil {
		t.Fatal(err)
	}
	if err := store.ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: executions[0].ID, Status: "judge_error"}); err != nil {
		t.Fatal(err)
	}
	assertSubmission(t, db, sub, "pending", 0, 0)
	if err := store.ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: executions[1].ID, Token: token, Status: "accepted", Runtime: 15, Memory: 1000}); err != nil {
		t.Fatal(err)
	}
	assertSubmission(t, db, sub, "judge_error", 15, 1000)
	if err := store.ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: executions[0].ID, Token: uuid.NewString(), Status: "accepted", Runtime: 999, Memory: 999}); err != nil {
		t.Fatal(err)
	}
	results, err := stores.NewSubmissionStore(db).GetTestCaseResultsBySubmissionID(context.Background(), sub)
	if err != nil || len(results) != 2 || results[0].TestCaseID != "0" || results[0].Status != "judge_error" || results[1].TestCaseID != "1" || results[1].Status != "pass" {
		t.Fatalf("results: %+v, %v", results, err)
	}
}

func TestTerminalParentKeepsChildResult(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, executions := executionFixture(t, db, 1)
	store := stores.NewExecutionStore(db)
	token := uuid.NewString()
	if err := store.SaveAttemptToken(context.Background(), executions[0].ID, "", token); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE submissions SET status='judge_error' WHERE id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE submission_executions SET created_at=$2 WHERE id=$1`, executions[0].ID, time.Now().Unix()-120); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ClaimRecovery(context.Background(), 50)
	if err != nil || len(pending) != 0 {
		t.Fatalf("terminal parent must not be polled: %+v, %v", pending, err)
	}
	if err := store.ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: executions[0].ID, Token: token, Status: "accepted", Runtime: 10, Memory: 1000}); err != nil {
		t.Fatal(err)
	}
	assertSubmission(t, db, sub, "judge_error", 0, 0)
	results, err := stores.NewSubmissionStore(db).GetTestCaseResultsBySubmissionID(context.Background(), sub)
	if err != nil || len(results) != 1 || results[0].Status != "pass" {
		t.Fatalf("child result: %+v, %v", results, err)
	}
}

func TestDispatchFailureVerdictPrecedence(t *testing.T) {
	db := postgresTestDB(t)
	store := stores.NewExecutionStore(db)
	for _, status := range []string{"failed_to_process", "wrong_answer", "tle", "rte", "judge_error", "accepted"} {
		for _, failureFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failureFirst=%v", status, failureFirst), func(t *testing.T) {
				_, _, _, sub, executions := executionFixture(t, db, 2)
				fail := func() {
					if err := store.ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: executions[1].ID, Status: "judge_error"}); err != nil {
						t.Fatal(err)
					}
				}
				if failureFirst {
					fail()
				}
				if err := store.ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: executions[0].ID, Token: uuid.NewString(), Status: status, Runtime: 10, Memory: 1000}); err != nil {
					t.Fatal(err)
				}
				if !failureFirst {
					fail()
				}
				want := status
				if want == "accepted" {
					want = "judge_error"
				}
				assertSubmission(t, db, sub, want, 10, 1000)
			})
		}
	}
}

func TestReconcileStoredTerminalMetrics(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, executions := executionFixture(t, db, 2)
	if _, err := db.Exec(`UPDATE submission_executions SET status='accepted',runtime=125,memory=2048 WHERE submission_id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	store := stores.NewExecutionStore(db)
	ready, err := store.TerminalPendingParents(context.Background(), 50)
	if err != nil || len(ready) != 1 {
		t.Fatalf("ready: %+v, %v", ready, err)
	}
	if err := store.ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: executions[0].ID, Status: "wrong_answer", Runtime: 999, Memory: 999}); err != nil {
		t.Fatal(err)
	}
	assertSubmission(t, db, sub, "accepted", 250, 2048)
	results, err := stores.NewSubmissionStore(db).GetTestCaseResultsBySubmissionID(context.Background(), sub)
	if err != nil || len(results) != 2 {
		t.Fatalf("reconciled results: %+v, %v", results, err)
	}
	var runtime, memory int64
	if err := db.QueryRow(`SELECT runtime,memory FROM test_case_results WHERE execution_id=$1`, executions[0].ID).Scan(&runtime, &memory); err != nil || runtime != 125 || memory != 2048 {
		t.Fatalf("stored testcase metrics = %d/%d, %v", runtime, memory, err)
	}
}

func TestRepairLegacyResultIndex(t *testing.T) {
	db := postgresTestDB(t)
	if _, err := db.Exec(`DROP INDEX test_case_results_execution_id_uidx; CREATE UNIQUE INDEX test_case_results_execution_id_uidx ON test_case_results(execution_id) WHERE execution_id IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../migrations/000026_repair_execution_result_index.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(data)); err != nil {
		t.Fatal(err)
	}
	_, _, _, sub, executions := executionFixture(t, db, 1)
	if err := stores.NewExecutionStore(db).ProcessFinal(context.Background(), stores.FinalExecutionResult{ExecutionID: executions[0].ID, Token: uuid.NewString(), Status: "accepted"}); err != nil {
		t.Fatal(err)
	}
	assertSubmission(t, db, sub, "accepted", 0, 0)
}

func TestLeaderboardCachePostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	user, contest, _, _, _ := executionFixture(t, db, 1)

	if _, err := db.ExecContext(ctx, `INSERT INTO rankings(contest_id,user_id,score,hidden,disqualified) VALUES($1,$2,50,false,false) ON CONFLICT(contest_id,user_id) DO UPDATE SET score=50`, contest, user); err != nil {
		t.Fatal(err)
	}

	rankingStore := stores.NewRankingStore(db)
	rankingStore.SetCacheTTL(10 * time.Second)

	// Page 0 (valid page, totalPages = 1)
	resp0, err := rankingStore.GetLeaderboard(ctx, contest, 0)
	if err != nil {
		t.Fatalf("page 0 error: %v", err)
	}
	if len(resp0.Entries) != 1 || resp0.Entries[0].TotalScore != 50 {
		t.Fatalf("unexpected page 0 result: %+v", resp0)
	}

	// Update score in DB directly to verify cache hit returns cached score 50
	if _, err := db.ExecContext(ctx, `UPDATE rankings SET score=99 WHERE contest_id=$1 AND user_id=$2`, contest, user); err != nil {
		t.Fatal(err)
	}

	cachedResp, err := rankingStore.GetLeaderboard(ctx, contest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cachedResp.Entries[0].TotalScore != 50 {
		t.Fatalf("expected cached score 50, got %d", cachedResp.Entries[0].TotalScore)
	}

	// Page 5 (out of bounds, page >= max(totalPages, 1) which is 1)
	resp5, err := rankingStore.GetLeaderboard(ctx, contest, 5)
	if err != nil {
		t.Fatalf("page 5 error: %v", err)
	}
	if len(resp5.Entries) != 0 {
		t.Fatalf("expected empty entries for page 5, got %d", len(resp5.Entries))
	}

	// Invalidate cache for contest
	rankingStore.InvalidateCache(contest)

	// Now page 0 should fetch fresh data from DB (score 99)
	freshResp, err := rankingStore.GetLeaderboard(ctx, contest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if freshResp.Entries[0].TotalScore != 99 {
		t.Fatalf("expected fresh score 99, got %d", freshResp.Entries[0].TotalScore)
	}
}
