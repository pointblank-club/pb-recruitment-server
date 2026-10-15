package stores

import (
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"database/sql"
	"firebase.google.com/go/v4/auth"
)

type Storage struct {
	// Declarations of method extensions for each store go here
	Contests interface {
		ListContests(context.Context, int) ([]models.Contest, error)
		IsRegistered(context.Context, string, string) (bool, error)
		CreateContest(ctx context.Context, c *models.Contest) error
		UpdateContest(ctx context.Context, c *models.Contest) error
		DeleteContest(ctx context.Context, contestID string) error
		GetContest(context.Context, string) (*dto.GetContestResponse, error)
		RegisterUser(context.Context, string, string) error
		UnregisterUser(context.Context, string, string) error
		GetContestRegistrations(context.Context, string) ([]dto.ContestRegistration, error)
	}
	Users interface {
		CreateUser(context.Context, *auth.UserRecord, *dto.CreateUserRequest) error
		GetUserProfile(context.Context, string) (*models.User, error)
		UpdateUserProfile(context.Context, string, *dto.UpdateUserProfileRequest) error
	}
	Submissions interface {
		GetSubmissionStatusByID(context.Context, string) (*models.Submission, error)
		GetSubmissionDetailsByID(context.Context, string) (*dto.GetSubmissionDetailsResponse, error)
		GetTestCaseResultsBySubmissionID(context.Context, string) ([]models.TestCaseResult, error)
		ListUserSubmissionsByProblemID(context.Context, string, string, int) ([]models.Submission, error)
		CreateSubmission(context.Context, *models.Submission) (string, error)
		JudgeMCQ(context.Context, string) error
		MarkFailed(context.Context, string) error
		PendingCodeWithoutExecutions(context.Context, int64, int) ([]string, error)
		CheckSubmissionRateLimit(context.Context, string, int64) error
	}
	Executions interface {
		InsertBatch(ctx context.Context, submissionID string, indexes []int, jobs ...[]judge0.SubmissionRequest) ([]models.Execution, error)
		ClaimRecovery(context.Context, int) ([]models.Execution, error)
		ReleaseRecovery(context.Context, models.Execution, bool) error
		SaveAttemptToken(context.Context, string, string, string) error
		SaveRecoveryPayload(context.Context, models.Execution, judge0.SubmissionRequest) error
		ProcessFinal(ctx context.Context, result FinalExecutionResult) error
		TerminalPendingParents(ctx context.Context, limit int) ([]FinalExecutionResult, error)
	}
	Rankings interface {
		UpdateLeaderboardUser(ctx context.Context, contestID string, userID string, req *dto.UpdateLeaderboardUserRequest) error
		GetLeaderboard(ctx context.Context, contestID string, page int) (*dto.GetLeaderboardResponse, error)
		ConsolidateContest(ctx context.Context, contestID string, regradeMCQs bool, force bool) (*dto.ConsolidationResponse, error)
		InvalidateCache(contestID string)
	}
	Problems interface {
		CreateProblem(ctx context.Context, p *models.Problem) error
		UpdateProblem(ctx context.Context, p *models.Problem) error
		DeleteProblem(ctx context.Context, contestID string, problemID string) error
		GetProblemList(ctx context.Context, contestID string) ([]dto.ProblemOverview, error)
		GetProblem(ctx context.Context, problemID string, contestID string) (*dto.GetProblemStatementResponse, error)
	}
	Admins interface {
		IsAdmin(ctx context.Context, userID string) (bool, error)
	}
}

func NewStorage(db *sql.DB) *Storage {
	return &Storage{
		Contests:    NewContestStore(db),
		Users:       NewUserStore(db),
		Submissions: NewSubmissionStore(db),
		Executions:  NewExecutionStore(db),
		Rankings:    NewRankingStore(db),
		Problems:    NewProblemStore(db),
		Admins:      NewAdminStore(db),
	}
}
