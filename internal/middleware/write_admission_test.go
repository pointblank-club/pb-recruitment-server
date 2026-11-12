package middleware

import (
	"app/internal/common"
	"context"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

type writeBudgetProbe struct {
	calls   atomic.Int32
	release <-chan struct{}
}

func (p *writeBudgetProbe) Allow(context.Context, string, int, int) (int, error) {
	p.calls.Add(1)
	if p.release != nil {
		<-p.release
	}
	return 0, nil
}

func TestSubmissionBurstRejectsBeforeSharedBudget(t *testing.T) {
	now := time.Unix(1700000000, 0)
	burst := newSubmissionBurstLimitWithLimiter(newUserRateLimiter(4, 1, func() time.Time { return now }))
	release := make(chan struct{})
	probe := &writeBudgetProbe{release: release}
	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set(common.AUTH_USER_ID, "one-user"); return next(c) }
	}
	for _, path := range []string{"/submission/run", "/submission/submit"} {
		e.POST(path, func(c echo.Context) error { return c.NoContent(204) }, auth, burst, LimitRequests(probe, "submission-write", 60, 60))
	}
	results := make(chan int, 800)
	var wg sync.WaitGroup
	for i := range 800 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", []string{"/submission/run", "/submission/submit"}[i%2], nil)
			r.RemoteAddr = "192.0.2.1:1234"
			e.ServeHTTP(w, r)
			if w.Code == 429 && w.Header().Get("Retry-After") != "1" {
				t.Error("missing retry delay")
			}
			results <- w.Code
		}(i)
	}
	// The other796 requests finish while every admitted SQL check remains blocked.
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for range 796 {
		select {
		case code := <-results:
			if code != 429 {
				t.Errorf("got%d, want429", code)
			}
		case <-timeout.C:
			close(release)
			wg.Wait()
			t.Fatal("burst rejections waited for SQL")
		}
	}
	close(release)
	wg.Wait()
	if probe.calls.Load() != 4 {
		t.Fatal("shared budget calls", probe.calls.Load())
	}
	for range 4 {
		if code := <-results; code != 204 {
			t.Fatal(code)
		}
	}
	now = now.Add(time.Second)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("POST", "/submission/submit", nil))
	if w.Code != 204 {
		t.Fatal("refill", w.Code)
	}
}

func TestSubmissionBurstSeparatesCampusUsers(t *testing.T) {
	e := echo.New()
	probe := &writeBudgetProbe{}
	e.POST("/submit", func(c echo.Context) error { return c.NoContent(204) }, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set(common.AUTH_USER_ID, c.QueryParam("user")); return next(c) }
	}, SubmissionBurstLimit(), LimitRequests(probe, "submission-write", 60, 60))
	var wg sync.WaitGroup
	for i := range 600 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/submit?user="+time.Unix(int64(i), 0).Format(time.RFC3339), nil)
			r.RemoteAddr = "192.0.2.1:1234"
			e.ServeHTTP(w, r)
			if w.Code != 204 {
				t.Error(w.Code)
			}
		}(i)
	}
	wg.Wait()
	if probe.calls.Load() != 600 {
		t.Fatal(probe.calls.Load())
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("POST", "/submit", nil))
	if w.Code != 401 || probe.calls.Load() != 600 {
		t.Fatal("anonymous request reached SQL", w.Code, probe.calls.Load())
	}
}

