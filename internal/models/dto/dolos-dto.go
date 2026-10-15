package dto

type PlagiarismPairReviewRequest struct {
	Status string `json:"status" validate:"required,oneof=cleared confirmed unreviewed"`
	Note   string `json:"note" validate:"max=1000"`
}

type PlagiarismPair struct {
	Key             string  `json:"key"`
	LeftFile        string  `json:"left_file"`
	LeftUserID      string  `json:"left_user_id"`
	LeftName        string  `json:"left_name,omitempty"`
	RightFile       string  `json:"right_file"`
	RightUserID     string  `json:"right_user_id"`
	RightName       string  `json:"right_name,omitempty"`
	Similarity      float64 `json:"similarity"`
	LongestFragment int     `json:"longest_fragment"`
	TotalOverlap    int     `json:"total_overlap"`
	ReviewStatus    string  `json:"review_status"`
	ReviewNote      string  `json:"review_note,omitempty"`
}
