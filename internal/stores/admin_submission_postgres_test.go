package stores_test

import (
	"app/internal/stores"
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestListAdminSubmissionsEnumVerdictFilterPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	userID, contestID, problemID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Admin list test',$2,$3,2024,'CS')`, userID, userID+"@example.test", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO contests(id,name,registration_start_time,registration_end_time,start_time,end_time) VALUES($1,'Admin list test',1,2,3,4)`, contestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO problems(id,contest_id,name,score,type) VALUES($1,$2,'Admin list problem',10,'code')`, problemID, contestID); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"accepted", "wrong_answer", "accepted"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO submissions(id,user_id,contest_id,problem_id,type,language,status,created_at) VALUES($1,$2,$3,$4,'code','go',$5::submission_status,$6)`, fmt.Sprintf("submission-%d", i), userID, contestID, problemID, status, i+1); err != nil {
			t.Fatal(err)
		}
	}

	store := stores.NewSubmissionStore(db)
	all, allCount, err := store.ListAdminSubmissions(ctx, contestID, "", "", "", 0, 2)
	if err != nil {
		t.Fatalf("unfiltered list: %v", err)
	}
	if allCount != 3 || len(all) != 2 {
		t.Fatalf("unfiltered page = %d items, count %d; want 2 items, count 3", len(all), allCount)
	}
	accepted, acceptedCount, err := store.ListAdminSubmissions(ctx, contestID, "", "", "accepted", 0, 1)
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if acceptedCount != 2 || len(accepted) != 1 || string(accepted[0].Status) != "accepted" {
		t.Fatalf("accepted page = %#v, count %d; want one accepted item, count 2", accepted, acceptedCount)
	}
	acceptedSecondPage, count, err := store.ListAdminSubmissions(ctx, contestID, "", "", "accepted", 1, 1)
	if err != nil {
		t.Fatalf("filtered second page: %v", err)
	}
	if count != 2 || len(acceptedSecondPage) != 1 || string(acceptedSecondPage[0].Status) != "accepted" {
		t.Fatalf("accepted second page = %#v, count %d; want one accepted item, count 2", acceptedSecondPage, count)
	}
}
