package dto

import "app/internal/models"

type ProblemOverview struct {
	ID    string                `json:"id"`
	Name  string                `json:"name"`
	Score int                   `json:"score"`
	Type  models.SubmissionType `json:"type"`
}

type GetProblemStatementResponse struct {
	ProblemID    string                `json:"problem_id"`
	ContestID    string                `json:"contest_id"`
	Name         string                `json:"name"`
	Description  string                `json:"description"`
	Score        int                   `json:"score"`
	Type         models.SubmissionType `json:"type"`
	Answer       []int                 `json:"answer,omitempty"`
	Options      []string              `json:"options,omitempty"`
	Testcases    []TestCaseResponse    `json:"testcases,omitempty"`
	TestcasesKey string                `json:"-"`
	TimeLimit    int                   `json:"time_limit,omitempty"`
	MemoryLimit  int                   `json:"memory_limit,omitempty"`
}

type CreateProblemRequest struct {
	Name        string                  `json:"name" validate:"required"`
	Description string                  `json:"description" validate:"required"`
	Score       int                     `json:"score" validate:"required,gt=0"`
	Type        models.SubmissionType   `json:"type" validate:"required,oneof=mcq code"`
	Answer      []int                   `json:"answer,omitempty" validate:"required_if=Type mcq,omitempty,min=1,unique,dive,gte=0"`
	Options     []string                `json:"options,omitempty" validate:"required_if=Type mcq,omitempty,min=2,dive,required"`
	Testcases   []CreateTestCaseRequest `json:"testcases,omitempty" validate:"required_if=Type code,dive"`
	TimeLimit   int                     `json:"time_limit,omitempty"`
	MemoryLimit int                     `json:"memory_limit,omitempty"`
}

type CreateTestCaseRequest struct {
	Input          string `json:"input" validate:"required"`
	ExpectedOutput string `json:"expected_output" validate:"required"`
	IsSample       bool   `json:"is_sample"`
}

type TestCaseResponse struct {
	Index          int    `json:"index"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output,omitempty"`
	IsSample       bool   `json:"is_sample"`
}
