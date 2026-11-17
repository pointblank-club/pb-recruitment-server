package dto

import (
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestUpsertContestRequestValidation(t *testing.T) {
	v := validator.New()

	baseReq := func() UpsertContestRequest {
		return UpsertContestRequest{
			Name:                  "Test Contest",
			Description:           "",
			RegistrationStartTime: 1000,
			RegistrationEndTime:   2000,
			StartTime:             3000,
			EndTime:               4000,
			EligibleTo:            []int{1, 2},
		}
	}

	t.Run("empty description is valid", func(t *testing.T) {
		req := baseReq()
		req.Description = ""
		if err := v.Struct(req); err != nil {
			t.Fatalf("expected empty description to be valid, got: %v", err)
		}
	})

	t.Run("provided description is valid", func(t *testing.T) {
		req := baseReq()
		req.Description = "Sample contest description"
		if err := v.Struct(req); err != nil {
			t.Fatalf("expected provided description to be valid, got: %v", err)
		}
	})

	t.Run("missing name is invalid", func(t *testing.T) {
		req := baseReq()
		req.Name = ""
		if err := v.Struct(req); err == nil {
			t.Fatalf("expected missing name to fail validation")
		}
	})

	t.Run("end time before start time is invalid", func(t *testing.T) {
		req := baseReq()
		req.EndTime = req.StartTime - 1
		if err := v.Struct(req); err == nil {
			t.Fatalf("expected end time before start time to fail validation")
		}
	})
}
