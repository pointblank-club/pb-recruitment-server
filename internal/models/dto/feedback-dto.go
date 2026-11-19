package dto

type SubmitFeedbackRequest struct {
	PlatformSmoothness   int      `json:"platform_smoothness" validate:"required,min=1,max=5"`
	AtmosphereRating     *int     `json:"atmosphere_rating,omitempty" validate:"omitempty,min=1,max=5"`
	DifficultyRating     *int     `json:"difficulty_rating,omitempty" validate:"omitempty,min=1,max=5"`
	ResponsivenessRating *int     `json:"responsiveness_rating,omitempty" validate:"omitempty,min=1,max=5"`
	TimeRating           *int     `json:"time_rating,omitempty" validate:"omitempty,min=1,max=5"`
	ThoughtsDuringEvent  string   `json:"thoughts_during_event,omitempty"`
	ThoughtsOther        string   `json:"thoughts_other,omitempty"`
	FinalFeedback        string   `json:"final_feedback,omitempty"`
	LogisticsClarity     string   `json:"logistics_clarity,omitempty"`
	LogisticsPunctuality string   `json:"logistics_punctuality,omitempty"`
	LogisticsVenue       string   `json:"logistics_venue,omitempty"`
	BestThing            string   `json:"best_thing,omitempty"`
	RecommendLikelihood  string   `json:"recommend_likelihood,omitempty"`
	ConvincedReasons     []string `json:"convinced_reasons,omitempty"`
	ConvincedOther       string   `json:"convinced_other,omitempty"`
	Suggestions          string   `json:"suggestions,omitempty"`
}

type FeedbackStatusResponse struct {
	CanSubmit    bool   `json:"can_submit"`
	HasSubmitted bool   `json:"has_submitted"`
	Message      string `json:"message,omitempty"`
}
