package dto

import (
	"testing"

	"github.com/go-playground/validator/v10"
)

// ponytail: one check for the tag that stops an admin edit wiping answers.json.
// `required_if=Type code,dive` is easy to break -- without `dive` the validator
// silently skips CreateTestCaseRequest's own tags and every case below passes.
func TestCreateProblemRequestTestcaseValidation(t *testing.T) {
	v := validator.New()

	cases := []struct {
		name    string
		req     CreateProblemRequest
		wantErr bool
	}{
		{"code with no testcases", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "code",
		}, true},
		{"code with testcase missing expected_output", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "code",
			Testcases: []CreateTestCaseRequest{{Input: "5 6"}},
		}, true},
		{"code with testcase missing input", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "code",
			Testcases: []CreateTestCaseRequest{{ExpectedOutput: "11"}},
		}, true},
		{"code fully specified", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "code",
			Testcases: []CreateTestCaseRequest{{Input: "5 6", ExpectedOutput: "11"}},
		}, false},
		{"mcq needs no testcases", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "mcq", Answer: []int{1},
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Struct(tc.req)
			if tc.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no validation error, got %v", err)
			}
		})
	}
}
