package yandex

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSealRoundTrip(t *testing.T) {
	s, err := NewSealer(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal("y0_AgAAAAB-token")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, "token") {
		t.Fatalf("sealed value leaks plaintext or lacks version: %q", sealed)
	}
	if again, _ := s.Seal("y0_AgAAAAB-token"); again == sealed {
		t.Fatal("two seals of the same token must differ")
	}
	got, err := s.Open(sealed)
	if err != nil || got != "y0_AgAAAAB-token" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

// recruitment-backup opens tokens sealed here (internal/secret TestOpensValueSealedByServer
// pins the same vector). If this fails, the backup can no longer read candidate tokens.
func TestSealFormatMatchesRecruitmentBackup(t *testing.T) {
	s, err := NewSealer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Open("v1:AqYRP+1+hH/q5B3mER6Wx4/BAH46d9DhtzSu5WuWefcvaes25FLdrHp1PVcNfyOCrq+j4R4")
	if err != nil || got != "y0_candidate-access-token" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestOpenRejectsWrongKeyAndGarbage(t *testing.T) {
	s1, _ := NewSealer(bytes.Repeat([]byte{1}, 32))
	s2, _ := NewSealer(bytes.Repeat([]byte{2}, 32))
	sealed, _ := s1.Seal("secret")
	if _, err := s2.Open(sealed); !errors.Is(err, ErrMalformedSealedValue) {
		t.Fatalf("wrong key: %v", err)
	}
	for _, bad := range []string{"", "secret", "v1:", "v1:!!!", "v2:abcd"} {
		if _, err := s1.Open(bad); !errors.Is(err, ErrMalformedSealedValue) {
			t.Fatalf("Open(%q) = %v", bad, err)
		}
	}
	if _, err := NewSealer(make([]byte, 16)); err == nil {
		t.Fatal("a 16 byte key must be rejected")
	}
}
