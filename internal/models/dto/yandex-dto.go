package dto

// YandexConnectionResponse describes the candidate's backup Yandex account.
// Enabled is false while the server has no Yandex app configured; Required means contest
// registration refuses candidates who are not connected. YandexLogin can be empty for
// accounts without a proper login (some Google sign-ins); show YandexEmail instead.
type YandexConnectionResponse struct {
	Enabled     bool   `json:"enabled"`
	Required    bool   `json:"required"`
	Connected   bool   `json:"connected"`
	TokenState  string `json:"token_state,omitempty" enums:"active,needs_reauth"`
	YandexLogin string `json:"yandex_login,omitempty"`
	YandexEmail string `json:"yandex_email,omitempty"`
	ConnectedAt int64  `json:"connected_at,omitempty"`
}

type StartYandexConnectionRequest struct {
	// Consent must be true: the candidate ticked the agreement to have their solutions
	// submitted to the backup contest from their Yandex account.
	Consent bool `json:"consent"`
	// ContestID is where the website returns the candidate afterwards. Optional.
	ContestID string `json:"contest_id" validate:"omitempty,max=64"`
}

type StartYandexConnectionResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

type CompleteYandexConnectionRequest struct {
	Code  string `json:"code" validate:"required,max=512"`
	State string `json:"state" validate:"required,max=2048"`
}

type CompleteYandexConnectionResponse struct {
	YandexConnectionResponse
	ContestID string `json:"contest_id,omitempty"`
}
