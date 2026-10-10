package judge0

import "encoding/json"

type SubmissionRequest struct {
	SourceCode      string  `json:"source_code"`
	LanguageID      int     `json:"language_id"`
	Stdin           string  `json:"stdin"`
	ExpectedOutput  string  `json:"expected_output"`
	CPUTimeLimit    float64 `json:"cpu_time_limit"`
	MemoryLimit     float64 `json:"memory_limit"`
	CallbackURL     string  `json:"callback_url,omitempty"`
	CompilerOptions string  `json:"compiler_options,omitempty"`
}

type SubmissionResult struct {
	Token string
	Error error
}

type batchTokenResponse struct {
	Token string `json:"token"`
}

type CallbackResult struct {
	Token  string          `json:"token"`
	Time   json.RawMessage `json:"time"`
	Memory *int64          `json:"memory"`
	Status struct {
		ID          int    `json:"id"`
		Description string `json:"description"`
	} `json:"status"`
}

type StatusResponse struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
}
type SubmissionStatusResponse struct {
	Token  string          `json:"token"`
	Time   json.RawMessage `json:"time"`
	Memory *int64          `json:"memory"`
	Status StatusResponse  `json:"status"`
}
