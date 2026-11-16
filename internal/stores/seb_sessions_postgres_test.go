package stores_test

import (
	"app/internal/common"
	"app/internal/controllers"
	"app/internal/middleware"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/routes"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func TestBrowserSessionsHTTPPostgres(t *testing.T) {
	db := postgresTestDB(t)
	user, contest, problem, _, _ := executionFixture(t, db, 0)
	ctx := context.Background()
	service := services.NewContestService(stores.NewStorage(db), nil)
	controller := controllers.NewContestController(service, nil)
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set(common.AUTH_USER_ID, user); return next(c) }
	})
	e.POST("/contests/:id/browser-session", controller.StartBrowserSession, middleware.ValidateRequest(new(dto.StartBrowserSessionRequest)))
	e.GET("/contests/:id/problems", controller.GetContestProblemsList)
	start := func(attempt string, cookie *http.Cookie, seb bool, generation int) (*dto.BrowserSessionResponse, *http.Cookie, *httptest.ResponseRecorder) {
		t.Helper()
		r := httptest.NewRequest("POST", "/contests/"+contest+"/browser-session", strings.NewReader(fmt.Sprintf(`{"attempt_id":%q,"generation":%d}`, attempt, generation)))
		r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if seb {
			r.Header.Set("User-Agent", "Mozilla/5.0 SEB/3.9.0")
		} else {
			r.Header.Set("User-Agent", "Mozilla/5.0 Chrome/139.0")
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 200 {
			return nil, nil, w
		}
		var res dto.BrowserSessionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		var saved *http.Cookie
		if cookies := w.Result().Cookies(); len(cookies) > 0 {
			saved = cookies[0]
		}
		return &res, saved, w
	}
	// Ordinary browsers are allowed, flagged, and never consume a SEB session.
	for range 3 {
		res, cookie, w := start(uuid.NewString(), nil, false, 0)
		if w.Code != 200 || res.IsSEB || res.SessionCount != 0 || !res.SEBNotDetected || res.IsTerminated || cookie != nil {
			t.Fatal(res, cookie, w.Code, w.Body.String())
		}
	}
	attempt := uuid.NewString()
	first, cookie, w := start(attempt, nil, true, 0)
	if w.Code != 200 || !first.IsSEB || first.SessionCount != 1 || first.CookieConfirmed || cookie == nil {
		t.Fatal(first, cookie, w.Code, w.Body.String())
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteNoneMode || cookie.MaxAge != 0 || !cookie.Expires.IsZero() || cookie.Domain != "" || cookie.Path != "/" || !strings.HasPrefix(cookie.Name, "__Host-") {
		t.Fatal("unsafe cookie", cookie)
	}
	retry, retryCookie, w := start(attempt, nil, true, 0)
	if w.Code != 200 || retry.SessionCount != 1 || retryCookie.Value != cookie.Value {
		t.Fatal("lost response consumed another session", retry, w.Code)
	}
	confirmed, _, w := start(uuid.NewString(), cookie, true, 0)
	if w.Code != 200 || !confirmed.CookieConfirmed || confirmed.SessionCount != 1 {
		t.Fatal(confirmed, w.Code)
	}
	if _, _, w := start(attempt, nil, true, 0); w.Code != 409 {
		t.Fatal("confirmed startup restored a deleted cookie", w.Code)
	}
	// Repeated reads, navigation, and reconnect-style requests never create a session.
	for range 4 {
		r := httptest.NewRequest("GET", "/contests/"+contest+"/problems", nil)
		r.AddCookie(cookie)
		r.Header.Set("User-Agent", "SEB/3.9.0")
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for want := 2; want <= 3; want++ {
		res, _, w := start(uuid.NewString(), nil, true, 0)
		if w.Code != 200 || res.SessionCount != want || res.IsTerminated {
			t.Fatal(res, w.Code)
		}
	}
	locked, _, w := start(uuid.NewString(), nil, true, 0)
	if w.Code != 200 || !locked.IsTerminated || locked.SessionCount != 3 {
		t.Fatal(locked, w.Code)
	}
	for _, agent := range []string{"SEB/3.9.0", "Chrome/139.0"} {
		r := httptest.NewRequest("GET", "/contests/"+contest+"/problems", nil)
		r.AddCookie(cookie)
		r.Header.Set("User-Agent", agent)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "BROWSER_SESSION_LIMIT") {
			t.Fatal("existing lock bypassed", w.Code, w.Body.String())
		}
	}
	submissions := stores.NewSubmissionStore(db)
	sub := &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code}
	if _, _, err := submissions.CreateCodeSubmission(ctx, sub, uuid.NewString(), "payload", 5, 10); !errors.Is(err, common.ErrBrowserSessionTerminated) {
		t.Fatal("code bypasses lock", err)
	}
	sub.Type = models.MCQ
	if _, err := submissions.CreateMCQSubmission(ctx, sub, 5); !errors.Is(err, common.ErrBrowserSessionTerminated) {
		t.Fatal("MCQ bypasses lock", err)
	}
	if _, err := db.Exec(`UPDATE contest_registrations SET tab_switch_count=2 WHERE contest_id=$1 AND user_id=$2`, contest, user); err != nil {
		t.Fatal(err)
	}
	admin := echo.New()
	admin.POST("/admin/contests/:contestId/contestants/:userId/reset-browser-sessions", controller.ResetBrowserSessions, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set(common.AUTH_USER_ID, "trusted-admin"); return next(c) }
	})
	resetRequest := httptest.NewRequest("POST", "/admin/contests/"+contest+"/contestants/"+user+"/reset-browser-sessions", strings.NewReader(`{"admin_id":"spoofed"}`))
	reset := httptest.NewRecorder()
	admin.ServeHTTP(reset, resetRequest)
	if reset.Code != 200 {
		t.Fatal(reset.Code, reset.Body.String())
	}
	registrations, err := stores.NewContestStore(db).GetContestRegistrations(ctx, contest)
	if err != nil || len(registrations) != 1 {
		t.Fatal(registrations, err)
	}
	reg := registrations[0]
	if reg.BrowserSessionCount != 0 || reg.BrowserSessionGeneration != 1 || reg.IsBrowserSessionTerminated || !reg.SEBNotDetected || reg.Disqualified || reg.Hidden || reg.TabSwitchCount != 2 {
		t.Fatal("reset changed unrelated state", reg)
	}
	var auditAdmin string
	var previous int
	var when time.Time
	if err := db.QueryRow(`SELECT admin_id,previous_count,reset_at FROM browser_session_resets WHERE contest_id=$1 AND user_id=$2 AND generation=1`, contest, user).Scan(&auditAdmin, &previous, &when); err != nil || auditAdmin != "trusted-admin" || previous != 3 || when.IsZero() {
		t.Fatal(auditAdmin, previous, when, err)
	}
	if _, _, stale := start(uuid.NewString(), cookie, true, 0); stale.Code != 409 || !strings.Contains(stale.Body.String(), "BROWSER_SESSION_STALE") {
		t.Fatal("old request crossed reset", stale.Code, stale.Body.String())
	}
	res, newCookie, w := start(uuid.NewString(), cookie, true, 1)
	if w.Code != 200 || res.Generation != 1 || res.SessionCount != 1 || newCookie == nil || newCookie.Value == cookie.Value || res.IsTerminated || !res.SEBNotDetected {
		t.Fatal("current browser did not resume as session 1", res, newCookie, w.Code)
	}
	res, _, w = start(uuid.NewString(), newCookie, true, 1)
	if w.Code != 200 || res.SessionCount != 1 || !res.CookieConfirmed {
		t.Fatal(res, w.Code)
	}
	var oldSessions int
	if err := db.QueryRow(`SELECT count(*) FROM contest_browser_sessions WHERE contest_id=$1 AND user_id=$2 AND generation=0`, contest, user).Scan(&oldSessions); err != nil || oldSessions != 3 {
		t.Fatal("reset deleted history", oldSessions, err)
	}
	if _, err := db.Exec(`UPDATE contests SET end_time=$2 WHERE id=$1`, contest, time.Now().UnixMilli()-1); err != nil {
		t.Fatal(err)
	}
	review := httptest.NewRecorder()
	e.ServeHTTP(review, httptest.NewRequest("GET", "/contests/"+contest+"/problems", nil))
	if review.Code != 200 {
		t.Fatal("post-contest review blocked", review.Code, review.Body.String())
	}
	// The production reset route requires authentication before it can reach the controller.
	protected := echo.New()
	routes.AddAdminRoutes(protected, controller, nil, nil, nil, nil, nil)
	denied := httptest.NewRecorder()
	protected.ServeHTTP(denied, httptest.NewRequest("POST", resetRequest.URL.Path, nil))
	if denied.Code != 401 {
		t.Fatal("unprotected reset", denied.Code, denied.Body.String())
	}
}

