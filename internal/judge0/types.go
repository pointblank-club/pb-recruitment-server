package judge0

import "encoding/json"

type SubmissionRequest struct {
	SourceCode      string  `json:"source_code"`
	LanguageID      int     `json:"language_id"`
	Stdin           string  `json:"stdin"`
	ExpectedOutput  *string `json:"expected_output,omitempty"`
	CPUTimeLimit    float64 `json:"cpu_time_limit"`
	MemoryLimit     float64 `json:"memory_limit"`
	CallbackURL     string  `json:"callback_url,omitempty"`
	CompilerOptions string  `json:"compiler_options,omitempty"`
	EnableNetwork   bool    `json:"enable_network"`
	WallTimeLimit   float64 `json:"wall_time_limit,omitempty"`
	MaxFileSize     int     `json:"max_file_size,omitempty"`
}

type SubmissionResult struct {
	Token string
	Error error
}

type batchTokenResponse struct {
	Token string `json:"token"`
}

type StatusResponse struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
}
type SubmissionStatusResponse struct {
	Attempt       string          `json:"-"`
	Stdout        json.RawMessage `json:"stdout"`
	Token         string          `json:"token"`
	Time          json.RawMessage `json:"time"`
	Memory        *int64          `json:"memory"`
	CompileOutput json.RawMessage `json:"compile_output"`
	Stderr        json.RawMessage `json:"stderr"`
	Status        StatusResponse  `json:"status"`
}
