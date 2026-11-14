package stores_test

import (
	"app/internal/models"
	"app/internal/stores"
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
)

func TestProblemLimitsRemovedPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, contest, _, _, _ := executionFixture(t, db, 0)
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
		if list, err := store.GetProblemList(ctx, contest); err != nil || len(list) != 3 {
			t.Fatalf("%s changed problem rows: list=%+v error=%v", direction, list, err)
		}
	}
}
