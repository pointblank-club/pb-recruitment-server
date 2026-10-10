package routes

import (
	"app/internal/controllers"
	"app/internal/middleware"
	"app/internal/models/dto"
	"firebase.google.com/go/v4/auth"
	"github.com/labstack/echo/v4"
)

func AddSubmissionRoutes(
	e *echo.Echo,
	authClient *auth.Client,
	submissionController *controllers.SubmissionController,
) {
	e.PUT("/internal/judge0/callback/:execution_id", submissionController.Judge0Callback)
	// // Get the status of a specific submission
	// // The authenticated user can only get the status of their own submissions
	e.GET("/submission/:id/status",
		submissionController.GetSubmissionStatus,
		middleware.RequireFirebaseAuth(authClient),
	)

	// // Get details of a specific submission (which test case passed/failed, runtime, memory, etc.)
	// // The authenticated user can only get the details of their own submissions
	e.GET("/submission/:id/details",
		submissionController.GetSubmissionDetails,
		middleware.RequireFirebaseAuth(authClient),
	)

	// // List all submissions of the authenticated user for a specific Code problem
	// // Paginate, page=<page> and 20 entries per page
	// // Add a REQUIRED query parameter "problem_id" to filter submissions by problem
	e.GET("/submission/list",
		submissionController.ListUserSubmissions,
		middleware.RequireFirebaseAuth(authClient),
		middleware.ValidateRequest(new(dto.ListProblemSubmissionsRequest)),
	)

	// // Submit a solution to a problem in a contest
	// // The authenticated user can only submit solutions to contests they are registered in
	// // The request body should contain the contest ID, problem ID, language, and code
	// // For MCQ type questions, the request body should contain the selected option(s)
	// // The response should contain the submission ID
	e.POST("/submission/submit",
		submissionController.SubmitSolution,
		middleware.RequireFirebaseAuth(authClient),
		middleware.ValidateRequest(new(dto.SubmitSubmissionRequest)),
	)
}
