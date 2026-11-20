package yandex

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// StateTTL bounds how long a started connection can be completed. It is generous because a
// candidate may create a Yandex account (with phone verification) during the flow; the
// authorization code itself still expires ten minutes after Yandex issues it.
const StateTTL = time.Hour

var ErrInvalidState = errors.New("invalid or expired oauth state")

// State is carried through Yandex in the OAuth state parameter. It is signed, not
// encrypted, so it must hold nothing secret. Binding it to the Firebase user stops anyone
// from completing a connection, with their own Yandex code, on someone else's account.
type State struct {
	UserID      string `json:"u"`
	ContestID   string `json:"c,omitempty"`
	ConsentedAt int64  `json:"a"`
	ExpiresAt   int64  `json:"e"`
	Nonce       string `json:"n"`
}

// StateKey derives the state signing key from the token key, so one secret covers both.
func StateKey(tokenKey []byte) []byte {
	mac := hmac.New(sha256.New, tokenKey)
	mac.Write([]byte("pb-recruitment yandex oauth state v1"))
	return mac.Sum(nil)
}

func SignState(key []byte, s State) (string, error) {
	if s.Nonce == "" {
		nonce := make([]byte, 8)
		if _, err := rand.Read(nonce); err != nil {
			return "", err
		}
		s.Nonce = hex.EncodeToString(nonce)
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(stateMAC(key, body)), nil
}

func VerifyState(key []byte, token string, now time.Time) (State, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" {
		return State{}, ErrInvalidState
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, stateMAC(key, body)) {
		return State{}, ErrInvalidState
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return State{}, ErrInvalidState
	}
	var s State
	if err := json.Unmarshal(payload, &s); err != nil || s.UserID == "" || s.ConsentedAt <= 0 {
		return State{}, ErrInvalidState
	}
	if now.UnixMilli() > s.ExpiresAt {
		return State{}, ErrInvalidState
	}
	return s, nil
}

func stateMAC(key []byte, body string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return mac.Sum(nil)
}
