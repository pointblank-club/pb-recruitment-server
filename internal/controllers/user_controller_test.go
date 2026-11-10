package controllers

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

type mockUserService struct {
	createUserErr error
	signupErr     error
	signupResp    *dto.SignupResponse
}

func (m *mockUserService) CreateUser(ctx context.Context, userID string, req *dto.CreateUserRequest) error {
	return m.createUserErr
}

func (m *mockUserService) Signup(ctx context.Context, req *dto.SignupRequest) (*dto.SignupResponse, error) {
	return m.signupResp, m.signupErr
}

func (m *mockUserService) GetUserProfile(ctx context.Context, userID string) (*models.User, error) {
	return nil, nil
}

func (m *mockUserService) UpdateUserProfile(ctx context.Context, userID string, req *dto.UpdateUserProfileRequest) error {
	return nil
}

func TestSignupConflictResponses(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantError  string
		wantField  string
		hasField   bool
	}{
		{
			name:       "USN duplicate conflict has id field",
			serviceErr: common.USNAlreadyExistsError,
			wantStatus: http.StatusConflict,
			wantError:  common.USNAlreadyExistsError.Error(),
			wantField:  "id",
			hasField:   true,
		},
		{
			name:       "Email duplicate conflict has email field",
			serviceErr: common.EmailAlreadyExistsError,
			wantStatus: http.StatusConflict,
			wantError:  common.EmailAlreadyExistsError.Error(),
			wantField:  "email",
			hasField:   true,
		},
		{
			name:       "UID/User duplicate conflict is fieldless",
			serviceErr: common.UserAlreadyExistsError,
			wantStatus: http.StatusConflict,
			wantError:  common.UserAlreadyExistsError.Error(),
			wantField:  "",
			hasField:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &UserController{
				userService: &mockUserService{signupErr: tt.serviceErr},
			}
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/auth/signup", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.Set(common.VALIDATED_REQUEST_BODY, &dto.SignupRequest{
				USN:          "1DS24CS015",
				MobileNumber: "9876543210",
				CurrentYear:  2,
				Email:        "test@example.com",
				Password:     "password123",
				Name:         "Test User",
			})

			if err := controller.Signup(c); err != nil {
				t.Fatalf("unexpected handler error: %v", err)
			}

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}

			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to decode response JSON: %v", err)
			}

			if body["error"] != tt.wantError {
				t.Fatalf("expected error %q, got %q", tt.wantError, body["error"])
			}

			val, exists := body["field"]
			if exists != tt.hasField {
				t.Fatalf("expected field present=%v, got present=%v (%q)", tt.hasField, exists, val)
			}
			if tt.hasField && val != tt.wantField {
				t.Fatalf("expected field %q, got %q", tt.wantField, val)
			}
		})
	}
}

func TestCreateUserConflictResponses(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantError  string
		wantField  string
		hasField   bool
	}{
		{
			name:       "USN duplicate conflict has id field",
			serviceErr: common.USNAlreadyExistsError,
			wantStatus: http.StatusConflict,
			wantError:  common.USNAlreadyExistsError.Error(),
			wantField:  "id",
			hasField:   true,
		},
		{
			name:       "Email duplicate conflict has email field",
			serviceErr: common.EmailAlreadyExistsError,
			wantStatus: http.StatusConflict,
			wantError:  common.EmailAlreadyExistsError.Error(),
			wantField:  "email",
			hasField:   true,
		},
		{
			name:       "UID/User duplicate conflict is fieldless",
			serviceErr: common.UserAlreadyExistsError,
			wantStatus: http.StatusConflict,
			wantError:  common.UserAlreadyExistsError.Error(),
			wantField:  "",
			hasField:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &UserController{
				userService: &mockUserService{createUserErr: tt.serviceErr},
			}
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/users/create", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.Set(common.AUTH_USER_ID, "firebase-uid-123")
			c.Set(common.VALIDATED_REQUEST_BODY, &dto.CreateUserRequest{
				USN:          "1DS24CS015",
				MobileNumber: "9876543210",
				CurrentYear:  2,
				Name:         "Test User",
			})

			if err := controller.CreateUser(c); err != nil {
				t.Fatalf("unexpected handler error: %v", err)
			}

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}

			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to decode response JSON: %v", err)
			}

			if body["error"] != tt.wantError {
				t.Fatalf("expected error %q, got %q", tt.wantError, body["error"])
			}

			val, exists := body["field"]
			if exists != tt.hasField {
				t.Fatalf("expected field present=%v, got present=%v (%q)", tt.hasField, exists, val)
			}
			if tt.hasField && val != tt.wantField {
				t.Fatalf("expected field %q, got %q", tt.wantField, val)
			}
		})
	}
}

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
