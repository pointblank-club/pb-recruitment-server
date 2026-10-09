package dto

import (
	"app/internal/models"
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestSubmitSubmissionRequestTypeValidation(t *testing.T) {
	for _, kind := range []string{"mcq", "code", "MCQ", "Code", "unknown", ""} {
		t.Run(kind, func(t *testing.T) {
			req := SubmitSubmissionRequest{ContestID: "contest", ProblemID: "problem", Type: models.SubmissionType(kind), Language: "cpp", Code: "source"}
			err := validator.New().Struct(req)
			valid := kind == "mcq" || kind == "code"
			if (err == nil) != valid {
				t.Errorf("type %q: validation error=%v", kind, err)
			}
		})
	}
}
