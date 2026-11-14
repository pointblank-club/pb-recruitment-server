package routes

import (
	"app/internal/controllers"
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
)

// Invalid USN fails ValidateUserInput before reaching shared budgets.
const signupAdmissionBody = `{"name":"Test","email":"test@example.test","password":"synthetic-password","usn":"invalid","mobile_number":"1234567890","current_year":2,"department":"CS"}`

// Valid USN passes ValidateUserInput and reaches admission budgets.
const validSignupAdmissionBody = `{"name":"Test","email":"test@example.test","password":"synthetic-password","usn":"1DS24CS001","mobile_number":"1234567890","current_year":2,"department":"CS"}`

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

	// 1. Invalid JSON/DTOs must not consume shared SQL budgets.
	invalid := httptest.NewRequest("POST", "/auth/signup", strings.NewReader(`{}`))
	invalid.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	invalidResponse := httptest.NewRecorder()
	e.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != 400 || len(limiter.hits) != 0 {
		t.Fatal("invalid signup reached SQL admission", invalidResponse.Code, limiter.hits)
	}

	// 2. Junk USNs must be rejected for free without consuming shared SQL budgets (F-04).
	invalidUSN := httptest.NewRequest("POST", "/auth/signup", strings.NewReader(signupAdmissionBody))
	invalidUSN.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	invalidUSNResponse := httptest.NewRecorder()
	e.ServeHTTP(invalidUSNResponse, invalidUSN)
	if invalidUSNResponse.Code != 400 || len(limiter.hits) != 0 {
		t.Fatal("invalid USN reached SQL admission", invalidUSNResponse.Code, limiter.hits)
	}

	// 3. Valid signups pass to admission budgets.
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/auth/signup", strings.NewReader(validSignupAdmissionBody))
			r.RemoteAddr = "192.0.2.1:1234"
			r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			e.ServeHTTP(w, r)
			if w.Code != 503 && w.Code != 201 {
				t.Errorf("unexpected status: %d %s", w.Code, w.Body.String())
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
	r := httptest.NewRequest("POST", "/auth/signup", strings.NewReader(validSignupAdmissionBody))
	r.RemoteAddr = "192.0.2.2:1234"
	r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	e.ServeHTTP(w, r)
	if w.Code != 429 || w.Header().Get(echo.HeaderRetryAfter) != "10" {
		t.Fatal("shared fleet burst was not bounded", w.Code, w.Header())
	}
}

