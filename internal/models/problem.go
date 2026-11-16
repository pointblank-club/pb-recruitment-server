package models

type Problem struct {
	ID                 string         `json:"id"` // UUID as string
	ContestID          string         `json:"contest_id"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	Score              int            `json:"score"`
	Type               SubmissionType `json:"type"` // "code" or "mcq"
	TimeLimit          int            `json:"time_limit,omitempty"`
	MemoryLimit        int            `json:"memory_limit,omitempty"`
	HasMultipleAnswers bool           `json:"has_multiple_answers"`
	Answer             []int          `json:"answer"`
	Options            []string       `json:"options"`
	Testcases          string         `json:"-"`
}