func TestBrowserSessionConcurrencyPostgres(t *testing.T) {
	db := postgresTestDB(t)
	user, contest, _, _, _ := executionFixture(t, db, 0)
	store := stores.NewSEBStore(db)
	ctx := context.Background()
	generation := 0
	attempt := uuid.NewString()
	var wg sync.WaitGroup
	tokens := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, token, err := store.Observe(ctx, contest, user, "", attempt, true, true, &generation)
			if err != nil || res.SessionCount != 1 || res.IsTerminated {
				t.Errorf("res=%+v err=%v", res, err)
				return
			}
			tokens <- token
		}()
	}
	wg.Wait()
	close(tokens)
	var token string
	for got := range tokens {
		if token != "" && got != token {
			t.Fatal("retries issued different cookies")
		}
		token = got
	}
	if _, _, err := store.Observe(ctx, contest, user, token, "", true, false, nil); err != nil {
		t.Fatal(err)
	}
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.Observe(ctx, contest, user, "", uuid.NewString(), true, true, &generation)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var count int
	var locked bool
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM contest_browser_sessions WHERE contest_id=$1 AND user_id=$2),is_browser_session_terminated FROM contest_registrations WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&count, &locked); err != nil || count != 3 || !locked {
		t.Fatal(count, locked, err)
	}
	otherUser, otherContest, _, _, _ := executionFixture(t, db, 0)
	if _, _, err := store.Observe(ctx, otherContest, otherUser, token, "", true, false, nil); !errors.Is(err, common.ErrBrowserSessionRequired) {
		t.Fatal("cookie crossed identity", err)
	}
	// Concurrent resets serialize and preserve a complete audit trail.
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Reset(ctx, contest, user, "admin"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var current int
	if err := db.QueryRow(`SELECT browser_session_generation FROM contest_registrations WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&current); err != nil || current != 8 {
		t.Fatal(current, err)
	}
	var stale *common.BrowserSessionStaleError
	if _, _, err := store.Observe(ctx, contest, user, token, uuid.NewString(), true, true, &generation); !errors.As(err, &stale) || stale.Generation != 8 {
		t.Fatal("stale retry changed the allowance", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM contest_browser_sessions WHERE contest_id=$1 AND user_id=$2 AND generation=8`, contest, user).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM browser_session_resets WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&count); err != nil || count != 8 {
		t.Fatal(count, err)
	}
}
