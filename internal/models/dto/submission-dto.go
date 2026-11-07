package dto

import "app/internal/models"

type SubmitSubmissionRequest struct {
	ContestID string                `json:"contest_id" validate:"required"`
	ProblemID string                `json:"problem_id" validate:"required"`
	Language  string                `json:"language" validate:"required_if=Type code"`
	Code      string                `json:"code" validate:"required_if=Type code"`
	Option    []int                 `json:"option" validate:"required_if=Type mcq,omitempty,min=1,dive,gte=0,lte=2147483647"`
	Type      models.SubmissionType `json:"type" validate:"required,oneof=mcq code"`
}

type SubmitSubmissionResponse struct {
	SubmissionID string `json:"submission_id"`
}

type ListProblemSubmissionsRequest struct {
	ProblemID string `query:"problem_id" validate:"required"`
	Page      int    `query:"page" validate:"min=0"`
}

type ListProblemSubmissionsResponse struct {
	Submissions []models.Submission `json:"submissions"`
}

type GetSubmissionDetailsResponse struct {
	models.Submission
	Code string `json:"code"`
}

type AdminSubmissionItem struct {
	ID          string                 `json:"id"`
	UserID      string                 `json:"user_id"`
	UserName    string                 `json:"user_name"`
	UserEmail   string                 `json:"user_email"`
	ContestID   string                 `json:"contest_id"`
	ProblemID   string                 `json:"problem_id"`
	ProblemName string                 `json:"problem_name"`
	Type        models.SubmissionType  `json:"type"`
	Language    string                 `json:"language"`
	Status      models.SubmissionStatus `json:"status"`
	CreatedAt   int64                  `json:"created_at"`
	Runtime     int64                  `json:"runtime"`
	Memory      int64                  `json:"memory"`
}

type AdminSubmissionDetails struct {
	AdminSubmissionItem
	Code            string                  `json:"code"`
	TestCaseResults []models.TestCaseResult `json:"test_case_results"`
}

