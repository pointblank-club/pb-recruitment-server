package routes

import (
	"app/internal/controllers"
	"app/internal/middleware"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/labstack/echo/v4"
)

type unavailableReadLimiter struct{ calls atomic.Int32 }

func (l *unavailableReadLimiter) Allow(context.Context, string, int, int) (int, error) {
	l.calls.Add(1)
	return 0, errors.New("database unavailable")
}

func readAdmissionServer(limiter middleware.RequestLimiter, legacy bool) *echo.Echo {
	e := echo.New()
	middleware.ConfigureRequestBounds(e)
	AddUserRoutes(e, nil, controllers.NewUserController(nil), limiter)
	if legacy {
		// Reproduce the former global PostgreSQL check on the exact route under test.
		e.Use(middleware.LimitRequests(limiter, "ingress", 60000, 60))
	} else {
		middleware.AddRequestLimits(e)
	}
	return e
}

func concurrentUnauthenticatedReads(t *testing.T, e *echo.Echo) {
	t.Helper()
	const n = 600
	headers := []string{"", "Basic malformed", "Bearer "}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r := httptest.NewRequest(http.MethodGet, "/users/profile", nil)
			r.RemoteAddr = "192.0.2.1:1234"
			r.Header.Set("Authorization", headers[i%len(headers)])
			w := httptest.NewRecorder()
			e.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("read status=%d, want401: %s", w.Code, w.Body.String())
			}
		}(i)
	}
	close(start)
	wg.Wait()
}

func TestUnauthenticatedReadsDoNotUseDatabaseAdmission(t *testing.T) {
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	limiter := &unavailableReadLimiter{}
	e := readAdmissionServer(limiter, false)
	concurrentUnauthenticatedReads(t, e)
	if got := limiter.calls.Load(); got != 0 {
		t.Fatal("401 path called database admission", got)
	}
	// The independent signup budget must still fail closed before any Firebase work.
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(validSignupAdmissionBody))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	e.ServeHTTP(w, request)
	if w.Code != http.StatusServiceUnavailable || limiter.calls.Load() != 1 {
		t.Fatal("signup bypassed shared admission", w.Code, limiter.calls.Load())
	}
}

func TestUnauthenticatedReadsWithExhaustedPoolPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable local PostgreSQL instance")
	}
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	limiter := middleware.NewRequestLimiter(db)
	before := db.Stats().WaitCount
	concurrentUnauthenticatedReads(t, readAdmissionServer(limiter, false))
	if got := db.Stats().WaitCount; got != before {
		t.Fatalf("new ingress waited for DB: %d -> %d", before, got)
	}

	legacy := readAdmissionServer(limiter, true)
	reqCtx, cancelRequest := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelRequest()
	r := httptest.NewRequest(http.MethodGet, "/users/profile", nil).WithContext(reqCtx)
	w := httptest.NewRecorder()
	legacy.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || db.Stats().WaitCount <= before {
		t.Fatal("legacy check did not reproduce the DB admission failure", w.Code, db.Stats())
	}
	t.Log("exhausted pool: previous ingress returned503; fixed ingress returned600/600401 with zero DB waits")
}
