package models

type Problem struct {
	ID                 string         `json:"id"` // UUID as string
	ContestID          string         `json:"contest_id"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	Score              int            `json:"score"`
	Type               SubmissionType `json:"type"` // "code" or "mcq"
	HasMultipleAnswers bool           `json:"has_multiple_answers"`
	Answer             []int          `json:"answer"`
	Testcases          string         `json:"testcases"`
}
