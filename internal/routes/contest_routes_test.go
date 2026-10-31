package routes

import (
	"app/internal/controllers"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

type endedContestStore struct{ *stores.ContestStore }

func (endedContestStore) GetContest(context.Context, string) (*dto.GetContestResponse, error) {
	now := time.Now().UnixMilli()
	return &dto.GetContestResponse{Contest: models.Contest{StartTime: now - 120_000, EndTime: now - 60_000}}, nil
}

type mcqProblemStore struct{ *stores.ProblemStore }

func (mcqProblemStore) GetProblemList(context.Context, string) ([]dto.ProblemOverview, error) {
	return []dto.ProblemOverview{{ID: "p1"}}, nil
}

func (mcqProblemStore) GetProblem(context.Context, string, string) (*dto.GetProblemStatementResponse, error) {
	return &dto.GetProblemStatementResponse{ProblemID: "p1", Type: models.MCQ}, nil
}

// Guests reach ended-contest problems with no Authorization header.
func TestProblemRoutesAllowGuestsAfterContestEnds(t *testing.T) {
	cs := services.NewContestService(&stores.Storage{Contests: endedContestStore{}, Problems: mcqProblemStore{}}, nil)
	e := echo.New()
	AddContestRoutes(e, nil, controllers.NewContestController(cs, nil))
	for _, path := range []string{"/contests/c1/problems", "/contests/c1/problems/p1"} {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200: %s", path, w.Code, w.Body)
		}
	}
}
