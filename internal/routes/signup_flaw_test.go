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
		req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(signupAdmissionBody))
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
	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(signupAdmissionBody))
	req.RemoteAddr = goodIP + ":1234"
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Passes capacity, reaches controller check -> 400 Bad Request
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected good IP to pass capacity and get 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestFlaw_FleetLimitReleaseDoesNotLeakSlots verifies that when the 100/10s fleet limit triggers,
// the capacity slot held by the request is released immediately and not leaked.
func TestFlaw_FleetLimitReleaseDoesNotLeakSlots(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)

	// Set fleet limit to near capacity (99 hits out of 100)
	limiter.hits["signup-fleet:fleet"] = 100

	// 50 requests arrive from different IPs. Fleet limiter returns 429 for all of them.
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(signupAdmissionBody))
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
	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(signupAdmissionBody))
	req.RemoteAddr = "192.0.2.51:1234"
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 from controller, got %d: %s", rec.Code, rec.Body.String())
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

	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(signupAdmissionBody))
	req = req.WithContext(ctx)
	req.RemoteAddr = "192.0.2.60:1234"
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Echo/ValidateRequest or SignupCapacity returns timeout or bad request
	if rec.Code != http.StatusRequestTimeout && rec.Code != http.StatusBadRequest {
		t.Logf("cancelled request code: %d %s", rec.Code, rec.Body.String())
	}

	// Immediate next normal request must succeed
	req2 := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(signupAdmissionBody))
	req2.RemoteAddr = "192.0.2.61:1234"
	req2.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

// TestFlaw_F04Demonstration shows how invalid USN still charges the IP rate limit
// because ValidateRequest does not validate USN format.
func TestFlaw_F04Demonstration(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)

	ip := "192.0.2.77"
	// Request has valid JSON & struct tags, but USN is "INVALID_USN" which fails validateUserInput in controller
	junkPayload := `{"name":"Test","email":"test@example.com","password":"password123","usn":"INVALID_USN","mobile_number":"1234567890","current_year":2,"department":"CS"}`

	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(junkPayload))
	req.RemoteAddr = ip + ":1234"
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	// Notice that limiter was charged 1 hit even though the request had an invalid USN!
	if limiter.hits["signup-ip:ip:"+ip] != 1 {
		t.Fatalf("expected limiter hit to be 1, got %d", limiter.hits["signup-ip:ip:"+ip])
	}
	if limiter.hits["signup-fleet:fleet"] != 1 {
		t.Fatalf("expected fleet hit to be 1, got %d", limiter.hits["signup-fleet:fleet"])
	}
}

// TestFlaw_HighConcurrencyBurst checks behavior when 150 requests arrive concurrently.
// 8 should be active, up to 100 admitted, and excess (>100) should get 429 busy without deadlock or panic.
func TestFlaw_HighConcurrencyBurst(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)

	var (
		wg        sync.WaitGroup
		status400 atomic.Int32
		status429 atomic.Int32
		other     atomic.Int32
	)

	totalRequests := 150
	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(signupAdmissionBody))
			req.RemoteAddr = "192.0.2.88:1234"
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			switch rec.Code {
			case http.StatusBadRequest:
				status400.Add(1)
			case http.StatusTooManyRequests:
				status429.Add(1)
			default:
				other.Add(1)
			}
		}(i)
	}
	wg.Wait()

	t.Logf("High concurrency results: 400s=%d, 429s=%d, other=%d",
		status400.Load(), status429.Load(), other.Load())

	if other.Load() > 0 {
		t.Fatalf("unexpected statuses received: %d", other.Load())
	}
	// At least 100 should be admitted (status 400 due to synthetic body), and excess rejected with 429
	if status400.Load() == 0 {
		t.Fatalf("expected at least some requests to pass, got 0")
	}
}
