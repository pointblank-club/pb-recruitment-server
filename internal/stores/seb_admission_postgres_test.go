package stores_test

import (
	"app/internal/common"
	"app/internal/controllers"
	"app/internal/judge0"
	"app/internal/middleware"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type resetBeforeAdmissionStore struct {
	*stores.SubmissionStore
	reset func(context.Context, *int) error
}

func (s resetBeforeAdmissionStore) CreateMCQSubmission(ctx context.Context, sub *models.Submission, cooldown int64) (string, error) {
	if err := s.reset(ctx, sub.BrowserSessionGeneration); err != nil {
		return "", err
	}
	return s.SubmissionStore.CreateMCQSubmission(ctx, sub, cooldown)
}

func (s resetBeforeAdmissionStore) CreateCodeSubmission(ctx context.Context, sub *models.Submission, key, fingerprint string, cooldown int64, capacity int) (string, bool, error) {
	if err := s.reset(ctx, sub.BrowserSessionGeneration); err != nil {
		return "", false, err
	}
	return s.SubmissionStore.CreateCodeSubmission(ctx, sub, key, fingerprint, cooldown, capacity)
}

func (s resetBeforeAdmissionStore) CheckRunCapacity(ctx context.Context, user, contest string, generation *int) error {
	if err := s.reset(ctx, generation); err != nil {
		return err
	}
	return s.SubmissionStore.CheckRunCapacity(ctx, user, contest, generation)
}

func browserAdmissionRequest(e *echo.Echo, path string, body any, cookie *http.Cookie, seb bool, key string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	request := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if seb {
		request.Header.Set("User-Agent", "SEB/3.9.0")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	return response
}

func TestRunRequiresContestAndConfirmedSEBCookie(t *testing.T) {
	db := postgresTestDB(t)
	user := "run-cookie-user"
	contest := runContestFixture(t, db, user)
	var dispatches atomic.Int32
	judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			dispatches.Add(1)
			w.WriteHeader(201)
			fmt.Fprint(w, `[{"token":"job"}]`)
			return
		}
		fmt.Fprint(w, `{"submissions":[{"token":"job","status":{"id":3},"stdout":null,"stderr":null,"compile_output":null}]}`)
	}))
	defer judge.Close()
	t.Setenv("JUDGE0_URL", judge.URL)
	storage := stores.NewStorage(db)
	contestService := services.NewContestService(storage, nil)
	client := judge0.NewClient()
	controller := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, client), contestService, client)
	contestController := controllers.NewContestController(contestService, nil)
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set(common.AUTH_USER_ID, user); return next(c) }
	})
	e.POST("/submission/run", controller.Run)
	e.POST("/contests/:id/browser-session", contestController.StartBrowserSession, middleware.ValidateRequest(new(dto.StartBrowserSessionRequest)))
	for _, id := range []string{"", "   ", contest} {
		for _, seb := range []bool{false, true} {
			if id == contest && !seb {
				continue
			}
			body := map[string]any{"language": "python", "code": "print(1)"}
			if id != "" {
				body["contest_id"] = id
			}
			response := browserAdmissionRequest(e, "/submission/run", body, nil, seb, "")
			want := 400
			if id == contest {
				want = 428
			}
			if response.Code != want {
				t.Fatalf("contest=%q SEB=%t: %d %s", id, seb, response.Code, response.Body.String())
			}
		}
	}
	if dispatches.Load() != 0 {
		t.Fatal("Run dispatched before cookie admission")
	}
	zero := 0
	startup := dto.StartBrowserSessionRequest{AttemptID: uuid.NewString(), Generation: &zero}
	response := browserAdmissionRequest(e, "/contests/"+contest+"/browser-session", startup, nil, true, "")
	if response.Code != 200 || len(response.Result().Cookies()) != 1 {
		t.Fatal(response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	response = browserAdmissionRequest(e, "/contests/"+contest+"/browser-session", startup, cookie, true, "")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = browserAdmissionRequest(e, "/submission/run", dto.RunRequest{ContestID: contest, Language: "python", Code: "print(1)"}, cookie, true, "")
	if response.Code != 200 || dispatches.Load() != 1 {
		t.Fatal("confirmed Run failed", response.Code, response.Body.String(), dispatches.Load())
	}
	assertNoRunWrites(t, db)
}

func TestBrowserResetFencesAtomicWriteAdmission(t *testing.T) {
	for _, kind := range []string{"code", "mcq", "run", "ordinary-mcq"} {
		t.Run(kind, func(t *testing.T) {
			db := postgresTestDB(t)
			user, contest, problem, _, _ := executionFixture(t, db, 0)
			if _, err := db.Exec(`DELETE FROM submissions`); err != nil {
				t.Fatal(err)
			}
			mcq := kind == "mcq" || kind == "ordinary-mcq"
			if mcq {
				if _, err := db.Exec(`UPDATE problems SET type='mcq',time_limit=NULL,memory_limit=NULL,options='{"A","B"}',answer='{1}' WHERE id=$1`, problem); err != nil {
					t.Fatal(err)
				}
			}
			storage := stores.NewStorage(db)
			seb := kind != "ordinary-mcq"
			var resets int
			storage.Submissions = resetBeforeAdmissionStore{stores.NewSubmissionStore(db), func(ctx context.Context, generation *int) error {
				if seb && (generation == nil || *generation != 0) || !seb && generation != nil {
					return fmt.Errorf("wrong validated generation: %v", generation)
				}
				resets++
				_, err := storage.SEB.Reset(ctx, contest, user, "reset-admin")
				return err
			}}
			var dispatches atomic.Int32
			judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { dispatches.Add(1); w.WriteHeader(500) }))
			defer judge.Close()
			t.Setenv("JUDGE0_URL", judge.URL)
			contestService := services.NewContestService(storage, nil)
			client := judge0.NewClient()
			controller := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, client), contestService, client)
			contestController := controllers.NewContestController(contestService, nil)
			e := echo.New()
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error { c.Set(common.AUTH_USER_ID, user); return next(c) }
			})
			e.POST("/contests/:id/browser-session", contestController.StartBrowserSession, middleware.ValidateRequest(new(dto.StartBrowserSessionRequest)))
			e.POST("/submission/submit", controller.SubmitSolution, middleware.ValidateRequest(new(dto.SubmitSubmissionRequest)))
			e.POST("/submission/run", controller.Run)
			var cookie *http.Cookie
			if seb {
				zero := 0
				startup := dto.StartBrowserSessionRequest{AttemptID: uuid.NewString(), Generation: &zero}
				response := browserAdmissionRequest(e, "/contests/"+contest+"/browser-session", startup, nil, true, "")
				if response.Code != 200 || len(response.Result().Cookies()) != 1 {
					t.Fatal(response.Code, response.Body.String())
				}
				cookie = response.Result().Cookies()[0]
				if response := browserAdmissionRequest(e, "/contests/"+contest+"/browser-session", startup, cookie, true, ""); response.Code != 200 {
					t.Fatal(response.Code, response.Body.String())
				}
			}
			req := dto.SubmitSubmissionRequest{ContestID: contest, ProblemID: problem, Type: models.Code, Language: "python", Code: "print(1)"}
			if mcq {
				req.Type, req.Option, req.Language, req.Code = models.MCQ, []int{1}, "", ""
			}
			var replayID, replayKey string
			if kind == "code" {
				replayKey = uuid.NewString()
				payload, _ := json.Marshal(req)
				zero := 0
				var err error
				replayID, _, err = stores.NewSubmissionStore(db).CreateCodeSubmission(context.Background(), &models.Submission{UserID: user, ContestID: contest, ProblemID: problem, Type: models.Code, BrowserSessionGeneration: &zero}, replayKey, fmt.Sprintf("%x", sha256.Sum256(payload)), 5, 10)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE submissions SET status='accepted' WHERE id=$1`, replayID); err != nil {
					t.Fatal(err)
				}
			}
			var response *httptest.ResponseRecorder
			if kind == "run" {
				response = browserAdmissionRequest(e, "/submission/run", dto.RunRequest{ContestID: contest, Language: "python", Code: "print(1)"}, cookie, true, "")
			} else {
				response = browserAdmissionRequest(e, "/submission/submit", req, cookie, seb, uuid.NewString())
			}
			want := 428
			if !seb {
				want = 201
			}
			if response.Code != want || resets != 1 || dispatches.Load() != 0 {
				t.Fatalf("%s: %d %s resets=%d dispatches=%d", kind, response.Code, response.Body.String(), resets, dispatches.Load())
			}
			var generation, sessions, submissions int
			if err := db.QueryRow(`SELECT browser_session_generation,(SELECT count(*) FROM contest_browser_sessions WHERE contest_id=$1 AND user_id=$2 AND generation=browser_session_generation),(SELECT count(*) FROM submissions WHERE contest_id=$1) FROM contest_registrations WHERE contest_id=$1 AND user_id=$2`, contest, user).Scan(&generation, &sessions, &submissions); err != nil {
				t.Fatal(err)
			}
			if generation != 1 || sessions != 0 || (kind == "code" || !seb) && submissions != 1 || (kind == "mcq" || kind == "run") && submissions != 0 {
				t.Fatalf("unexpected reset/admission state: generation=%d sessions=%d submissions=%d", generation, sessions, submissions)
			}
			if kind == "code" {
				response = browserAdmissionRequest(e, "/submission/submit", req, cookie, true, replayKey)
				var replay dto.SubmitSubmissionResponse
				_ = json.Unmarshal(response.Body.Bytes(), &replay)
				if response.Code != 201 || replay.SubmissionID != replayID || resets != 1 {
					t.Fatalf("accepted replay did not precede cookie/admission: %d %s", response.Code, response.Body.String())
				}
			}
		})
	}
}
