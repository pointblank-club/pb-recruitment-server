package routes

import (
	"app/internal/controllers"
	"app/internal/middleware"
	"app/internal/models/dto"

	"firebase.google.com/go/v4/auth"
	"github.com/labstack/echo/v4"
)

func AddContestRoutes(
	e *echo.Echo,
	authClient *auth.Client,
	contestController *controllers.ContestController,
) {
	// List all contests
	e.GET("/contests/list",
		contestController.ListContests,
		middleware.OptionalFirebaseAuth(authClient),
	)

	// Get details of a specific contest
	// If the user is authenticated, return user-specific details
	// If not, return public details
	e.GET("/contests/:id",
		contestController.GetContest,
		middleware.OptionalFirebaseAuth(authClient),
	)

	// // Get the leaderboard of a specific contest
	// // Paginate, page=<page> and 20 entries per page
	e.GET("/contests/:id/leaderboard",
		contestController.GetLeaderboard,
	)

	// Register/Unregister the authenticated user for a specific contest
	// Use a request body with action=register or action=unregister
	e.POST("/contests/:id/registration",
		contestController.ModifyRegistration,
		middleware.RequireFirebaseAuth(authClient),
		middleware.ValidateRequest(new(dto.ModifyRegistrationRequest)),
	)

	// Get the problems of a specific contest.
	// Auth is optional: any user (including unauthenticated) can see problems after the contest ends.
	e.GET("/contests/:id/problems",
		contestController.GetContestProblemsList,
		middleware.OptionalFirebaseAuth(authClient),
	)

	// Get the problem statement of a specific problem in a contest.
	// Same access rules as the problems list endpoint above.
	e.GET("/contests/:id/problems/:problem_id",
		contestController.GetContestProblem,
		middleware.OptionalFirebaseAuth(authClient),
	)
}
