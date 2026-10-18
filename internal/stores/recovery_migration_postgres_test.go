package stores_test

import (
	"app/internal/stores"
	"context"
	"os"
	"testing"
	"time"
)

func TestRecoveryDeadlineMigrationPostgres(t *testing.T) {
	db := postgresTestDB(t)
	_, _, _, sub, _ := executionFixture(t, db, 3)
	apply := func(direction string) {
		t.Helper()
		data, err := os.ReadFile("../migrations/000032_simplify_recovery." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
	}
	apply("down")
	if _, err := db.Exec(`UPDATE submission_executions SET
		claim_until=CASE WHEN test_case_index=0 THEN now()+interval '30 seconds' ELSE '-infinity'::timestamptz END,
		next_recovery_at=CASE WHEN test_case_index=1 THEN now()+interval '45 seconds' ELSE '-infinity'::timestamptz END
		WHERE submission_id=$1`, sub); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT id,GREATEST(next_recovery_at,claim_until) FROM submission_executions WHERE submission_id=$1 AND test_case_index<2`, sub)
	if err != nil {
		t.Fatal(err)
	}
	deadlines := make(map[string]time.Time)
	for rows.Next() {
		var id string
		var deadline time.Time
		if err := rows.Scan(&id, &deadline); err != nil {
			t.Fatal(err)
		}
		deadlines[id] = deadline
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	apply("up")
	for id, want := range deadlines {
		var got time.Time
		if err := db.QueryRow(`SELECT next_recovery_at FROM submission_executions WHERE id=$1`, id).Scan(&got); err != nil || !got.Equal(want) {
			t.Fatal("lease or backoff changed", got, want, err)
		}
	}
	claims, err := stores.NewExecutionStore(db).ClaimRecovery(context.Background(), 3, false)
	if err != nil || len(claims) != 1 || claims[0].TestCaseIndex != 2 {
		t.Fatal("migration reclaimed active work", claims, err)
	}
	var obsolete int
	if err := db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='submission_executions_pending_created_idx'`).Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatal("obsolete index remains", obsolete, err)
	}
}
