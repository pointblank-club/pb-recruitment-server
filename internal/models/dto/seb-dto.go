package dto

const MaxBrowserSessions = 3

type StartBrowserSessionRequest struct {
	AttemptID  string `json:"attempt_id" validate:"required,uuid"`
	Generation *int   `json:"generation" validate:"required,min=0,max=2147483647"`
}

type BrowserSessionResponse struct {
	IsSEB           bool `json:"is_seb"`
	SessionCount    int  `json:"session_count"`
	SEBNotDetected  bool `json:"seb_not_detected"`
	IsTerminated    bool `json:"is_terminated"`
	CookieConfirmed bool `json:"cookie_confirmed"`
	Generation      int  `json:"generation"`
}

type ResetBrowserSessionsResponse struct {
	Generation int `json:"generation"`
}
