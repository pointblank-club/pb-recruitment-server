package services

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestJudgeOutputTextDecodesBase64(t *testing.T) {
	// Judge0 1.13.1 callbacks always Base64-encode output fields.
	plain := "error: expected ';' after expression"
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	raw, _ := json.Marshal(encoded)
	got := judgeOutputText(json.RawMessage(raw))
	if got != plain {
		t.Errorf("judgeOutputText(base64) = %q, want %q", got, plain)
	}
}

func TestJudgeOutputTextDiscardsMalformedBase64(t *testing.T) {
	raw, _ := json.Marshal("this is not base64!!")
	got := judgeOutputText(json.RawMessage(raw))
	if got != "" {
		t.Errorf("judgeOutputText(malformed base64) = %q, want empty", got)
	}
}

func TestJudgeOutputTextNullAndEmpty(t *testing.T) {
	for _, input := range []string{"null", ""} {
		got := judgeOutputText(json.RawMessage(input))
		if got != "" {
			t.Errorf("judgeOutputText(%q) = %q, want empty", input, got)
		}
	}
}

func TestDecodeJudgeOutputValidation(t *testing.T) {
	for _, raw := range []string{"", `42`, `{}`, `"%%%invalid"`} {
		if _, err := decodeJudgeOutput(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted malformed output %q", raw)
		}
	}
	for _, raw := range []string{`null`, `""`} {
		if text, err := decodeJudgeOutput(json.RawMessage(raw)); err != nil || text != "" {
			t.Errorf("empty output %q: %q %v", raw, text, err)
		}
	}
}

func TestSanitizeJudgeOutputRemovesNUL(t *testing.T) {
	// NUL bytes in stderr cause PostgreSQL SQLSTATE 22021 on TEXT columns.
	input := "error\x00: segfault\x00"
	got := sanitizeJudgeOutput(input)
	if strings.Contains(got, "\x00") {
		t.Error("NUL byte survived sanitization")
	}
	if got != "error: segfault" {
		t.Errorf("sanitizeJudgeOutput = %q", got)
	}
}

func TestSanitizeJudgeOutputReplacesInvalidUTF8(t *testing.T) {
	// Binary stderr: 0x80 is not valid UTF-8 start byte.
	input := "error: \x80\xfe"
	got := sanitizeJudgeOutput(input)
	if !strings.Contains(got, "\ufffd") {
		t.Error("invalid UTF-8 not replaced with U+FFFD")
	}
	if strings.Contains(got, "\x80") || strings.Contains(got, "\xfe") {
		t.Error("raw invalid bytes survived sanitization")
	}
}

func TestJudgeOutputTextBase64WithNUL(t *testing.T) {
	// End-to-end: Base64-encoded binary stderr containing NUL.
	// This is the exact scenario where verdict persistence fails.
	binary := "Segmentation fault\x00\x80"
	encoded := base64.StdEncoding.EncodeToString([]byte(binary))
	raw, _ := json.Marshal(encoded)
	got := judgeOutputText(json.RawMessage(raw))
	if strings.Contains(got, "\x00") {
		t.Error("NUL byte survived full pipeline")
	}
	if !strings.Contains(got, "Segmentation fault") {
		t.Errorf("readable part lost: %q", got)
	}
}

func TestJudgeOutputTextTruncation(t *testing.T) {
	// Verify the 4 KiB limit applies after decoding, not before.
	long := strings.Repeat("A", 8192)
	raw, _ := json.Marshal(long)
	got := judgeOutputText(json.RawMessage(raw))
	if len(got) > judgeOutputLimit {
		t.Errorf("output length %d exceeds limit %d", len(got), judgeOutputLimit)
	}
}

func TestJudgeOutputTextBase64TruncationOrder(t *testing.T) {
	// A 4 KiB Base64-encoded string decodes to ~3 KiB of output.
	// Truncation must happen after decoding so we don't cut mid-encoding.
	plain := strings.Repeat("X", 3000)
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	raw, _ := json.Marshal(encoded)
	got := judgeOutputText(json.RawMessage(raw))
	if got != plain {
		t.Errorf("decoded output was truncated prematurely: len=%d, want %d", len(got), len(plain))
	}
}
