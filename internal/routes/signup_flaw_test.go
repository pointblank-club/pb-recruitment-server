package routes

import (
	"app/internal/controllers"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestFlaw_RateLimitedIPDoesNotConsumeCapacitySlots verifies that an IP blocked by signup-ip
// does NOT consume worker capacity slots, allowing other IPs to proceed without getting 429 busy.
func TestFlaw_RateLimitedIPDoesNotConsumeCapacitySlots(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)

	// Simulate bad IP that has exhausted its 1200 hourly limit
	badIP := "198.51.100.1"
	limiter.hits["signup-ip:ip:"+badIP] = 1200

	// Bad IP sends 10 requests; all must return 429 from limiter
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(validSignupAdmissionBody))
		req.RemoteAddr = badIP + ":1234"
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 for rate-limited IP, got %d", rec.Code)
		}
		// Must be rate limiter 429, NOT "signup capacity busy"
		if strings.Contains(rec.Body.String(), "signup capacity busy") {
			t.Fatalf("rate-limited IP consumed capacity slot: %s", rec.Body.String())
		}
	}

	// Legitimate IP should be able to get through capacity immediately
	goodIP := "198.51.100.2"
	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(validSignupAdmissionBody))
	req.RemoteAddr = goodIP + ":1234"
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Passes capacity, reaches controller check (returns 503 from nil service, not 429)
	if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusCreated {
		t.Fatalf("expected good IP to pass capacity, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestFlaw_FleetLimitReleaseDoesNotLeakSlots verifies that when the 100/10s fleet limit triggers,
// the capacity slot held by the request is released immediately and not leaked.
func TestFlaw_FleetLimitReleaseDoesNotLeakSlots(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)

	// Set fleet limit to near capacity (100 hits out of 100)
	limiter.hits["signup-fleet:fleet"] = 100

	// 50 requests arrive from different IPs. Fleet limiter returns 429 for all of them.
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(validSignupAdmissionBody))
		req.RemoteAddr = "192.0.2.50:1234"
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 from fleet limiter, got %d", rec.Code)
		}
	}

	// Now reset fleet limit
	limiter.hits["signup-fleet:fleet"] = 0

	// A new request arrives. If any slot was leaked, capacity would be full/busy.
	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(validSignupAdmissionBody))
	req.RemoteAddr = "192.0.2.51:1234"
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusCreated {
		t.Fatalf("expected 503 or 201 from controller, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestFlaw_ClientDisconnectReleasesCapacitySlot verifies that if a client disconnects
// while waiting in queue for an active worker slot, the slot in admitted channel is released.
func TestFlaw_ClientDisconnectReleasesCapacitySlot(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)

	// Create a context that is already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(validSignupAdmissionBody))
	req = req.WithContext(ctx)
	req.RemoteAddr = "192.0.2.60:1234"
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Immediate next normal request must succeed
	req2 := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(validSignupAdmissionBody))
	req2.RemoteAddr = "192.0.2.61:1234"
	req2.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusServiceUnavailable && rec2.Code != http.StatusCreated {
		t.Fatalf("expected 503 or 201, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

// TestF04_InvalidUSNDoesNotConsumeBudget verifies that invalid USN is rejected for free
// by ValidateUserInput before reaching rate limit budgets (Fix F-04).
func TestF04_InvalidUSNDoesNotConsumeBudget(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)

	ip := "192.0.2.77"
	// Request has valid JSON & struct tags, but USN is "INVALID_USN"
	junkPayload := `{"name":"Test","email":"test@example.com","password":"password123","usn":"INVALID_USN","mobile_number":"1234567890","current_year":2,"department":"CS"}`

	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(junkPayload))
	req.RemoteAddr = ip + ":1234"
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	// Verify that limiter was NOT charged!
	if len(limiter.hits) != 0 {
		t.Fatalf("expected 0 limiter hits for invalid USN, got %v", limiter.hits)
	}
}

// TestFlaw_HighConcurrencyBurst checks behavior when 150 requests arrive concurrently.
func TestFlaw_HighConcurrencyBurst(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)

	var (
		wg        sync.WaitGroup
		admitted  atomic.Int32
		status429 atomic.Int32
		other     atomic.Int32
	)

	totalRequests := 150
	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(validSignupAdmissionBody))
			req.RemoteAddr = "192.0.2.88:1234"
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			switch rec.Code {
			case http.StatusServiceUnavailable, http.StatusCreated:
				admitted.Add(1)
			case http.StatusTooManyRequests:
				status429.Add(1)
			default:
				other.Add(1)
			}
		}(i)
	}
	wg.Wait()

	t.Logf("High concurrency results: admitted=%d, 429s=%d, other=%d",
		admitted.Load(), status429.Load(), other.Load())

	if other.Load() > 0 {
		t.Fatalf("unexpected statuses received: %d", other.Load())
	}
	if admitted.Load() == 0 {
		t.Fatalf("expected at least some requests to pass, got 0")
	}
}
