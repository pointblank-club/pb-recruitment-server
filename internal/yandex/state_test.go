package yandex

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStateRoundTripAndTampering(t *testing.T) {
	key := StateKey([]byte("0123456789abcdef0123456789abcdef"))
	now := time.UnixMilli(1_800_000_000_000)
	token, err := SignState(key, State{UserID: "user-1", ContestID: "c-1", ConsentedAt: now.UnixMilli(), ExpiresAt: now.Add(StateTTL).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyState(key, token, now.Add(30*time.Minute))
	if err != nil || got.UserID != "user-1" || got.ContestID != "c-1" || got.ConsentedAt != now.UnixMilli() || got.Nonce == "" {
		t.Fatalf("VerifyState = %+v, %v", got, err)
	}

	if _, err := VerifyState(key, token, now.Add(StateTTL+time.Millisecond)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expired state accepted: %v", err)
	}
	if _, err := VerifyState(StateKey([]byte("another key")), token, now); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("state signed with another key accepted: %v", err)
	}
	body, sig, _ := strings.Cut(token, ".")
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"u":"attacker","a":1,"e":9999999999999,"n":"x"}`))
	for _, bad := range []string{"", ".", body, body + ".", forged + "." + sig, body + "." + sig + "x"} {
		if _, err := VerifyState(key, bad, now); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("VerifyState(%q) = %v", bad, err)
		}
	}
}

func TestStateKeyIsSeparateFromTokenKey(t *testing.T) {
	tokenKey := []byte("0123456789abcdef0123456789abcdef")
	if string(StateKey(tokenKey)) == string(tokenKey) {
		t.Fatal("the state key must be derived, not the token key itself")
	}
}
