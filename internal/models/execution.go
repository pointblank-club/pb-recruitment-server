package models

import "encoding/json"

type Execution struct {
	DispatchPayload  json.RawMessage `json:"-"`
	Attempt          string          `json:"-"`
	Claim            string          `json:"-"`
	DispatchCount    int             `json:"-"`
	RecoveryFailures int             `json:"-"`
	ID               string          `json:"id"`
	SubmissionID     string          `json:"submission_id"`
	TestCaseIndex    int             `json:"test_case_index"`
	Judge0Token      string          `json:"judge0_token,omitempty"`
}
