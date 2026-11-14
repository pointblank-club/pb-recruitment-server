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
	now := time.Now().Unix()
	if _, err := db.Exec(`UPDATE contests SET start_time=$2 WHERE id=$1`, contest, (now-240)*1000); err != nil {
		t.Fatal(err)
	}
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
	// Recent coding solves remain visible before the next MCQ batch.
	if _, err := db.Exec(`UPDATE submissions SET created_at=$2 WHERE id=$1`, otherCode, now-10); err != nil {
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
	correctAt, mcqOnlyAt := now-180, now-30
	if _, err := db.Exec(`UPDATE submissions SET created_at=$2 WHERE id=$1`, correct, correctAt); err != nil {
		t.Fatal(err)
	}
	mcqOnly := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,name,email,usn,current_year,department) VALUES($1,'MCQ only',$2,$1,1,'CS')`, mcqOnly, mcqOnly+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO contest_registrations(contest_id,user_id,registered_at) VALUES($1,$2,1)`, contest, mcqOnly); err != nil {
		t.Fatal(err)
	}
	mcqOnlySubmission, err := storage.Submissions.CreateMCQSubmission(ctx, &models.Submission{UserID: mcqOnly, ContestID: contest, ProblemID: mcqProblem, Type: models.MCQ, Option: []int{1}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Submissions.JudgeMCQ(ctx, mcqOnlySubmission); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE submissions SET created_at=$2 WHERE id=$1`, mcqOnlySubmission, mcqOnlyAt); err != nil {
		t.Fatal(err)
	}
	// Model verdicts finalized at the historical submission times.
	for id, finalized := range map[string]int64{correct: correctAt, mcqOnlySubmission: mcqOnlyAt} {
		if _, err := db.Exec(`UPDATE mcq_verdict_history SET finalized_at=$2 WHERE submission_id=$1`, id, finalized*1000); err != nil {
			t.Fatal(err)
		}
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
	for _, stage := range []string{"live", "consolidated", "batched", "ended"} {
		ended := stage == "ended"
		if stage == "consolidated" {
			if _, err := storage.Rankings.ConsolidateContest(ctx, contest, false, true); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "batched" {
			// Advance the batch boundary without waiting or invalidating the warm live cache.
			if _, err := db.Exec(`UPDATE contests SET start_time=$2 WHERE id=$1`, contest, (now-360)*1000); err != nil {
				t.Fatal(err)
			}
		}
		if ended {
			if _, err := db.Exec(`UPDATE contests SET end_time=$2 WHERE id=$1`, contest, time.Now().Add(-time.Second).UnixMilli()); err != nil {
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
		// Share the cache across viewers without exposing admin-only breakdowns.
		for _, viewer := range []string{"", user, other, ""} {
			var entries []dto.LeaderboardEntry
			if err := json.Unmarshal(request(cc.GetLeaderboard, viewer, contest, nil), &entries); err != nil || len(entries) != 3 {
				t.Fatalf("leaderboard=%+v %v", entries, err)
			}
			wantOrder := []string{user, other, mcqOnly}
			if viewer == user || ended {
				wantOrder = []string{user, mcqOnly, other}
			} else if stage == "batched" {
				wantOrder = []string{user, other, mcqOnly}
			}
			if entries[0].UserID != wantOrder[0] || entries[1].UserID != wantOrder[1] || entries[2].UserID != wantOrder[2] {
				t.Fatalf("stage=%s viewer=%q unexpected leaderboard order: %+v", stage, viewer, entries)
			}
			for _, entry := range entries {
				wantScore, wantRank, wantSolved, wantTime := 10, 3, 1, (now-10)*1000
				wantBreakdowns := 2
				switch entry.UserID {
				case user:
					wantScore, wantRank, wantSolved, wantTime = 30, 1, 2, correctAt*1000
				case mcqOnly:
					wantScore, wantRank, wantSolved, wantTime = 20, 2, 1, mcqOnlyAt*1000
					wantBreakdowns = 1
				}
				if viewer != user && !ended {
					switch entry.UserID {
					case mcqOnly:
						wantScore, wantRank, wantSolved, wantTime = 0, 3, 0, 0
					case user:
						if stage != "batched" {
							wantScore, wantSolved, wantTime = 10, 1, 100_000
						}
					case other:
						wantRank = 2
					}
				}
				if entry.TotalScore != wantScore || entry.Rank != wantRank || entry.ProblemsSolved != wantSolved || entry.LastSubmissionTime != wantTime {
					t.Fatalf("stage=%s viewer=%q entry=%+v, want score/rank/solves/time=%d/%d/%d/%d", stage, viewer, entry, wantScore, wantRank, wantSolved, wantTime)
				}
				if viewer != user && len(entry.ProblemScores) != 0 {
					t.Fatal("non-admin breakdown leaked")
				}
				if viewer == user && len(entry.ProblemScores) != wantBreakdowns {
					t.Fatal("admin breakdown missing")
				}
			}
		}
	}
	// A new batch must bypass an unexpired cache entry without accumulating keys per batch.
	for _, tc := range []struct {
		cutoff                  int64
		userScore, mcqOnlyScore int
	}{
		{correctAt, 10, 0},
		{correctAt + 1, 30, 0},
		{mcqOnlyAt + 1, 30, 20},
		{correctAt, 10, 0},
	} {
		res, err := storage.Rankings.GetLiveLeaderboard(ctx, contest, 0, tc.cutoff*1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range res.Entries {
			if entry.UserID == user && entry.TotalScore != tc.userScore || entry.UserID == mcqOnly && entry.TotalScore != tc.mcqOnlyScore {
				t.Fatalf("cutoff=%d unexpected cached totals: %+v", tc.cutoff, res.Entries)
			}
		}
	}
}
