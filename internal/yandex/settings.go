package yandex

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAuthorizeURL = "https://oauth.yandex.com/authorize"
	DefaultTokenURL     = "https://oauth.yandex.ru/token"
	DefaultInfoURL      = "https://login.yandex.ru/info"
)

// Settings is the server's Yandex connection configuration.
//
// The connect step is enabled when the candidate OAuth app (client id, secret, redirect
// URI) and YANDEX_TOKEN_KEY are all set. REQUIRE_YANDEX_CONNECT additionally makes contest
// registration refuse candidates without a connected account.
type Settings struct {
	Required bool
	TokenKey []byte
	OAuth    OAuthConfig
}

func (s Settings) Enabled() bool {
	return s.OAuth.ClientID != "" && s.OAuth.ClientSecret != "" && s.OAuth.RedirectURI != "" && len(s.TokenKey) == 32
}

// ConnectRequired reports REQUIRE_YANDEX_CONNECT. An invalid value reads as false here;
// LoadSettings rejects it so the server refuses to start with it.
func ConnectRequired(getenv func(string) string) bool {
	required, err := parseBool(getenv("REQUIRE_YANDEX_CONNECT"))
	return err == nil && required
}

// LoadSettings reads the environment. A partial setup is an error rather than silently
// disabling the connect step, and so is requiring a connection that cannot be made.
func LoadSettings(getenv func(string) string) (Settings, error) {
	get := func(key string) string { return strings.TrimSpace(getenv(key)) }
	var errs []error

	required, err := parseBool(get("REQUIRE_YANDEX_CONNECT"))
	if err != nil {
		errs = append(errs, errors.New("REQUIRE_YANDEX_CONNECT must be true or false"))
	}
	s := Settings{
		Required: required,
		OAuth: OAuthConfig{
			ClientID:     get("YANDEX_OAUTH_CLIENT_ID"),
			ClientSecret: get("YANDEX_OAUTH_CLIENT_SECRET"),
			RedirectURI:  get("YANDEX_OAUTH_REDIRECT_URI"),
			AuthorizeURL: orDefault(get("YANDEX_OAUTH_AUTHORIZE_URL"), DefaultAuthorizeURL),
			TokenURL:     orDefault(get("YANDEX_OAUTH_URL"), DefaultTokenURL),
			InfoURL:      orDefault(get("YANDEX_LOGIN_INFO_URL"), DefaultInfoURL),
			Timeout:      10 * time.Second,
		},
	}
	if raw := get("YANDEX_TOKEN_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) != 32 {
			errs = append(errs, errors.New("YANDEX_TOKEN_KEY must be 32 bytes encoded as standard base64 (openssl rand -base64 32)"))
		} else {
			s.TokenKey = key
		}
	}
	if s.OAuth.RedirectURI != "" && !validURL(s.OAuth.RedirectURI) {
		errs = append(errs, errors.New("YANDEX_OAUTH_REDIRECT_URI must be an absolute http(s) URL"))
	}
	for name, value := range map[string]string{
		"YANDEX_OAUTH_AUTHORIZE_URL": s.OAuth.AuthorizeURL,
		"YANDEX_OAUTH_URL":           s.OAuth.TokenURL,
		"YANDEX_LOGIN_INFO_URL":      s.OAuth.InfoURL,
	} {
		if !validURL(value) {
			errs = append(errs, fmt.Errorf("%s must be an absolute http(s) URL", name))
		}
	}

	missing := []string{}
	for _, item := range []struct {
		name string
		set  bool
	}{
		{"YANDEX_OAUTH_CLIENT_ID", s.OAuth.ClientID != ""},
		{"YANDEX_OAUTH_CLIENT_SECRET", s.OAuth.ClientSecret != ""},
		{"YANDEX_OAUTH_REDIRECT_URI", s.OAuth.RedirectURI != ""},
		{"YANDEX_TOKEN_KEY", get("YANDEX_TOKEN_KEY") != ""},
	} {
		if !item.set {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 && (required || len(missing) < 4) {
		reason := "the Yandex connect step is partly configured"
		if required {
			reason = "REQUIRE_YANDEX_CONNECT=true needs the Yandex connect step configured"
		}
		errs = append(errs, fmt.Errorf("%s; missing %s", reason, strings.Join(missing, ", ")))
	}
	return s, errors.Join(errs...)
}

func parseBool(raw string) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return false, nil
	}
	return strconv.ParseBool(strings.TrimSpace(raw))
}

func orDefault(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}
