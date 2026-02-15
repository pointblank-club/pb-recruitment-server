package routes

import (
	"app/internal/controllers"
	"app/internal/middleware"
	"app/internal/models/dto"
	"app/internal/services"

	"net/http"

	"firebase.google.com/go/v4/auth"
	"github.com/labstack/echo/v4"
)

func AddAdminRoutes(
	e *echo.Echo,
	contestController *controllers.ContestController,
	authClient *auth.Client,
	userService *services.UserService,
	adminService *services.AdminService,
) {
	adminGroup := e.Group("/admin")
	adminGroup.Use(middleware.RequireFirebaseAuth(authClient))
	adminGroup.Use(middleware.RequireAdminRole(userService, adminService))

	// Check Is Admin
	adminGroup.GET("/", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	//Contest Management
	adminGroup.GET("/contests/list", contestController.ListContests)
	adminGroup.GET("/contest/:id", contestController.GetContest)
	adminGroup.POST("/contest", contestController.HandleCreateContest, middleware.ValidateRequest(new(dto.UpsertContestRequest)))
	adminGroup.PUT("/contest/:id", contestController.HandleUpdateContest, middleware.ValidateRequest(new(dto.UpsertContestRequest)))
	adminGroup.DELETE("/contest/:id", contestController.HandleDeleteContest)

	//Problem Management
	adminGroup.POST("/:contestid/problem", contestController.HandleCreateProblem)
	adminGroup.PUT("/:contestid/:problemid", contestController.HandleUpdateProblem)
	adminGroup.DELETE("/:contestid/:problemid", contestController.HandleDeleteProblem)
	adminGroup.GET("/:contestid/:problemid/testcases", contestController.GetProblemTestcases)
	adminGroup.GET("/:contestid/:problemid/answers", contestController.GetProblemAnswers)

	//Leaderboard/User Management
	adminGroup.PUT("/:contestid/leaderboard/:userid", contestController.HandleUpdateLeaderboardUser)

	adminGroup.GET("/contests/:contestId/registrations", contestController.GetContestRegistrations)
}
