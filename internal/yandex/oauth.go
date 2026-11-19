package yandex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidCode means Yandex refused the authorization code: it expired, was already
// used, or was never valid. The candidate has to start the connection again.
var ErrInvalidCode = errors.New("yandex rejected the authorization code")

// APIError is any other unsuccessful Yandex ID response.
type APIError struct {
	Op          string
	Status      int
	Code        string
	Description string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("yandex %s: HTTP %d", e.Op, e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return msg
}

type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// ExpiresAt is the Unix millisecond expiry, or 0 when Yandex gave none. recruitment-backup
// compares token_expires_at in milliseconds when deciding what to refresh.
func (t Token) ExpiresAt(now time.Time) int64 {
	if t.ExpiresIn <= 0 {
		return 0
	}
	return now.Add(time.Duration(t.ExpiresIn) * time.Second).UnixMilli()
}

// UserInfo identifies the candidate's Yandex account. Login can be empty, for example for
// some accounts created through Google sign-in, so the uid is the identity everywhere.
type UserInfo struct {
	ID    int64
	Login string
	Email string
}

type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	AuthorizeURL string
	TokenURL     string
	InfoURL      string
	Timeout      time.Duration
}

type Client struct {
	cfg  OAuthConfig
	http *http.Client
}

func NewClient(cfg OAuthConfig) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: timeout}}
}

// AuthorizeURL is where the website sends the candidate. force_confirm makes Yandex ask
// again even if access was granted before, which also lets the candidate switch accounts.
// Scopes come from the app registration (contest:submit, login:info, login:email).
func (c *Client) AuthorizeURL(state string) string {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {c.cfg.ClientID},
		"redirect_uri":  {c.cfg.RedirectURI},
		"state":         {state},
		"force_confirm": {"yes"},
	}
	return c.cfg.AuthorizeURL + "?" + q.Encode()
}

func (c *Client) ExchangeCode(ctx context.Context, code string) (Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("yandex token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	body, status, err := c.do(req)
	if err != nil {
		return Token{}, fmt.Errorf("yandex token: %w", err)
	}
	if status != http.StatusOK {
		apiErr := decodeError("token", status, body)
		if status == http.StatusBadRequest && (apiErr.Code == "invalid_grant" || apiErr.Code == "bad_verification_code") {
			return Token{}, fmt.Errorf("%w: %s", ErrInvalidCode, apiErr.Description)
		}
		return Token{}, apiErr
	}
	var t Token
	if err := json.Unmarshal(body, &t); err != nil {
		return Token{}, fmt.Errorf("yandex token: decode response: %w", err)
	}
	if t.AccessToken == "" {
		return Token{}, &APIError{Op: "token", Status: status, Code: "invalid_response", Description: "missing access_token"}
	}
	return t, nil
}

func (c *Client) UserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	endpoint := c.cfg.InfoURL
	if strings.Contains(endpoint, "?") {
		endpoint += "&format=json"
	} else {
		endpoint += "?format=json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return UserInfo{}, fmt.Errorf("yandex info: %w", err)
	}
	req.Header.Set("Authorization", "OAuth "+accessToken)
	req.Header.Set("Accept", "application/json")
	body, status, err := c.do(req)
	if err != nil {
		return UserInfo{}, fmt.Errorf("yandex info: %w", err)
	}
	if status != http.StatusOK {
		return UserInfo{}, decodeError("info", status, body)
	}
	var raw struct {
		ID           string   `json:"id"`
		Login        string   `json:"login"`
		DefaultEmail string   `json:"default_email"`
		Emails       []string `json:"emails"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return UserInfo{}, fmt.Errorf("yandex info: decode response: %w", err)
	}
	id, err := strconv.ParseInt(strings.TrimSpace(raw.ID), 10, 64)
	if err != nil || id <= 0 {
		return UserInfo{}, &APIError{Op: "info", Status: status, Code: "invalid_response", Description: "missing or invalid uid"}
	}
	info := UserInfo{ID: id, Login: strings.TrimSpace(raw.Login), Email: strings.TrimSpace(raw.DefaultEmail)}
	if info.Email == "" && len(raw.Emails) > 0 {
		info.Email = strings.TrimSpace(raw.Emails[0])
	}
	return info, nil
}

func (c *Client) do(req *http.Request) ([]byte, int, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, 0, fmt.Errorf("read body: %w", err)
	}
	return body, resp.StatusCode, nil
}

func decodeError(op string, status int, body []byte) *APIError {
	var e struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &e)
	return &APIError{Op: op, Status: status, Code: e.Error, Description: e.Description}
}
