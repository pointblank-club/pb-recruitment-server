package stores_test

import (
	"app/internal/models/dto"
	"app/internal/stores"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUpdateUserProfileDepartmentImmutablePostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.NewString()
	// Insert user with legacy department "CSE"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id, name, email, usn, mobile_number, current_year, department) VALUES($1, 'Initial Name', $2, '1DS24CS099', '9876543210', 2, 'CSE')`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}

	store := stores.NewUserStore(db)

	// Call UpdateUserProfile attempting to change department
	updateReq := &dto.UpdateUserProfileRequest{
		Name:         "Updated Name",
		MobileNumber: "9123456789",
		Department:   "ME",
	}
	if err := store.UpdateUserProfile(ctx, userID, updateReq); err != nil {
		t.Fatalf("unexpected update error: %v", err)
	}

	// Verify that name and mobile updated, but department remained strictly "CSE"
	var name, mobile, dept string
	if err := db.QueryRowContext(ctx, `SELECT name, mobile_number, department FROM users WHERE id = $1`, userID).Scan(&name, &mobile, &dept); err != nil {
		t.Fatal(err)
	}

	if name != "Updated Name" {
		t.Fatalf("expected name 'Updated Name', got %q", name)
	}
	if mobile != "9123456789" {
		t.Fatalf("expected mobile '9123456789', got %q", mobile)
	}
	if dept != "CSE" {
		t.Fatalf("expected department to remain 'CSE', got %q", dept)
	}
}

