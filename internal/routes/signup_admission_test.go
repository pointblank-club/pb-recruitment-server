package routes

import (
	"app/internal/controllers"
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
)

// The DTO is valid; the synthetic USN fails controller checks before Firebase.
const signupAdmissionBody = `{"name":"Test","email":"test@example.test","password":"synthetic-password","usn":"invalid","mobile_number":"1234567890","current_year":2,"department":"CS"}`

type signupBudgetRecorder struct {
	mu     sync.Mutex
	hits   map[string]int
	limits map[string][2]int
}

func (l *signupBudgetRecorder) Allow(_ context.Context, key string, limit, seconds int) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limits[key] = [2]int{limit, seconds}
	if l.hits[key] >= limit {
		return seconds, nil
	}
	l.hits[key]++
	return 0, nil
}

func TestSignupRouteAllowsHundredCampusAdmissionsBeforeFleetLimit(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)
	// Invalid JSON/DTOs must not consume the shared SQL budgets.
	invalid := httptest.NewRequest("POST", "/auth/signup", strings.NewReader(`{}`))
	invalid.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	invalidResponse := httptest.NewRecorder()
	e.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != 400 || len(limiter.hits) != 0 {
		t.Fatal("invalid signup reached SQL admission", invalidResponse.Code, limiter.hits)
	}
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/auth/signup", strings.NewReader(signupAdmissionBody))
			r.RemoteAddr = "192.0.2.1:1234"
			r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			e.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Errorf("campus request rejected by admission: %d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	if limiter.hits["signup-fleet:fleet"] != 100 || limiter.limits["signup-fleet:fleet"] != [2]int{100, 10} {
		t.Fatal("fleet policy", limiter.hits, limiter.limits)
	}
	if limiter.hits["signup-ip:ip:192.0.2.1"] != 100 || limiter.limits["signup-ip:ip:192.0.2.1"] != [2]int{1200, 3600} {
		t.Fatal("campus IP policy", limiter.hits, limiter.limits)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/auth/signup", strings.NewReader(signupAdmissionBody))
	r.RemoteAddr = "192.0.2.2:1234"
	r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	e.ServeHTTP(w, r)
	if w.Code != 429 || w.Header().Get(echo.HeaderRetryAfter) != "10" {
		t.Fatal("shared fleet burst was not bounded", w.Code, w.Header())
	}
}

type blockingReader struct {
	started chan struct{}
	unblock chan struct{}
}

func (b *blockingReader) Read(p []byte) (n int, err error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.unblock
	return 0, io.EOF
}

func TestSignupSlowBodyDoesNotHoldCapacitySlots(t *testing.T) {
	e := echo.New()
	limiter := &signupBudgetRecorder{hits: map[string]int{}, limits: map[string][2]int{}}
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)

	// Simulate 100 slow clients currently stuck reading request bodies.
	// If SignupCapacity were before ValidateRequest, all 100 capacity slots would be held,
	// causing any subsequent request to be immediately rejected with 429.
	unblock := make(chan struct{})
	defer close(unblock)

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		reader := &blockingReader{started: make(chan struct{}, 1), unblock: unblock}
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/auth/signup", reader)
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
		}()
		<-reader.started
	}

	// While 100 clients are slowly reading bodies, an active legitimate request arrives.
	// Because body validation runs before capacity allocation, capacity slots are free.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/auth/signup", strings.NewReader(signupAdmissionBody))
	r.RemoteAddr = "192.0.2.99:1234"
	r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	e.ServeHTTP(w, r)

	// The request should pass capacity and reach controller validation (returning 400 for synthetic USN),
	// NOT 429 "signup capacity busy".
	if w.Code == 429 && strings.Contains(w.Body.String(), "signup capacity busy") {
		t.Fatalf("slow body reads exhausted signup capacity slots: %d %s", w.Code, w.Body.String())
	}
	if w.Code != 400 {
		t.Fatalf("unexpected status code: %d, body: %s", w.Code, w.Body.String())
	}
}

