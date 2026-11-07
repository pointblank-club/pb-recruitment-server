package models

type SubmissionType string

const (
	MCQ  SubmissionType = "mcq"
	Code SubmissionType = "code"
)

type SubmissionStatus string

const (
	// Attempted is a response-only status; stored verdicts remain unchanged.
	Attempted         SubmissionStatus = "attempted"
	Pending           SubmissionStatus = "pending"
	Accepted          SubmissionStatus = "accepted"
	WrongAnswer       SubmissionStatus = "wrong_answer"
	TimeLimitExceed   SubmissionStatus = "tle"
	MemoryLimitExceed SubmissionStatus = "mle"
	RuntimeError      SubmissionStatus = "rte"
	CompilationError  SubmissionStatus = "failed_to_process"
	JudgeError        SubmissionStatus = "judge_error"
)

type TestCaseResult struct {
	ID           string `json:"id"`
	SubmissionID string `json:"submission_id"`
	TestCaseID   string `json:"test_case_id"`
	Status       string `json:"status"`
	Runtime      int64  `json:"runtime"`
	Memory       int64  `json:"memory"`
	CreatedAt    int64  `json:"created_at"`
}

type Submission struct {
	ContestEndTime  int64            `json:"-"`
	ID              string           `json:"id"`
	UserID          string           `json:"user_id"`
	ContestID       string           `json:"contest_id"`
	ProblemID       string           `json:"problem_id"`
	Type            SubmissionType   `json:"type"`
	Language        string           `json:"language,omitempty"` // For code submissions
	Option          []int            `json:"option,omitempty"`   // Selected option(s) for MCQ submissions
	Status          SubmissionStatus `json:"status"`             // e.g., "Pending", "Accepted", "Wrong Answer", etc.
	CreatedAt       int64            `json:"created_at"`         // Unix timestamp
	Runtime         int64            `json:"runtime,omitempty"`
	Memory          int64            `json:"memory,omitempty"`
	TestCaseResults []TestCaseResult `json:"test_case_results,omitempty"`
}
