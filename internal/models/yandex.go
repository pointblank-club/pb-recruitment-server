package models

const (
	YandexTokenActive      = "active"
	YandexTokenNeedsReauth = "needs_reauth"
)

// YandexAccount is a candidate's connected Yandex account (table yandex_accounts).
// AccessToken and RefreshToken hold sealed values, never plain tokens. Times are Unix ms.
type YandexAccount struct {
	UserID         string
	Email          string
	YandexUID      int64
	YandexLogin    string
	YandexEmail    string
	AccessToken    string
	RefreshToken   string
	TokenExpiresAt int64
	TokenState     string
	ConsentedAt    int64
	ConnectedAt    int64
}
