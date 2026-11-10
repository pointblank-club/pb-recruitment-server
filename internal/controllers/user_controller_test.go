package controllers

import (
	"app/internal/common"
	"errors"
	"testing"
)

func TestValidateUserInput(t *testing.T) {
	tests := []struct {
		name        string
		usn         string
		mobile      string
		currentYear int
		wantErr     error
		wantField   string
	}{
		{
			name:        "valid 1st year application number",
			usn:         "26UGDS1234",
			mobile:      "9876543210",
			currentYear: 1,
			wantErr:     nil,
			wantField:   "",
		},
		{
			name:        "valid 1st year application number lowercase",
			usn:         "26ugds5678",
			mobile:      "9876543210",
			currentYear: 1,
			wantErr:     nil,
			wantField:   "",
		},
		{
			name:        "invalid 1st year application number format",
			usn:         "25UGDS1234",
			mobile:      "9876543210",
			currentYear: 1,
			wantErr:     common.InvalidApplicationNumberError,
			wantField:   "id",
		},
		{
			name:        "1st year entered USN instead of application number",
			usn:         "1DS24CS001",
			mobile:      "9876543210",
			currentYear: 1,
			wantErr:     common.InvalidApplicationNumberError,
			wantField:   "id",
		},
		{
			name:        "valid 2nd year USN (2024 batch)",
			usn:         "1DS24CS015",
			mobile:      "9876543210",
			currentYear: 2,
			wantErr:     nil,
			wantField:   "",
		},
		{
			name:        "valid 3rd year USN (2023 batch)",
			usn:         "1ds23is042",
			mobile:      "9876543210",
			currentYear: 3,
			wantErr:     nil,
			wantField:   "",
		},
		{
			name:        "valid senior USN (2022 batch)",
			usn:         "1DS22EC010",
			mobile:      "9876543210",
			currentYear: 3,
			wantErr:     nil,
			wantField:   "",
		},
		{
			name:        "invalid USN invalid branch",
			usn:         "1DS24ZZ999",
			mobile:      "9876543210",
			currentYear: 2,
			wantErr:     common.InvalidUSNError,
			wantField:   "id",
		},
		{
			name:        "invalid mobile number less than 10 digits",
			usn:         "1DS24CS015",
			mobile:      "12345",
			currentYear: 2,
			wantErr:     common.InvalidMobileNumberError,
			wantField:   "mobile",
		},
		{
			name:        "invalid mobile number non-numeric",
			usn:         "1DS24CS015",
			mobile:      "987654321a",
			currentYear: 2,
			wantErr:     common.InvalidMobileNumberError,
			wantField:   "mobile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err, field := validateUserInput(tt.usn, tt.mobile, tt.currentYear)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected error %v, got %v", tt.wantErr, err)
			}
			if field != tt.wantField {
				t.Fatalf("expected field %q, got %q", tt.wantField, field)
			}
		})
	}
}
