package dto

type UpdateLeaderboardUserRequest struct {
	Hidden       *bool `json:"hidden"`
	Disqualified *bool `json:"disqualified"`
}

type LeaderboardEntry struct {
	Rank               int            `json:"rank"`
	UserID             string         `json:"user_id"`
	Username           string         `json:"username"`
	USN                string         `json:"usn"`
	TotalScore         float64        `json:"total_score"`
	ProblemsSolved     int            `json:"problems_solved"`
	DSASolved          *int           `json:"dsa_solved,omitempty"` // Older frozen snapshots omit the split counts.
	MCQSolved          *int           `json:"mcq_solved,omitempty"`
	LastSubmissionTime int64          `json:"last_submission_time"`
	ProblemScores      []ProblemScore `json:"problem_scores,omitempty"`
}

type ProblemScore struct {
	ProblemID       string  `json:"problem_id"`
	ProblemName     string  `json:"problem_name"`
	Type            string  `json:"type,omitempty"`
	Score           float64 `json:"score"`
	MaxScore        float64 `json:"max_score"`
	Attempts        int     `json:"attempts"`
	SolvedAt        *int64  `json:"solved_at,omitempty"`
	TestCasesPassed *int    `json:"test_cases_passed,omitempty"`
	TotalTestCases  *int    `json:"total_test_cases,omitempty"`
}

type GetLeaderboardResponse struct {
	Entries    []LeaderboardEntry `json:"entries"`
	Page       int                `json:"page"`
	TotalPages int                `json:"total_pages"`
	TotalCount int                `json:"total_count"`
	Frozen     bool               `json:"-"`
}

type LeaderboardFreezeStatus struct {
	Frozen bool `json:"frozen"`
}

type SetLeaderboardFreezeRequest struct {
	Frozen *bool `json:"frozen" validate:"required"`
}

type ConsolidationResponse struct {
	ContestID          string `json:"contest_id"`
	MCQsGraded         int    `json:"mcqs_graded"`
	ParticipantsRanked int    `json:"participants_ranked"`
	PendingSubmissions int    `json:"pending_submissions"`
	ProcessedAt        int64  `json:"processed_at"`
	Status             string `json:"status"`
}
