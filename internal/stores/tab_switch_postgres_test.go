package stores_test

import (
	"app/internal/common"
	"app/internal/controllers"
	"app/internal/judge0"
	"app/internal/middleware"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/services"
	"app/internal/stores"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomdw "github.com/labstack/echo/v4/middleware"
)

func seedTabSwitchFixture(t *testing.T, db *sql.DB, startOffset, endOffset, regStartOffset, regEndOffset int64) (string, string, string, string) {
	t.Helper()
	user := uuid.NewString()
	contest := uuid.NewString()
	mcqProblem := uuid.NewString()
	codeProblem := uuid.NewString()
	now := time.Now().UnixMilli()

	if _, err := db.Exec(`INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Candidate',$2,$1,2,'CS')`, user, user+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time,eligible_to) VALUES($1,'Tab Switch Contest','',$2,$3,$4,$5,'1,2,3,4')`,
		contest, now+regStartOffset, now+regEndOffset, now+startOffset, now+endOffset,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO contest_registrations(contest_id,user_id,registered_at) VALUES($1,$2,1)`, contest, user); err != nil {
		t.Fatal(err)
	}
	ps := stores.NewProblemStore(db)
	if err := ps.CreateProblem(context.Background(), &models.Problem{
		ID: mcqProblem, ContestID: contest, Name: "MCQ 1", Type: models.MCQ, Score: 10, Options: []string{"A", "B"}, Answer: []int{1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ps.CreateProblem(context.Background(), &models.Problem{
		ID: codeProblem, ContestID: contest, Name: "Code 1", Type: models.Code, Score: 100, TimeLimit: 1000, MemoryLimit: 256,
		Options: []string{}, Answer: []int{},
		Testcases: "problems/" + contest + "/" + codeProblem + "/testcases.json",
	}); err != nil {
		t.Fatal(err)
	}
	return user, contest, mcqProblem, codeProblem
}

func mockCodeS3Client(t *testing.T) *s3.S3 {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "testcases.json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"index": 0, "input": "1\n"}})
		case strings.HasSuffix(r.URL.Path, "answers.json"):
			_ = json.NewEncoder(w).Encode([]string{"1\n"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":           "test",
		"AWS_SECRET_ACCESS_KEY":       "test",
		"AWS_SESSION_TOKEN":           "",
		"AWS_REGION":                  "us-east-1",
		"AWS_SHARED_CREDENTIALS_FILE": "/dev/null",
		"AWS_CONFIG_FILE":             "/dev/null",
		"AWS_EC2_METADATA_DISABLED":   "true",
		"S3_SUBMISSIONS_BUCKET":       "test",
		"AWS_ENDPOINT_URL_S3":         srv.URL,
	} {
		t.Setenv(k, v)
	}
	return s3.NewS3Client()
}

func TestTabSwitchRetryDeduplicationAndDistinctDevicesPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	user, contest, _, _ := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
	cs := stores.NewContestStore(db)

	// Five identical retries with same observation_id (sequential + concurrent) count only once.
	obs1 := uuid.NewString()
	for i := 0; i < 3; i++ {
		res, err := cs.RecordTabSwitch(ctx, contest, user, obs1, 0, 1, "192.0.2.10", "DeviceA")
		if err != nil {
			t.Fatalf("sequential retry %d: %v", i, err)
		}
		if res.TabSwitchCount != 1 || res.IsTerminated || res.Generation != 0 {
			t.Fatalf("retry %d mutated state: %+v", i, res)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := cs.RecordTabSwitch(ctx, contest, user, obs1, 0, 1, "192.0.2.10", "DeviceA")
			if err != nil {
				t.Errorf("concurrent retry error: %v", err)
				return
			}
			if res.TabSwitchCount != 1 || res.IsTerminated {
				t.Errorf("concurrent retry unexpected count: %+v", res)
			}
		}()
	}
	wg.Wait()

	// Separate devices with distinct observation IDs increment accurately to termination.
	for i := 2; i <= 5; i++ {
		res, err := cs.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 0, 1, "198.51.100.2", "DeviceB")
		if err != nil {
			t.Fatalf("device B step %d: %v", i, err)
		}
		wantTerm := i >= 5
		if res.TabSwitchCount != i || res.IsTerminated != wantTerm {
			t.Fatalf("step %d got %+v, want count=%d terminated=%v", i, res, i, wantTerm)
		}
	}

	var histLen int
	if err := db.QueryRowContext(ctx, `SELECT jsonb_array_length(tab_switch_history) FROM contest_registrations WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&histLen); err != nil {
		t.Fatal(err)
	}
	if histLen != 5 {
		t.Fatalf("expected 5 unique history entries, got %d", histLen)
	}
}

func TestTabSwitchConcurrentIncrementAndStickyTerminationPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	user, contest, _, _ := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
	cs := stores.NewContestStore(db)

	// Reach count=3 sequentially.
	for i := 0; i < 3; i++ {
		if _, err := cs.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 0, 1, "192.0.2.1", "UA"); err != nil {
			t.Fatal(err)
		}
	}

	// Race two concurrent distinct observations from count=3 -> must reach 5 (terminated), never 4.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := cs.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 0, 1, "192.0.2.1", "UA"); err != nil {
				t.Errorf("concurrent increment failed: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	status, err := cs.GetCandidateSessionStatus(ctx, contest, user)
	if err != nil {
		t.Fatal(err)
	}
	if status.TabSwitchCount != 5 || !status.IsTabSwitchTerminated {
		t.Fatalf("lost increment or missing termination: %+v", status)
	}

	// Extra reports after termination must leave count=5, terminated=true, history length=5.
	for i := 0; i < 3; i++ {
		res, err := cs.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 0, 2, "192.0.2.1", "UA")
		if err != nil {
			t.Fatal(err)
		}
		if res.TabSwitchCount != 5 || !res.IsTerminated {
			t.Fatalf("post-termination mutation: %+v", res)
		}
	}

	var histLen int
	if err := db.QueryRowContext(ctx, `SELECT jsonb_array_length(tab_switch_history) FROM contest_registrations WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&histLen); err != nil {
		t.Fatal(err)
	}
	if histLen != 5 {
		t.Fatalf("post-termination appended history: len=%d, want 5", histLen)
	}

	// Also verify large-then-small deltas never undo termination or exceed 5.
	user2, contest2, _, _ := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
	if _, err := cs.RecordTabSwitch(ctx, contest2, user2, uuid.NewString(), 0, 5, "192.0.2.1", "UA"); err != nil {
		t.Fatal(err)
	}
	resSmall, err := cs.RecordTabSwitch(ctx, contest2, user2, uuid.NewString(), 0, 1, "192.0.2.1", "UA")
	if err != nil {
		t.Fatal(err)
	}
	if resSmall.TabSwitchCount != 5 || !resSmall.IsTerminated {
		t.Fatalf("small report cleared termination: %+v", resSmall)
	}
}

func TestTabSwitchAdminResetGenerationAndDelayedReportsPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	user, contest, _, _ := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
	cs := stores.NewContestStore(db)

	for i := 0; i < 5; i++ {
		if _, err := cs.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 0, 1, "192.0.2.1", "UA"); err != nil {
			t.Fatal(err)
		}
	}

	// Hold an external lock on contest_registrations? Wait: simulate pre-reset report arriving right behind admin reset.
	newGen, err := cs.ResetContestantTabSwitches(ctx, contest, user, "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	if newGen != 1 {
		t.Fatalf("got generation %d, want 1", newGen)
	}

	var obsCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tab_switch_observations WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&obsCount); err != nil {
		t.Fatal(err)
	}
	if obsCount != 0 {
		t.Fatalf("expected prior-generation observations cleaned up, got %d", obsCount)
	}

	// Delayed gen-0 report must fail with TabSwitchStaleGenerationError (gen=1) and NOT mutate reset state.
	_, err = cs.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 0, 5, "192.0.2.1", "UA")
	var staleErr *common.TabSwitchStaleGenerationError
	if !errors.As(err, &staleErr) {
		t.Fatalf("expected TabSwitchStaleGenerationError, got %v", err)
	}
	if staleErr.Generation != 1 || staleErr.TabSwitchCount != 0 || staleErr.IsTerminated {
		t.Fatalf("unexpected stale error state: %+v", staleErr)
	}

	// Deterministically block a delayed gen-1 report behind an uncommitted reset tx to gen-2.
	resetTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resetTx.ExecContext(ctx, `
		UPDATE contest_registrations
		SET tab_switch_count = 0, is_tab_switch_terminated = FALSE, reset_generation = 2
		WHERE contest_id = $1 AND user_id = $2
	`, contest, user); err != nil {
		resetTx.Rollback()
		t.Fatal(err)
	}

	delayedDone := make(chan error, 1)
	go func() {
		_, err := cs.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 1, 5, "192.0.2.1", "UA")
		delayedDone <- err
	}()

	time.Sleep(100 * time.Millisecond)
	if err := resetTx.Commit(); err != nil {
		t.Fatal(err)
	}
	delayedErr := <-delayedDone
	var delayedStale *common.TabSwitchStaleGenerationError
	if !errors.As(delayedErr, &delayedStale) || delayedStale.Generation != 2 || delayedStale.TabSwitchCount != 0 || delayedStale.IsTerminated {
		t.Fatalf("delayed gen-1 report behind gen-2 reset got %v (stale=%+v)", delayedErr, delayedStale)
	}

	// Report in current generation 2 works normally.
	okRes, err := cs.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 2, 1, "192.0.2.1", "UA")
	if err != nil {
		t.Fatal(err)
	}
	if okRes.TabSwitchCount != 1 || okRes.IsTerminated || okRes.Generation != 2 {
		t.Fatalf("unexpected gen-2 result: %+v", okRes)
	}

	regs, err := cs.GetContestRegistrations(ctx, contest)
	if err != nil || len(regs) != 1 {
		t.Fatalf("GetContestRegistrations: len=%d err=%v", len(regs), err)
	}
	if regs[0].ResetGeneration != 2 || regs[0].TabSwitchCount != 1 || regs[0].IsTabSwitchTerminated {
		t.Fatalf("unexpected registration listing fields: %+v", regs[0])
	}
}

