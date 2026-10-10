package dto

import (
	"app/internal/models"
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestSubmitSubmissionRequestTypeValidation(t *testing.T) {
	for _, kind := range []string{"mcq", "code", "MCQ", "Code", "unknown", ""} {
		t.Run(kind, func(t *testing.T) {
			req := SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.SubmissionType(kind), Language: "cpp", Code: "source", Option: []int{0}}
			err := validator.New().Struct(req)
			valid := kind == "mcq" || kind == "code"
			if (err == nil) != valid {
				t.Errorf("type %q: validation error=%v", kind, err)
			}
		})
	}
}

func TestSubmitSubmissionRequestOptionValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    models.SubmissionType
		choices []int
		valid   bool
	}{
		{"missing choices", models.MCQ, nil, false},
		{"empty choices", models.MCQ, []int{}, false},
		{"negative choice", models.MCQ, []int{-1}, false},
		{"oversized choice", models.MCQ, []int{3_000_000_000}, false},
		{"valid choice", models.MCQ, []int{0}, true},
		{"valid multiple choices", models.MCQ, []int{1, 0}, true},
		{"code needs no choices", models.Code, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: tc.kind, Option: tc.choices, Language: "cpp", Code: "source"}
			if err := validator.New().Struct(req); (err == nil) != tc.valid {
				t.Errorf("validation error=%v, valid=%v", err, tc.valid)
			}
		})
	}
}
