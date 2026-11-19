package yandex

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// sealVersion prefixes every sealed value. The format is shared with recruitment-backup
// (internal/secret), which opens these tokens with BACKUP_ENCRYPTION_KEY: changing it
// would stop the backup service from reading any stored token.
const sealVersion = "v1:"

var ErrMalformedSealedValue = errors.New("sealed value is malformed or was sealed with a different key")

// Sealer encrypts candidate OAuth tokens with AES-256-GCM. A sealed value is "v1:"
// followed by unpadded standard base64 of the random nonce and the ciphertext.
type Sealer struct {
	aead cipher.AEAD
}

func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(plain string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	out := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return sealVersion + base64.RawStdEncoding.EncodeToString(out), nil
}

func (s *Sealer) Open(sealed string) (string, error) {
	body, ok := strings.CutPrefix(sealed, sealVersion)
	if !ok {
		return "", ErrMalformedSealedValue
	}
	raw, err := base64.RawStdEncoding.DecodeString(body)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", ErrMalformedSealedValue
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", ErrMalformedSealedValue
	}
	return string(plain), nil
}
