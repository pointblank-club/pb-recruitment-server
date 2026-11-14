package stores_test

import (
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestProblemLimitsRemovedPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, contest, problemID, submissionID, executions := executionFixture(t, db, 1)
	store := stores.NewProblemStore(db)
	ctx := context.Background()
	for _, kind := range []models.SubmissionType{models.Code, models.MCQ} {
		problem := &models.Problem{
			ID: uuid.NewString(), ContestID: contest, Name: "original", Description: "question", Score: 10, Type: kind,
			Answer: []int{0}, Options: []string{"one", "two"}, Testcases: "testcases.json",
		}
		if err := store.CreateProblem(ctx, problem); err != nil {
			t.Fatal(err)
		}
		problem.Name = "updated"
		if err := store.UpdateProblem(ctx, problem); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetProblem(ctx, problem.ID, contest)
		if err != nil || got.Name != problem.Name || got.Type != kind || got.TestcasesKey != problem.Testcases {
			t.Fatalf("problem=%+v error=%v", got, err)
		}
	}
	if list, err := store.GetProblemList(ctx, contest); err != nil || len(list) != 3 {
		t.Fatalf("list=%+v error=%v", list, err)
	}
	historical := judge0.SubmissionRequest{
		SourceCode: "print('historical')", LanguageID: 71,
		CPUTimeLimit: 9, MemoryLimit: 512000, WallTimeLimit: 5, MaxFileSize: 64,
	}
	payload, err := json.Marshal(historical)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE submission_executions SET dispatch_payload=$2 WHERE id=$1`, executions[0].ID, payload); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"down", "up"} {
		migration, err := os.ReadFile("../migrations/000042_remove_problem_limits." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name='problems' AND column_name IN ('time_limit','memory_limit')`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if direction == "down" {
			want = 2
		}
		if count != want {
			t.Fatalf("%s: resource columns=%d, want %d", direction, count, want)
		}
		if direction == "down" {
			var cpu, memory int
			if err := db.QueryRow(`SELECT time_limit,memory_limit FROM problems WHERE id=$1`, problemID).Scan(&cpu, &memory); err != nil || cpu != 1000 || memory != 256 {
				t.Fatalf("rollback defaults=%d/%d, error=%v", cpu, memory, err)
			}
			if _, err := db.Exec(`UPDATE problems SET time_limit=9000,memory_limit=500 WHERE id=$1`, problemID); err != nil {
				t.Fatal(err)
			}
		}
		if list, err := store.GetProblemList(ctx, contest); err != nil || len(list) != 3 {
			t.Fatalf("%s changed problem rows: list=%+v error=%v", direction, list, err)
		}
		var saved []byte
		if err := db.QueryRow(`SELECT dispatch_payload FROM submission_executions WHERE id=$1`, executions[0].ID).Scan(&saved); err != nil {
			t.Fatal(err)
		}
		var snapshot judge0.SubmissionRequest
		if err := json.Unmarshal(saved, &snapshot); err != nil || !reflect.DeepEqual(snapshot, historical) {
			t.Fatalf("%s changed pending snapshot: %+v, error=%v", direction, snapshot, err)
		}
	}
	var dispatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Submissions []judge0.SubmissionRequest `json:"submissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Submissions) != 1 {
			t.Errorf("invalid redispatch: %+v, error=%v", body, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		job := body.Submissions[0]
		if job.CPUTimeLimit != historical.CPUTimeLimit || job.MemoryLimit != historical.MemoryLimit ||
			job.WallTimeLimit != historical.WallTimeLimit || job.MaxFileSize != historical.MaxFileSize ||
			job.EnableNetwork || job.SourceCode != base64.StdEncoding.EncodeToString([]byte(historical.SourceCode)) {
			t.Errorf("historical redispatch changed: %+v", job)
		}
		dispatches.Add(1)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`[{"token":"historical"}]`))
	}))
	defer server.Close()
	t.Setenv("JUDGE0_URL", server.URL)
	readyExecutions(t, db, submissionID)
	services.NewSubmissionService(stores.NewStorage(db), nil, judge0.NewClient()).RecoverJudge0(ctx)
	if dispatches.Load() != 1 {
		t.Fatalf("historical redispatches=%d, want 1", dispatches.Load())
	}
}
