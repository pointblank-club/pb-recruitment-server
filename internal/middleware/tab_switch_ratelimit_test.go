package middleware

import (
	"app/internal/common"
	"app/internal/models/dto"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	echomdw "github.com/labstack/echo/v4/middleware"
)

func TestTabSwitchRateLimiter(t *testing.T) {
	now := time.Unix(1700000000, 0)
	limiter := newUserRateLimiter(2, 1, func() time.Time { return now })
	mw := newTabSwitchRateLimitWithLimiter(limiter)

	run := func(userID string) int {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(common.AUTH_USER_ID, userID)
		err := mw(func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})(c)
		if err != nil {
			t.Fatalf("unexpected handler error: %v", err)
		}
		return rec.Code
	}

	if code := run("u1"); code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", code)
	}
	if code := run("u1"); code != http.StatusOK {
		t.Fatalf("second request (burst 2): got %d, want 200", code)
	}
	if code := run("u1"); code != http.StatusTooManyRequests {
		t.Fatalf("third immediate request: got %d, want 429", code)
	}
	// Separate user has an independent bucket.
	if code := run("u2"); code != http.StatusOK {
		t.Fatalf("other user u2: got %d, want 200", code)
	}
	// Advance clock by 1 second -> 1 token refilled.
	now = now.Add(time.Second)
	if code := run("u1"); code != http.StatusOK {
		t.Fatalf("after 1s refill: got %d, want 200", code)
	}
}

func TestTabSwitchRateLimiterFullMapPreservesExhaustedOldestBucket(t *testing.T) {
	now := time.Unix(1700000000, 0)
	limiter := newUserRateLimiter(1, 0.1, func() time.Time { return now })
	limiter.maxBuckets = 2
	mw := newTabSwitchRateLimitWithLimiter(limiter)

	run := func(userID string) int {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(common.AUTH_USER_ID, userID)
		err := mw(func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})(c)
		if err != nil {
			t.Fatalf("unexpected handler error: %v", err)
		}
		return rec.Code
	}

	// Exhaust u1 (oldest bucket) and fill map to capacity with u2.
	if code := run("u1"); code != http.StatusOK {
		t.Fatalf("u1 initial: got %d, want 200", code)
	}
	now = now.Add(10 * time.Millisecond)
	if code := run("u2"); code != http.StatusOK {
		t.Fatalf("u2 initial: got %d, want 200", code)
	}

	// u1 is already present and happens to be the oldest bucket when map is full;
	// hitting again within refill window must stay rate-limited (429), not evict itself.
	now = now.Add(10 * time.Millisecond)
	if code := run("u1"); code != http.StatusTooManyRequests {
		t.Fatalf("exhausted oldest bucket u1 on full map: got %d, want 429", code)
	}
	if _, exists := limiter.buckets["u1"]; !exists {
		t.Fatal("u1 bucket should still be present")
	}

	// Absent key u3 arriving on full map evicts oldest bucket (u2) and stays capped at maxBuckets.
	now = now.Add(10 * time.Millisecond)
	if code := run("u3"); code != http.StatusOK {
		t.Fatalf("new user u3: got %d, want 200", code)
	}
	if len(limiter.buckets) != 2 {
		t.Fatalf("bucket count = %d, want 2", len(limiter.buckets))
	}
}

func TestTabSwitchValidationAndBodyLimit(t *testing.T) {
	e := echo.New()
	e.POST("/contests/:id/tab-switch", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, echomdw.BodyLimit("4K"), ValidateRequest(new(dto.TabSwitchRequest)))

	validUUID := "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"valid gen 0 count 1", `{"observation_id":"` + validUUID + `","generation":0,"count":1}`, http.StatusOK},
		{"empty object", `{}`, http.StatusBadRequest},
		{"zero count", `{"observation_id":"` + validUUID + `","generation":0,"count":0}`, http.StatusBadRequest},
		{"negative count", `{"observation_id":"` + validUUID + `","generation":0,"count":-1}`, http.StatusBadRequest},
		{"int32 max count", `{"observation_id":"` + validUUID + `","generation":0,"count":2147483647}`, http.StatusBadRequest},
		{"int32 overflow count", `{"observation_id":"` + validUUID + `","generation":0,"count":2147483648}`, http.StatusBadRequest},
		{"negative generation", `{"observation_id":"` + validUUID + `","generation":-1,"count":1}`, http.StatusBadRequest},
		{"invalid uuid", `{"observation_id":"not-a-uuid","generation":0,"count":1}`, http.StatusBadRequest},
		{"oversized payload >4K", `{"observation_id":"` + validUUID + `","generation":0,"count":1,"pad":"` + strings.Repeat("a", 5000) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/contests/c1/tab-switch", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s: got HTTP %d, want %d (body: %s)", tc.name, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
