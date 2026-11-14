package common

import (
	"errors"
	"testing"
)

func TestValidationFunctions(t *testing.T) {
	// Test branch resolution
	tests := []struct {
		dept string
		want string
		ok   bool
	}{
		{"CS", "CS", true},
		{"computer science and engineering", "CS", true},
		{"Information Science & Engineering", "IS", true},
		{"Nonexistent Dept", "", false},
	}

	for _, tt := range tests {
		got, ok := ResolveBranchCode(tt.dept)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ResolveBranchCode(%q) = (%q, %v), want (%q, %v)", tt.dept, got, ok, tt.want, tt.ok)
		}
	}

	// Test 1st year vs upper year validation
	if err, field := ValidateUserInput("26UGDS1234", "9876543210", 1, "CS"); err != nil || field != "" {
		t.Errorf("expected valid 1st year app number, got err=%v field=%s", err, field)
	}

	if err, field := ValidateUserInput("1DS24CS015", "9876543210", 2, "CS"); err != nil || field != "" {
		t.Errorf("expected valid 2nd year USN, got err=%v field=%s", err, field)
	}

	if err, field := ValidateUserInput("INVALID", "9876543210", 2, "CS"); !errors.Is(err, InvalidUSNError) || field != "id" {
		t.Errorf("expected InvalidUSNError, got err=%v field=%s", err, field)
	}
}
