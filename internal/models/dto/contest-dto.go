package dto

import "app/internal/models"

// GetContestResponse represents the response for getting contest details
type GetContestResponse struct {
	models.Contest
	IsRegistered *bool `json:"is_registered,omitempty"` // Whether the user is registered for the contest
}

type UpsertContestRequest struct {
	Name                  string `json:"name" validate:"required"`
	Description           string `json:"description" validate:"required"` // base64 encoded
	RegistrationStartTime int64  `json:"registration_start_time" validate:"required"`
	RegistrationEndTime   int64  `json:"registration_end_time" validate:"required,gtfield=RegistrationStartTime"`
	StartTime             int64  `json:"start_time" validate:"required,gtfield=RegistrationStartTime"`
	EndTime               int64  `json:"end_time" validate:"required,gtfield=StartTime"`
	EligibleTo            []int  `json:"eligible_to" validate:"required,dive,oneof=1 2 3"` // Student year restriction
}

type ModifyRegistrationRequest struct {
	Action RegisterationAction `json:"action" validate:"required,oneof=register unregister"`
}

type RegisterationAction string

const (
	RegisterAction   RegisterationAction = "register"
	UnregisterAction RegisterationAction = "unregister"
)

type ContestRegistration struct {
	UserID                string `json:"user_id"`
	Name                  string `json:"name"`
	Email                 string `json:"email"`
	USN                   string `json:"usn"`
	Department            string `json:"department"`
	CurrentYear           int    `json:"current_year"`
	RegisteredAt          int64  `json:"registered_at"`
	Score                 int    `json:"score"`
	Hidden                bool   `json:"hidden"`
	Disqualified          bool   `json:"disqualified"`
	TabSwitchCount        int    `json:"tab_switch_count"`
	IsTabSwitchTerminated bool   `json:"is_tab_switch_terminated"`
}

// TabSwitchRequest represents the payload for reporting a tab switch event.
type TabSwitchRequest struct {
	Count int `json:"count"`
}

// TabSwitchResponse represents the result of ingesting a tab switch event.
type TabSwitchResponse struct {
	TabSwitchCount int  `json:"tab_switch_count"`
	IsTerminated   bool `json:"is_terminated"`
}

// CandidateSessionStatusResponse represents the candidate's current contest session status.
type CandidateSessionStatusResponse struct {
	TabSwitchCount        int  `json:"tab_switch_count"`
	IsTabSwitchTerminated bool `json:"is_tab_switch_terminated"`
	IsSubmitted           bool `json:"is_submitted"`
}

// ResetTabSwitchesResponse represents the response when an admin resets tab switches.
type ResetTabSwitchesResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}
