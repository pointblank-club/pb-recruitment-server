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
	Progress    string                `json:"progress,omitempty"`
	TimeLimit   int                   `json:"time_limit,omitempty"`
	MemoryLimit int                   `json:"memory_limit,omitempty"`
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
	Name string `json:"name" validate:"required,max=200"`
	// Required for code problems. For MCQ updates, omit to preserve the existing description or send an empty string to clear it.
	Description         string                  `json:"description" validate:"required_if=Type code,max=262144"`
	DescriptionProvided bool                    `json:"-"`
	Score               int                     `json:"score" validate:"required,gt=0"`
	Type                models.SubmissionType   `json:"type" validate:"required,oneof=mcq code"`
	Answer              []int                   `json:"answer,omitempty" validate:"required_if=Type mcq,omitempty,min=1,max=100,unique,dive,gte=0"`
	Options             []string                `json:"options,omitempty" validate:"required_if=Type mcq,omitempty,min=2,max=100,dive,required,max=4096"`
	Testcases           []CreateTestCaseRequest `json:"testcases,omitempty" validate:"required_if=Type code,max=100,dive"`
	TimeLimit           *int                    `json:"time_limit,omitempty" validate:"excluded_unless=Type code,omitempty,min=1,max=10000"` // milliseconds
	MemoryLimit         *int                    `json:"memory_limit,omitempty" validate:"excluded_unless=Type code,omitempty,min=2,max=500"` // MiB
}

func (r *CreateProblemRequest) UnmarshalJSON(data []byte) error {
	var fields struct {
		Description json.RawMessage `json:"description"`
		TimeLimit   json.RawMessage `json:"time_limit"`
		MemoryLimit json.RawMessage `json:"memory_limit"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if string(fields.TimeLimit) == "null" || string(fields.MemoryLimit) == "null" {
		return errors.New("time_limit and memory_limit must be integers when provided")
	}
	type requestAlias CreateProblemRequest
	var decoded requestAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = CreateProblemRequest(decoded)
	r.DescriptionProvided = fields.Description != nil
	return nil
}

type CreateTestCaseRequest struct {
	Input          string `json:"input" binding:"required" validate:"max=1048576"`
	ExpectedOutput string `json:"expected_output" binding:"required" validate:"max=1048576"`
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
