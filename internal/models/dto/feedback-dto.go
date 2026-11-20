package dto

import (
	"strings"

	"github.com/go-playground/validator/v10"
)

type SubmitFeedbackRequest struct {
	PlatformSmoothness   int      `json:"platform_smoothness" validate:"required,min=1,max=5"`
	AtmosphereRating     int      `json:"atmosphere_rating" validate:"required,min=1,max=5"`
	DifficultyRating     int      `json:"difficulty_rating" validate:"required,min=1,max=5"`
	ResponsivenessRating int      `json:"responsiveness_rating" validate:"required,min=1,max=5"`
	TimeRating           int      `json:"time_rating" validate:"required,min=1,max=5"`
	ThoughtsDuringEvent  string   `json:"thoughts_during_event" validate:"required,max=1000"`
	ThoughtsOther        string   `json:"thoughts_other,omitempty" validate:"max=1000"`
	FinalFeedback        string   `json:"final_feedback" validate:"max=5000"`
	LogisticsClarity     string   `json:"logistics_clarity" validate:"required,max=200"`
	LogisticsPunctuality string   `json:"logistics_punctuality" validate:"required,max=200"`
	LogisticsVenue       string   `json:"logistics_venue" validate:"required,max=200"`
	BestThing            string   `json:"best_thing" validate:"max=2000"`
	RecommendLikelihood  string   `json:"recommend_likelihood" validate:"required,max=200"`
	ConvincedReasons     []string `json:"convinced_reasons" validate:"required,min=1,unique,dive,required,max=200"`
	ConvincedOther       string   `json:"convinced_other,omitempty" validate:"max=1000"`
	Suggestions          string   `json:"suggestions" validate:"max=5000"`
}

func (req *SubmitFeedbackRequest) Trim() {
	if req == nil {
		return
	}
	req.ThoughtsDuringEvent = strings.TrimSpace(req.ThoughtsDuringEvent)
	req.ThoughtsOther = strings.TrimSpace(req.ThoughtsOther)
	req.FinalFeedback = strings.TrimSpace(req.FinalFeedback)
	req.LogisticsClarity = strings.TrimSpace(req.LogisticsClarity)
	req.LogisticsPunctuality = strings.TrimSpace(req.LogisticsPunctuality)
	req.LogisticsVenue = strings.TrimSpace(req.LogisticsVenue)
	req.BestThing = strings.TrimSpace(req.BestThing)
	req.RecommendLikelihood = strings.TrimSpace(req.RecommendLikelihood)
	req.ConvincedOther = strings.TrimSpace(req.ConvincedOther)
	req.Suggestions = strings.TrimSpace(req.Suggestions)
	for i := range req.ConvincedReasons {
		req.ConvincedReasons[i] = strings.TrimSpace(req.ConvincedReasons[i])
	}
}

func ValidateSubmitFeedbackRequest(sl validator.StructLevel) {
	req, ok := sl.Current().Interface().(SubmitFeedbackRequest)
	if !ok {
		if reqPtr, ok := sl.Current().Interface().(*SubmitFeedbackRequest); ok && reqPtr != nil {
			req = *reqPtr
		} else {
			return
		}
	}

	if strings.EqualFold(strings.TrimSpace(req.ThoughtsDuringEvent), "Other") && strings.TrimSpace(req.ThoughtsOther) == "" {
		sl.ReportError(req.ThoughtsOther, "thoughts_other", "thoughts_other", "required", "")
	}

	hasOther := false
	seen := make(map[string]bool, len(req.ConvincedReasons))
	for _, reason := range req.ConvincedReasons {
		trimmed := strings.TrimSpace(reason)
		if trimmed == "" {
			sl.ReportError(req.ConvincedReasons, "convinced_reasons", "convinced_reasons", "required", "")
			break
		}
		if seen[trimmed] {
			sl.ReportError(req.ConvincedReasons, "convinced_reasons", "convinced_reasons", "unique", "")
			break
		}
		seen[trimmed] = true
		if strings.EqualFold(trimmed, "Other") {
			hasOther = true
		}
	}

	if hasOther && strings.TrimSpace(req.ConvincedOther) == "" {
		sl.ReportError(req.ConvincedOther, "convinced_other", "convinced_other", "required", "")
	}
}

type FeedbackStatusResponse struct {
	CanSubmit    bool   `json:"can_submit"`
	HasSubmitted bool   `json:"has_submitted"`
	Message      string `json:"message,omitempty"`
}
