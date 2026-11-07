package dto

import (
	"app/internal/models"
	"encoding/json"
	"errors"
)

type ProblemOverview struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Score       int                   `json:"score"`
	Type        models.SubmissionType `json:"type"`
	TimeLimit   int                   `json:"time_limit,omitempty"`
	MemoryLimit int                   `json:"memory_limit,omitempty"`
	Progress    string                `json:"progress,omitempty"`
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
	TimeLimit   int                     `json:"time_limit,omitempty" validate:"omitempty,min=1,max=10000"` // milliseconds; submissions run under Judge0's 10s default wall_time_limit
	MemoryLimit int                     `json:"memory_limit,omitempty" validate:"omitempty,min=2,max=500"` // MB; Judge0 accepts 2048-512000 KB
}

type CreateTestCaseRequest struct {
	Input          string `json:"input" binding:"required"`
	ExpectedOutput string `json:"expected_output" binding:"required"`
	IsSample       bool   `json:"is_sample"`
}

// Check presence during decoding to allow blank data; binding tags describe required JSON fields in Swagger.
func (tc *CreateTestCaseRequest) UnmarshalJSON(data []byte) error {
	var fields struct {
		Input          *string `json:"input"`
		ExpectedOutput *string `json:"expected_output"`
		IsSample       bool    `json:"is_sample"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields.Input == nil || fields.ExpectedOutput == nil {
		return errors.New("testcase input and expected_output must be strings; use an empty string for blank data")
	}
	*tc = CreateTestCaseRequest{Input: *fields.Input, ExpectedOutput: *fields.ExpectedOutput, IsSample: fields.IsSample}
	return nil
}

type TestCaseResponse struct {
	Index          int    `json:"index"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output"`
	IsSample       bool   `json:"is_sample"`
}
