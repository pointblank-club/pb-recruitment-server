package dto

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/go-playground/validator/v10"
)

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
		{"code with blank expected_output", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "code",
			Testcases: []CreateTestCaseRequest{{Input: "5 6"}},
		}, false},
		{"code with blank input", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "code",
			Testcases: []CreateTestCaseRequest{{ExpectedOutput: "11"}},
		}, false},
		{"code fully specified", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "code",
			Testcases: []CreateTestCaseRequest{{Input: "5 6", ExpectedOutput: "11"}},
		}, false},
		{"mcq needs no testcases", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "mcq",
			Answer: []int{1}, Options: []string{"a", "b"},
		}, false},
		{"mcq needs an answer", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "mcq", Options: []string{"a", "b"},
		}, true},
		{"mcq rejects an empty answer array", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "mcq",
			Answer: []int{}, Options: []string{"a", "b"},
		}, true},
		{"mcq rejects duplicate answers", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "mcq",
			Answer: []int{1, 1}, Options: []string{"a", "b"},
		}, true},
		{"mcq accepts distinct answers", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "mcq",
			Answer: []int{1, 0}, Options: []string{"a", "b"},
		}, false},
		{"mcq needs options", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "mcq", Answer: []int{0},
		}, true},
		{"mcq needs at least two options", CreateProblemRequest{
			Name: "p", Description: "d", Score: 10, Type: "mcq",
			Answer: []int{0}, Options: []string{"only one"},
		}, true},
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

func TestCreateProblemRequestTestcaseJSON(t *testing.T) {
	for _, tc := range []struct {
		name, testcase        string
		wantErr               bool
		wantInput, wantOutput string
	}{
		{"blank input and answer", `{"input":"","expected_output":"","is_sample":true}`, false, "", ""},
		{"unicode and newlines", `{"input":"नमस्ते\n","expected_output":"λ\n","is_sample":true}`, false, "नमस्ते\n", "λ\n"},
		{"missing input", `{"expected_output":"answer"}`, true, "", ""},
		{"missing answer", `{"input":"input"}`, true, "", ""},
		{"null input", `{"input":null,"expected_output":"answer"}`, true, "", ""},
		{"null answer", `{"input":"input","expected_output":null}`, true, "", ""},
		{"numeric input", `{"input":1,"expected_output":"answer"}`, true, "", ""},
		{"numeric answer", `{"input":"input","expected_output":1}`, true, "", ""},
		{"null testcase", `null`, true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"name":"p","description":"d","score":10,"type":"code","testcases":[%s]}`, tc.testcase)
			var req CreateProblemRequest
			err := json.Unmarshal([]byte(body), &req)
			if err == nil {
				err = validator.New().Struct(req)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("decode and validate: %v; want error %v", err, tc.wantErr)
			}
			if err == nil {
				got := req.Testcases[0]
				if got.Input != tc.wantInput || got.ExpectedOutput != tc.wantOutput || !got.IsSample {
					t.Fatalf("testcase changed while decoding: %+v", got)
				}
			}
		})
	}
}
