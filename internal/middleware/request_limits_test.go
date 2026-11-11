package middleware

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"github.com/labstack/echo/v4"
)

type recordingLimiter struct {
	keys  []string
	retry int
	err   error
}

func (l *recordingLimiter) Allow(_ context.Context, key string, _, _ int) (int, error) {
	l.keys = append(l.keys, key)
	return l.retry, l.err
}

func TestRequestBudgetsAndFailure(t *testing.T) {
	for _, tc := range []struct {
		retry, want int
		err         error
	}{{0, 204, nil}, {5, 429, nil}, {0, 503, errors.New("database unavailable")}} {
		e := echo.New()
		l := &recordingLimiter{retry: tc.retry, err: tc.err}
		e.GET("/read", func(c echo.Context) error { return c.NoContent(204) }, func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error { c.Set(common.AUTH_USER_ID, c.QueryParam("user")); return next(c) }
		}, LimitRequests(l, "read", 180, 60))
		for _, uid := range []string{"alice", "bob"} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/read?user="+uid, nil)
			r.RemoteAddr = "192.0.2.1:1234"
			e.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatal(w.Code, tc.want)
			}
			if tc.retry > 0 && w.Header().Get("Retry-After") != "5" {
				t.Fatal(w.Header())
			}
		}
		if len(l.keys) != 2 || l.keys[0] != "read:user:alice" || l.keys[1] != "read:user:bob" {
			t.Fatal(l.keys)
		}
	}
}

func TestRequestIPTrust(t *testing.T) {
	for _, tc := range []struct{ trusted, remote, want string }{{"", "192.0.2.1:1234", "192.0.2.1"}, {"127.0.0.1/32", "192.0.2.1:1234", "192.0.2.1"}, {"127.0.0.1/32", "127.0.0.1:1234", "203.0.113.7"}} {
		t.Setenv("TRUSTED_PROXY_CIDRS", tc.trusted)
		e := echo.New()
		ConfigureRequestBounds(e)
		e.GET("/ip", func(c echo.Context) error { return c.String(200, c.RealIP()) })
		r := httptest.NewRequest("GET", "/ip", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-For", "198.51.100.4, 203.0.113.7")
		r.Header.Set("X-Real-IP", "198.51.100.5")
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Body.String() != tc.want {
			t.Fatal(w.Body.String(), tc.want)
		}
	}
}

func TestBodyBoundsBeforeBinding(t *testing.T) {
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	for _, length := range []int64{-1, 2 << 20} {
		e := echo.New()
		ConfigureRequestBounds(e)
		e.POST("/bind", func(c echo.Context) error { return c.NoContent(204) }, ValidateRequest(new(struct {
			Value string `json:"value"`
		})))
		r := httptest.NewRequest("POST", "/bind", strings.NewReader(`{"value":"`+strings.Repeat("x", 2<<20)+`"}`))
		r.Header.Set("Content-Type", "application/json")
		r.ContentLength = length
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 413 {
			t.Fatalf("length=%d: status=%d %s", length, w.Code, w.Body.String())
		}
	}
	e := echo.New()
	ConfigureRequestBounds(e)
	for _, path := range []string{"/admin/problem", "/internal/judge0/callback/:execution_id"} {
		e.POST(path, func(c echo.Context) error {
			_, err := io.Copy(io.Discard, c.Request().Body)
			if err != nil {
				return err
			}
			return c.NoContent(204)
		})
	}
	for _, path := range []string{"/admin/problem", "/internal/judge0/callback/test"} {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(strings.Repeat("x", 2<<20))))
		if w.Code != 204 {
			t.Fatal(path, w.Code)
		}
	}
	if e.Server.ReadHeaderTimeout == 0 || e.Server.ReadTimeout == 0 || e.Server.WriteTimeout == 0 || e.Server.IdleTimeout == 0 {
		t.Fatal("missing server timeout")
	}
}

func TestIngressCannotBeSkippedWithHeaders(t *testing.T) {
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	now := time.Unix(1700000000, 0)
	e := echo.New()
	ConfigureRequestBounds(e)
	limiter := newUserRateLimiter(1, 1, func() time.Time { return now })
	e.Use(newIngressRateLimitWithLimiter(limiter))
	e.GET("/users/profile", func(c echo.Context) error { return c.NoContent(401) })
	e.PUT("/internal/judge0/callback/:execution_id", func(c echo.Context) error { return c.NoContent(401) })
	e.GET("/health", func(c echo.Context) error { return c.NoContent(200) })
	for _, tc := range []struct {
		method, path, remote, forwarded string
		want                            int
	}{
		{"GET", "/users/profile", "192.0.2.1:1234", "203.0.113.1", 401},
		{"GET", "/users/profile", "192.0.2.1:1234", "203.0.113.2", 429},
		{"PUT", "/internal/judge0/callback/test", "192.0.2.1:1234", "", 401},
		{"GET", "/health", "192.0.2.1:1234", "", 200},
		{"OPTIONS", "/users/profile", "192.0.2.1:1234", "", 204},
		{"GET", "/users/profile", "192.0.2.2:1234", "", 401},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		e.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.path, w.Code, tc.want)
		}
		if w.Code == 429 && w.Header().Get("Retry-After") != "1" {
			t.Fatal("missing retry delay", w.Header())
		}
	}
	now = now.Add(time.Second)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users/profile", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	e.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("ingress budget did not refill", w.Code)
	}
}

func TestIngressConcurrentAdmission(t *testing.T) {
	now := time.Unix(1700000000, 0)
	e := echo.New()
	e.Use(newIngressRateLimitWithLimiter(newUserRateLimiter(20, 1, func() time.Time { return now })))
	e.GET("/read", func(c echo.Context) error { return c.NoContent(401) })
	var admitted, limited atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest("GET", "/read", nil))
			switch w.Code {
			case 401:
				admitted.Add(1)
			case 429:
				limited.Add(1)
			default:
				t.Errorf("unexpected status %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 20 || limited.Load() != 80 {
		t.Fatal("non-atomic ingress budget", admitted.Load(), limited.Load())
	}
}

func TestProfileAndProblemBounds(t *testing.T) {
	validator := NewValidator()
	if err := validator.Validate(dto.UpdateUserProfileRequest{Name: strings.Repeat("n", 201), MobileNumber: "1234567890", Department: "CS"}); err == nil {
		t.Fatal("oversized name accepted")
	}
	base := dto.CreateProblemRequest{Name: "P", Description: "D", Score: 1, Type: models.Code, Testcases: make([]dto.CreateTestCaseRequest, 100)}
	if err := validator.Validate(base); err != nil {
		t.Fatal("100 blank testcases should be valid", err)
	}
	base.Testcases = append(base.Testcases, dto.CreateTestCaseRequest{})
	if err := validator.Validate(base); err == nil {
		t.Fatal("101 testcases accepted")
	}
	base.Testcases = []dto.CreateTestCaseRequest{{Input: strings.Repeat("i", (1<<20)+1)}}
	if err := validator.Validate(base); err == nil {
		t.Fatal("oversized testcase accepted")
	}
}
