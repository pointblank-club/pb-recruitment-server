package yandex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func fakeYandexID(t *testing.T, token func(url.Values) (int, string), info func(*http.Request) (int, string)) OAuthConfig {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("token request %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		_ = r.ParseForm()
		status, body := token(r.PostForm)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		status, body := info(r)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return OAuthConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURI: "https://site.example/yandex/callback",
		AuthorizeURL: srv.URL + "/authorize", TokenURL: srv.URL + "/token", InfoURL: srv.URL + "/info", Timeout: time.Second,
	}
}

func TestAuthorizeURL(t *testing.T) {
	c := NewClient(OAuthConfig{ClientID: "client", RedirectURI: "https://site.example/yandex/callback", AuthorizeURL: DefaultAuthorizeURL})
	u, err := url.Parse(c.AuthorizeURL("signed.state"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != DefaultAuthorizeURL || q.Get("response_type") != "code" || q.Get("client_id") != "client" ||
		q.Get("redirect_uri") != "https://site.example/yandex/callback" || q.Get("state") != "signed.state" || q.Get("force_confirm") != "yes" {
		t.Fatalf("authorize url %s", u)
	}
	if q.Has("client_secret") {
		t.Fatal("the client secret must never reach the browser")
	}
}

func TestExchangeCodeAndUserInfo(t *testing.T) {
	cfg := fakeYandexID(t, func(form url.Values) (int, string) {
		if form.Get("grant_type") != "authorization_code" || form.Get("code") != "1234567" ||
			form.Get("client_id") != "client" || form.Get("client_secret") != "secret" {
			return http.StatusBadRequest, `{"error":"invalid_request"}`
		}
		return http.StatusOK, `{"token_type":"bearer","access_token":"y0_access","refresh_token":"1:refresh","expires_in":31536000}`
	}, func(r *http.Request) (int, string) {
		if r.Header.Get("Authorization") != "OAuth y0_access" || r.URL.Query().Get("format") != "json" {
			return http.StatusUnauthorized, `{}`
		}
		return http.StatusOK, `{"id":"1130000000000001","login":"asha","default_email":"asha@yandex.ru","emails":["asha@yandex.ru"]}`
	})
	c := NewClient(cfg)
	tok, err := c.ExchangeCode(context.Background(), "1234567")
	if err != nil || tok.AccessToken != "y0_access" || tok.RefreshToken != "1:refresh" {
		t.Fatalf("ExchangeCode = %+v, %v", tok, err)
	}
	now := time.UnixMilli(1_000)
	if got := tok.ExpiresAt(now); got != 1_000+31536000*1000 {
		t.Fatalf("expiry %d must be Unix milliseconds", got)
	}
	info, err := c.UserInfo(context.Background(), tok.AccessToken)
	if err != nil || info.ID != 1130000000000001 || info.Login != "asha" || info.Email != "asha@yandex.ru" {
		t.Fatalf("UserInfo = %+v, %v", info, err)
	}
}

func TestExchangeCodeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		status      int
		invalidCode bool
	}{
		{"expired code", `{"error":"invalid_grant","error_description":"Code has expired"}`, http.StatusBadRequest, true},
		{"bad code", `{"error":"bad_verification_code"}`, http.StatusBadRequest, true},
		{"wrong secret", `{"error":"invalid_client"}`, http.StatusUnauthorized, false},
		{"outage", `oops`, http.StatusBadGateway, false},
		{"no token", `{"token_type":"bearer"}`, http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fakeYandexID(t, func(url.Values) (int, string) { return tc.status, tc.body }, nil)
			_, err := NewClient(cfg).ExchangeCode(context.Background(), "code")
			if err == nil || errors.Is(err, ErrInvalidCode) != tc.invalidCode {
				t.Fatalf("err = %v, want invalid code %v", err, tc.invalidCode)
			}
		})
	}
}

func TestUserInfoWithoutLoginOrDefaultEmail(t *testing.T) {
	cfg := fakeYandexID(t, nil, func(*http.Request) (int, string) {
		return http.StatusOK, `{"id":"42","emails":["first@gmail.com"]}`
	})
	info, err := NewClient(cfg).UserInfo(context.Background(), "token")
	if err != nil || info.ID != 42 || info.Login != "" || info.Email != "first@gmail.com" {
		t.Fatalf("UserInfo = %+v, %v", info, err)
	}

	for _, body := range []string{`{"login":"x"}`, `{"id":"not-a-number"}`, `{"id":"0"}`, `not json`} {
		cfg := fakeYandexID(t, nil, func(*http.Request) (int, string) { return http.StatusOK, body })
		if _, err := NewClient(cfg).UserInfo(context.Background(), "token"); err == nil {
			t.Fatalf("UserInfo accepted %s", body)
		}
	}
}
