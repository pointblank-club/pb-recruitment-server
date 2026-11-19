package yandex

import (
	"strings"
	"testing"
)

const testTokenKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // base64 of 0123456789abcdef0123456789abcdef

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func complete(extra map[string]string) map[string]string {
	values := map[string]string{
		"YANDEX_OAUTH_CLIENT_ID":     "client",
		"YANDEX_OAUTH_CLIENT_SECRET": "secret",
		"YANDEX_OAUTH_REDIRECT_URI":  "https://recruitment.pointblank.club/yandex/callback",
		"YANDEX_TOKEN_KEY":           testTokenKey,
	}
	for k, v := range extra {
		values[k] = v
	}
	return values
}

func TestLoadSettings(t *testing.T) {
	s, err := LoadSettings(env(nil))
	if err != nil || s.Enabled() || s.Required {
		t.Fatalf("unset: %+v, %v", s, err)
	}

	s, err = LoadSettings(env(complete(map[string]string{"REQUIRE_YANDEX_CONNECT": "true"})))
	if err != nil || !s.Enabled() || !s.Required || string(s.TokenKey) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("complete: %+v, %v", s, err)
	}
	if s.OAuth.AuthorizeURL != DefaultAuthorizeURL || s.OAuth.TokenURL != DefaultTokenURL || s.OAuth.InfoURL != DefaultInfoURL {
		t.Fatalf("defaults: %+v", s.OAuth)
	}
	if !ConnectRequired(env(map[string]string{"REQUIRE_YANDEX_CONNECT": "true"})) || ConnectRequired(env(nil)) ||
		ConnectRequired(env(map[string]string{"REQUIRE_YANDEX_CONNECT": "maybe"})) {
		t.Fatal("ConnectRequired")
	}

	for _, tc := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"required but unconfigured", map[string]string{"REQUIRE_YANDEX_CONNECT": "true"}, "YANDEX_OAUTH_CLIENT_ID"},
		{"partial", map[string]string{"YANDEX_OAUTH_CLIENT_ID": "client"}, "YANDEX_TOKEN_KEY"},
		{"short key", complete(map[string]string{"YANDEX_TOKEN_KEY": "c2hvcnQ="}), "32 bytes"},
		{"bad flag", complete(map[string]string{"REQUIRE_YANDEX_CONNECT": "maybe"}), "true or false"},
		{"relative redirect", complete(map[string]string{"YANDEX_OAUTH_REDIRECT_URI": "/yandex/callback"}), "absolute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadSettings(env(tc.values)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}
