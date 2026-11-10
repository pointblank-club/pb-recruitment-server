package stores_test

import (
	"app/internal/common"
	"app/internal/controllers"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func TestMCQVerdictVisibilityPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx := context.Background()
	user, contest, codeProblem, codeSubmission, executions := executionFixture(t, db, 1)
	storage := stores.NewStorage(db)
	for _, execution := range executions {
		if err := storage.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: execution.ID, Status: "accepted"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE submissions SET created_at=100 WHERE id=$1`, codeSubmission); err != nil {
		t.Fatal(err)
	}
	mcqProblem := uuid.NewString()
	if err := storage.Problems.CreateProblem(ctx, &models.Problem{ID: mcqProblem, ContestID: contest, Name: "MCQ", Type: models.MCQ, Score: 20, Answer: []int{1}, Options: []string{"wrong", "right"}}); err != nil {
		t.Fatal(err)
	}
	other := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'Other',$2,$1,1,'CS')`, other, other+"@example.test"); err != nil {
		t.Fatal(err)
	}
	otherCode, otherExecutions := executionSubmission(t, db, other, contest, codeProblem, 1)
	if err := storage.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: otherExecutions[0].ID, Status: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE submissions SET created_at=100 WHERE id=$1`, otherCode); err != nil {
		t.Fatal(err)
	}
	// Two accepted code attempts still count as one solved problem and one score.
	_, repeatedExecutions := executionSubmission(t, db, user, contest, codeProblem, 1)
	if err := storage.Executions.ProcessFinal(ctx, stores.FinalExecutionResult{ExecutionID: repeatedExecutions[0].ID, Status: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO admin(user_id) VALUES($1)`, user); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{user, other} {
		if _, err := db.Exec(`INSERT INTO contest_registrations(contest_id,user_id,registered_at) VALUES($1,$2,1)`, contest, uid); err != nil {
			t.Fatal(err)
		}
	}
	correct, err := storage.Submissions.CreateMCQSubmission(ctx, &models.Submission{UserID: user, ContestID: contest, ProblemID: mcqProblem, Type: models.MCQ, Option: []int{1}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := storage.Submissions.CreateMCQSubmission(ctx, &models.Submission{UserID: other, ContestID: contest, ProblemID: mcqProblem, Type: models.MCQ, Option: []int{0}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{correct, wrong} {
		if err := storage.Submissions.JudgeMCQ(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// Even a forced consolidation while open must not expose stored MCQ ranks.
	if _, err := storage.Rankings.ConsolidateContest(ctx, contest, false, true); err != nil {
		t.Fatal(err)
	}
	cs := services.NewContestService(storage, nil)
	sc := controllers.NewSubmissionController(services.NewSubmissionService(storage, nil, nil), cs, nil)
	cc := controllers.NewContestController(cs, nil)
	request := func(handler echo.HandlerFunc, uid, id string, body any) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), w)
		c.SetParamNames("id")
		c.SetParamValues(id)
		if uid != "" {
			c.Set(common.AUTH_USER_ID, uid)
		}
		if body != nil {
			c.Set(common.VALIDATED_REQUEST_BODY, body)
		}
		if err := handler(c); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("missing private response caching policy")
		}
		return w.Body.Bytes()
	}
	for _, ended := range []bool{false, true} {
		if ended {
			if _, err := db.Exec(`UPDATE contests SET end_time=$2 WHERE id=$1`, contest, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			uid, id  string
			verdict  models.SubmissionStatus
			progress string
		}{
			{user, correct, models.Accepted, "solved"}, {other, wrong, models.WrongAnswer, "failed"},
		} {
			wantStatus, wantProgress := models.Attempted, "attempted"
			if ended {
				wantStatus, wantProgress = tc.verdict, tc.progress
			}
			var status struct {
				Status models.SubmissionStatus `json:"status"`
			}
			if err := json.Unmarshal(request(sc.GetSubmissionStatus, tc.uid, tc.id, nil), &status); err != nil || status.Status != wantStatus {
				t.Fatalf("ended=%v status=%+v err=%v", ended, status, err)
			}
			var detail dto.GetSubmissionDetailsResponse
			data := request(sc.GetSubmissionDetails, tc.uid, tc.id, nil)
			if err := json.Unmarshal(data, &detail); err != nil || detail.Status != wantStatus || len(detail.Option) != 1 {
				t.Fatalf("ended=%v details=%s err=%v", ended, data, err)
			}
			var history dto.ListProblemSubmissionsResponse
			if err := json.Unmarshal(request(sc.ListUserSubmissions, tc.uid, "", &dto.ListProblemSubmissionsRequest{ProblemID: mcqProblem}), &history); err != nil || len(history.Submissions) != 1 || history.Submissions[0].Status != wantStatus {
				t.Fatalf("ended=%v history=%+v err=%v", ended, history, err)
			}
			var problems []dto.ProblemOverview
			if err := json.Unmarshal(request(cc.GetContestProblemsList, tc.uid, contest, nil), &problems); err != nil {
				t.Fatal(err)
			}
			for _, p := range problems {
				want := "solved"
				if p.ID == mcqProblem {
					want = wantProgress
				}
				if p.Progress != want {
					t.Fatalf("ended=%v problem=%s progress=%s want=%s", ended, p.ID, p.Progress, want)
				}
			}
			stored, err := storage.Submissions.GetSubmissionStatusByID(ctx, tc.id)
			if err != nil || stored.Status != tc.verdict {
				t.Fatalf("stored verdict changed: %+v %v", stored, err)
			}
		}
		var codeStatus struct {
			Status models.SubmissionStatus `json:"status"`
		}
		if err := json.Unmarshal(request(sc.GetSubmissionStatus, user, codeSubmission, nil), &codeStatus); err != nil || codeStatus.Status != models.Accepted {
			t.Fatalf("code verdict changed: %+v %v", codeStatus, err)
		}
		// Warm both cache variants in alternating order; admin data must not leak to guests.
		for _, viewer := range []string{"", user, other, ""} {
			var entries []dto.LeaderboardEntry
			if err := json.Unmarshal(request(cc.GetLeaderboard, viewer, contest, nil), &entries); err != nil || len(entries) != 2 {
				t.Fatalf("leaderboard=%+v %v", entries, err)
			}
			for _, entry := range entries {
				full := ended || viewer == user
				wantScore, wantRank, wantSolved := 10, 1, 1
				if full {
					if entry.UserID == user {
						wantScore, wantSolved = 30, 2
					} else {
						wantRank = 2
					}
				}
				if entry.TotalScore != wantScore || entry.Rank != wantRank || entry.ProblemsSolved != wantSolved {
					t.Fatalf("ended=%v viewer=%q entry=%+v, want score/rank/solves=%d/%d/%d", ended, viewer, entry, wantScore, wantRank, wantSolved)
				}
				if !full && entry.LastSubmissionTime != 100_000 {
					t.Fatalf("MCQ solve time leaked: %+v", entry)
				}
				if viewer != user && len(entry.ProblemScores) != 0 {
					t.Fatal("non-admin breakdown leaked")
				}
				if viewer == user && len(entry.ProblemScores) != 2 {
					t.Fatal("admin breakdown missing")
				}
			}
		}
	}
}
