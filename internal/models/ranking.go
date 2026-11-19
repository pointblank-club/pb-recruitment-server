package models

type Ranking struct {
	ContestID    string  `json:"contest_id"` // Primary key
	UserID       string  `json:"user_id"`    // Primary key
	Score        float64 `json:"score"`
	Rank         int     `json:"rank"`
	Hidden       bool    `json:"hidden"`
	Disqualified bool    `json:"disqualified"`
	Shortlisted  bool    `json:"shortlisted"`
}