func TestSubmissionAtomicAdmissionAndRacePostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	user, contest, mcqProblem, codeProblem := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)

	judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`[{"token":"tok-1"}]`))
	}))
	defer judge.Close()
	t.Setenv("JUDGE0_URL", judge.URL)
	t.Setenv("JUDGE0_BATCH_SIZE", "10")
	t.Setenv("JUDGE0_CALLBACK_BASE_URL", "")
	t.Setenv("JUDGE0_CALLBACK_SECRET", "")
	t.Setenv("SUBMISSION_RATE_LIMIT_SECONDS", "0")

	storage := stores.NewStorage(db)
	subSvc := services.NewSubmissionService(storage, mockCodeS3Client(t), judge0.NewClient())
	contestSvc := services.NewContestService(storage, nil)
	subCtrl := controllers.NewSubmissionController(subSvc, contestSvc, judge0.NewClient())

	// Reproduce lock race: lock registration row, terminate in committing tx while MCQ CreateSubmission waits on lock.
	termTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := termTx.ExecContext(ctx, `
		UPDATE contest_registrations
		SET tab_switch_count = 5, is_tab_switch_terminated = TRUE
		WHERE contest_id = $1 AND user_id = $2
	`, contest, user); err != nil {
		termTx.Rollback()
		t.Fatal(err)
	}

	mcqDone := make(chan error, 1)
	go func() {
		_, err := subSvc.CreateSubmission(ctx, user, models.MCQ, &dto.SubmitSubmissionRequest{
			ContestID: contest,
			ProblemID: mcqProblem,
			Type:      models.MCQ,
			Option:    []int{1},
		})
		mcqDone <- err
	}()

	time.Sleep(100 * time.Millisecond)
	if err := termTx.Commit(); err != nil {
		t.Fatal(err)
	}

	mcqErr := <-mcqDone
	if !errors.Is(mcqErr, common.ErrTabSwitchTerminated) {
		t.Fatalf("raced MCQ submission should fail with ErrTabSwitchTerminated, got %v", mcqErr)
	}

	// Reproduce the same lock race on Code submissions via store & service: reset first, then race termination against CreateCodeSubmission.
	if _, err := contestSvc.ResetContestantTabSwitches(ctx, contest, user, "admin-1"); err != nil {
		t.Fatal(err)
	}
	codeTermTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codeTermTx.ExecContext(ctx, `
		UPDATE contest_registrations
		SET tab_switch_count = 5, is_tab_switch_terminated = TRUE
		WHERE contest_id = $1 AND user_id = $2
	`, contest, user); err != nil {
		codeTermTx.Rollback()
		t.Fatal(err)
	}
	codeRaceDone := make(chan error, 1)
	go func() {
		_, err := subSvc.CreateSubmission(ctx, user, models.Code, &dto.SubmitSubmissionRequest{
			ContestID: contest,
			ProblemID: codeProblem,
			Type:      models.Code,
			Language:  "python",
			Code:      base64.StdEncoding.EncodeToString([]byte("print(1)\n")),
		})
		codeRaceDone <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if err := codeTermTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if codeRaceErr := <-codeRaceDone; !errors.Is(codeRaceErr, common.ErrTabSwitchTerminated) {
		t.Fatalf("raced Code submission should fail with ErrTabSwitchTerminated, got %v", codeRaceErr)
	}

	// Direct SubmissionService.CreateSubmission for Code also rejects terminated user.
	_, codeErr := subSvc.CreateSubmission(ctx, user, models.Code, &dto.SubmitSubmissionRequest{
		ContestID: contest,
		ProblemID: codeProblem,
		Type:      models.Code,
		Language:  "python",
		Code:      base64.StdEncoding.EncodeToString([]byte("print(1)\n")),
	})
	if !errors.Is(codeErr, common.ErrTabSwitchTerminated) {
		t.Fatalf("direct Code CreateSubmission while terminated: got %v, want ErrTabSwitchTerminated", codeErr)
	}

	// HTTP SubmitSolution returns 403 for terminated user, even when fast-fail passes first or via service gate.
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/submission/submit", nil)
	rec := httptest.NewRecorder()
	ectx := e.NewContext(req, rec)
	ectx.Set(common.AUTH_USER_ID, user)
	ectx.Set(common.VALIDATED_REQUEST_BODY, &dto.SubmitSubmissionRequest{
		ContestID: contest,
		ProblemID: mcqProblem,
		Type:      models.MCQ,
		Option:    []int{1},
	})
	if err := subCtrl.SubmitSolution(ectx); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("HTTP SubmitSolution while terminated got %d, want 403: %s", rec.Code, rec.Body.String())
	}

	var subCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM submissions WHERE user_id=$1`, user).Scan(&subCount); err != nil {
		t.Fatal(err)
	}
	if subCount != 0 {
		t.Fatalf("expected 0 submissions stored while terminated, got %d", subCount)
	}

	// After admin reset, both Code and MCQ submissions succeed.
	if _, err := contestSvc.ResetContestantTabSwitches(ctx, contest, user, "admin-1"); err != nil {
		t.Fatal(err)
	}
	codeID, err := subSvc.CreateSubmission(ctx, user, models.Code, &dto.SubmitSubmissionRequest{
		ContestID: contest,
		ProblemID: codeProblem,
		Type:      models.Code,
		Language:  "python",
		Code:      base64.StdEncoding.EncodeToString([]byte("print(1)\n")),
	})
	if err != nil || codeID == "" {
		t.Fatalf("Code submission after admin reset failed: id=%q err=%v", codeID, err)
	}
}

func TestUnregisterPostStartBypassRejectedPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	// Registration still open AFTER start_time (matching production schedule risk).
	user, contest, _, _ := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
	storage := stores.NewStorage(db)
	cs := services.NewContestService(storage, nil)

	// Terminate user first.
	if _, err := storage.Contests.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 0, 5, "192.0.2.1", "UA"); err != nil {
		t.Fatal(err)
	}

	// Attempting unregister via service or store directly after start_time fails with ErrCannotUnregisterAfterStart.
	if err := cs.ModifyRegistration(ctx, contest, user, dto.UnregisterAction); !errors.Is(err, common.ErrCannotUnregisterAfterStart) {
		t.Fatalf("ModifyRegistration unregister after start: got %v, want ErrCannotUnregisterAfterStart", err)
	}
	if err := storage.Contests.UnregisterUser(ctx, contest, user); !errors.Is(err, common.ErrCannotUnregisterAfterStart) {
		t.Fatalf("UnregisterUser after start: got %v, want ErrCannotUnregisterAfterStart", err)
	}

	// Registration & termination flag remain completely intact.
	status, err := cs.GetCandidateSessionStatus(ctx, contest, user)
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsTabSwitchTerminated || status.TabSwitchCount != 5 {
		t.Fatalf("termination erased after failed unregister: %+v", status)
	}

	// Upcoming contest DOES permit unregistering cleanly.
	upUser, upContest, _, _ := seedTabSwitchFixture(t, db, 600_000, 3_600_000, -120_000, 300_000)
	if err := cs.ModifyRegistration(ctx, upContest, upUser, dto.UnregisterAction); err != nil {
		t.Fatalf("upcoming unregister failed: %v", err)
	}

	// Lock-then-sample: if UnregisterUser blocks on row lock until start_time passes, fresh post-lock time rejects it.
	raceUser, raceContest, _, _ := seedTabSwitchFixture(t, db, 150, 3_600_000, -120_000, 3_600_000)
	holdTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holdTx.ExecContext(ctx, `SELECT 1 FROM contest_registrations WHERE contest_id=$1 AND user_id=$2 FOR UPDATE`, raceContest, raceUser); err != nil {
		holdTx.Rollback()
		t.Fatal(err)
	}
	unregErrCh := make(chan error, 1)
	go func() {
		unregErrCh <- storage.Contests.UnregisterUser(ctx, raceContest, raceUser)
	}()
	time.Sleep(220 * time.Millisecond)
	if err := holdTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if unregErr := <-unregErrCh; !errors.Is(unregErr, common.ErrCannotUnregisterAfterStart) {
		t.Fatalf("unregister waiting across start_time got %v, want ErrCannotUnregisterAfterStart", unregErr)
	}
}

func TestTabSwitchBoundsTruncationAndHistoryCapPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	user, contest, _, _ := seedTabSwitchFixture(t, db, -60_000, 3_600_000, -120_000, 3_600_000)
	storage := stores.NewStorage(db)
	contestCtrl := controllers.NewContestController(services.NewContestService(storage, nil), nil)

	// Mount real Echo validation & BodyLimit middleware without Firebase network call by injecting AUTH_USER_ID in test middleware.
	e := echo.New()
	testGroup := e.Group("")
	testGroup.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(common.AUTH_USER_ID, user)
			return next(c)
		}
	})
	testGroup.POST("/contests/:id/tab-switch", contestCtrl.RecordTabSwitch, echomdw.BodyLimit("4K"), middleware.ValidateRequest(new(dto.TabSwitchRequest)))
	testGroup.GET("/contests/:id/session-status", contestCtrl.GetCandidateSessionStatus)

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"empty json", `{}`, http.StatusBadRequest},
		{"zero count", `{"observation_id":"` + uuid.NewString() + `","generation":0,"count":0}`, http.StatusBadRequest},
		{"negative count", `{"observation_id":"` + uuid.NewString() + `","generation":0,"count":-5}`, http.StatusBadRequest},
		{"int32 max", `{"observation_id":"` + uuid.NewString() + `","generation":0,"count":2147483647}`, http.StatusBadRequest},
		{"int32 overflow", `{"observation_id":"` + uuid.NewString() + `","generation":0,"count":2147483648}`, http.StatusBadRequest},
		{"invalid uuid", `{"observation_id":"abc","generation":0,"count":1}`, http.StatusBadRequest},
		{"oversized body", `{"observation_id":"` + uuid.NewString() + `","generation":0,"count":1,"pad":"` + strings.Repeat("x", 5000) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		req := httptest.NewRequest(http.MethodPost, "/contests/"+contest+"/tab-switch", bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s: got HTTP %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}

	// Verify GET /session-status returns clean JSON without is_submitted.
	statusReq := httptest.NewRequest(http.MethodGet, "/contests/"+contest+"/session-status", nil)
	statusRec := httptest.NewRecorder()
	e.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("session-status HTTP %d: %s", statusRec.Code, statusRec.Body.String())
	}
	if strings.Contains(statusRec.Body.String(), "is_submitted") {
		t.Fatalf("unexpected dead is_submitted field in response: %s", statusRec.Body.String())
	}

	// Verify long multi-byte UA & IP are valid UTF-8 truncated to byte bounds.
	longUA := strings.Repeat("🔥", 100) // 400 UTF-8 bytes
	longIP := strings.Repeat("1.2.3.4,", 15)
	if _, err := storage.Contests.RecordTabSwitch(ctx, contest, user, uuid.NewString(), 0, 1, longIP, longUA); err != nil {
		t.Fatal(err)
	}
	var rawHist []byte
	if err := db.QueryRowContext(ctx, `SELECT tab_switch_history FROM contest_registrations WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&rawHist); err != nil {
		t.Fatal(err)
	}
	var histEntries []map[string]any
	if err := json.Unmarshal(rawHist, &histEntries); err != nil {
		t.Fatal(err)
	}
	gotUA, _ := histEntries[0]["user_agent"].(string)
	gotIP, _ := histEntries[0]["ip"].(string)
	if len(gotUA) > dto.TabSwitchMaxUserAgentBytes || !utf8.ValidString(gotUA) {
		t.Fatalf("invalid bounded user_agent: len=%d validUTF8=%v", len(gotUA), utf8.ValidString(gotUA))
	}
	if len(gotIP) > dto.TabSwitchMaxIPBytes || !utf8.ValidString(gotIP) {
		t.Fatalf("invalid bounded ip: len=%d validUTF8=%v", len(gotIP), utf8.ValidString(gotIP))
	}

	// Verify tab_switch_history never grows past 100 entries across repeated switches & resets.
	gen := 0
	for i := 0; i < 60; i++ {
		if _, err := storage.Contests.RecordTabSwitch(ctx, contest, user, uuid.NewString(), gen, 1, "192.0.2.1", "UA"); err != nil {
			t.Fatal(err)
		}
		var err error
		gen, err = storage.Contests.ResetContestantTabSwitches(ctx, contest, user, "admin")
		if err != nil {
			t.Fatal(err)
		}
	}
	var histLen int
	if err := db.QueryRowContext(ctx, `SELECT jsonb_array_length(tab_switch_history) FROM contest_registrations WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&histLen); err != nil {
		t.Fatal(err)
	}
	if histLen != dto.TabSwitchMaxHistoryEntries {
		t.Fatalf("history len=%d, want capped at %d", histLen, dto.TabSwitchMaxHistoryEntries)
	}
}

func TestMigration038DownAndUpPostgres(t *testing.T) {
	db := postgresTestDB(t)

	downSQL, err := os.ReadFile("../migrations/000038_tab_switching.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL, err := os.ReadFile("../migrations/000038_tab_switching.up.sql")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(string(downSQL)); err != nil {
		t.Fatalf("000038 down migration failed: %v", err)
	}
	var obsExists bool
	if err := db.QueryRow(`SELECT to_regclass('tab_switch_observations') IS NOT NULL`).Scan(&obsExists); err != nil {
		t.Fatal(err)
	}
	if obsExists {
		t.Fatal("tab_switch_observations should not exist after down migration")
	}

	if _, err := db.Exec(string(upSQL)); err != nil {
		t.Fatalf("000038 re-up migration failed: %v", err)
	}
	if err := db.QueryRow(`SELECT to_regclass('tab_switch_observations') IS NOT NULL`).Scan(&obsExists); err != nil {
		t.Fatal(err)
	}
	if !obsExists {
		t.Fatal("tab_switch_observations missing after re-up migration")
	}
}
