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
	Testcases    []TestCaseResponse    `json:"testcases,omitempty"`
	TestcasesKey string                `json:"-"`
}

type CreateProblemRequest struct {
	Name        string                  `json:"name" validate:"required"`
	Description string                  `json:"description" validate:"required"`
	Score       int                     `json:"score" validate:"required,gt=0"`
	Type        models.SubmissionType   `json:"type" validate:"required,oneof=mcq code"`
	Answer      []int                   `json:"answer"` // required only for MCQ
	Testcases   []CreateTestCaseRequest `json:"testcases,omitempty"`
}

type CreateTestCaseRequest struct {
	Input          string `json:"input" validate:"required"`
	ExpectedOutput string `json:"expected_output" validate:"required"`
}

type TestCaseResponse struct {
	Index          int    `json:"index"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output,omitempty"`
}