func TestSignupCapacityAllowsHundredCampusBurstWithEightWorkers(t *testing.T) {
	e := echo.New()
	release := make(chan struct{})
	started := make(chan struct{}, 100)
	var active, peak, completed atomic.Int32
	e.POST("/signup", func(c echo.Context) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		completed.Add(1)
		return c.NoContent(204)
	}, SignupCapacity())
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/signup", nil)
			r.RemoteAddr = "192.0.2.1:1234"
			e.ServeHTTP(w, r)
			if w.Code != 204 {
				t.Error(w.Code)
			}
		}()
	}
	for range 8 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("workers did not start")
		}
	}
	if active.Load() != 8 {
		t.Error("active", active.Load())
	}
	close(release)
	wg.Wait()
	if completed.Load() != 100 || peak.Load() != 8 {
		t.Fatal("completed/peak", completed.Load(), peak.Load())
	}
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestSignupCapacityBoundsQueueAndReleasesCancellation(t *testing.T) {
	e := echo.New()
	release := make(chan struct{})
	started := make(chan struct{}, 3)
	e.POST("/signup", func(c echo.Context) error { started <- struct{}{}; <-release; return c.NoContent(204) }, newSignupCapacity(1, 2, time.Second, time.Second))
	call := func(ctx context.Context) <-chan int {
		result := make(chan int, 1)
		go func() {
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest("POST", "/signup", nil).WithContext(ctx))
			if w.Code == 429 && w.Header().Get("Retry-After") != "1" {
				t.Error("missing retry")
			}
			result <- w.Code
		}()
		return result
	}
	first := call(context.Background())
	<-started
	base, cancel := context.WithCancel(context.Background())
	queued := &observedDoneContext{Context: base, observed: make(chan struct{})}
	second := call(queued)
	<-queued.observed
	if code := <-call(context.Background()); code != 429 {
		t.Error("full queue", code)
	}
	cancel()
	if code := <-second; code != 408 {
		t.Error("cancelled", code)
	}
	nextCtx, nextCancel := context.WithCancel(context.Background())
	thirdCtx := &observedDoneContext{Context: nextCtx, observed: make(chan struct{})}
	third := call(thirdCtx)
	<-thirdCtx.observed
	close(release)
	if code := <-first; code != 204 {
		t.Error(code)
	}
	if code := <-third; code != 204 {
		t.Error("cancel leaked slot", code)
	}
	nextCancel()
}

func TestSignupCapacityQueueTimeoutDoesNotCallHandler(t *testing.T) {
	e := echo.New()
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	e.POST("/signup", func(c echo.Context) error { calls.Add(1); close(started); <-release; return c.NoContent(204) }, newSignupCapacity(1, 2, 20*time.Millisecond, time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/signup", nil))
	}()
	<-started
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("POST", "/signup", nil))
	close(release)
	<-done
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" || calls.Load() != 1 {
		t.Fatal(w.Code, w.Header(), calls.Load())
	}
}

func TestSignupCapacityAlreadyCancelledWithFreeSlot(t *testing.T) {
	e := echo.New()
	e.POST("/signup", func(c echo.Context) error {
		t.Error("cancelled request reached signup work")
		return c.NoContent(204)
	}, SignupCapacity())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("POST", "/signup", nil).WithContext(ctx))
	if w.Code != 408 {
		t.Fatal(w.Code)
	}
}

func TestSignupCapacityRejectsExpiredWait(t *testing.T) {
	e := echo.New()
	called := false
	e.POST("/signup", func(c echo.Context) error { called = true; return c.NoContent(204) }, newSignupCapacity(1, 1, 0, time.Second))
	for range 20 {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest("POST", "/signup", nil))
		if w.Code != 429 || called {
			t.Fatal("expired queue admitted work", w.Code, called)
		}
	}
}

func TestSignupCapacityActiveTimeoutReleasesWorker(t *testing.T) {
	e := echo.New()
	calls := 0
	e.POST("/signup", func(c echo.Context) error {
		calls++
		if calls == 1 {
			if _, ok := c.Request().Context().Deadline(); !ok {
				t.Fatal("active signup has no deadline")
			}
			<-c.Request().Context().Done()
			if c.Request().Context().Err() != context.DeadlineExceeded {
				t.Fatal(c.Request().Context().Err())
			}
			return c.NoContent(503)
		}
		return c.NoContent(204)
	}, newSignupCapacity(1, 1, time.Second, 20*time.Millisecond))
	for _, want := range []int{503, 204} {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest("POST", "/signup", nil))
		if w.Code != want {
			t.Fatalf("HTTP %d, want %d", w.Code, want)
		}
	}
}
