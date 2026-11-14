package routes

import (
	"app/internal/controllers"
	"app/internal/middleware"
	"app/internal/models/dto"

	"firebase.google.com/go/v4/auth"
	"github.com/labstack/echo/v4"
)

func AddUserRoutes(
	e *echo.Echo,
	authClient *auth.Client,
	userController *controllers.UserController,
	limiter middleware.RequestLimiter,
) {
	e.POST("/auth/signup",
		userController.Signup,
		middleware.ValidateRequest(new(dto.SignupRequest)),
		middleware.LimitRequests(limiter, "signup-ip", 1200, 3600),
		middleware.SignupCapacity(),
		middleware.LimitRequests(limiter, "signup-fleet", 100, 10),
	)

	e.POST("/users/create",
		userController.CreateUser,
		middleware.RequireFirebaseAuth(authClient),
		middleware.LimitRequests(limiter, "user-write", 60, 60),
		middleware.ValidateRequest(new(dto.CreateUserRequest)),
	)

	e.GET("/users/profile",
		userController.GetUserProfile,
		middleware.RequireFirebaseAuth(authClient),
		middleware.LimitRequests(limiter, "user-read", 180, 60),
	)

	e.POST("/users/profile",
		userController.UpdateUserProfile,
		middleware.RequireFirebaseAuth(authClient),
		middleware.LimitRequests(limiter, "user-write", 60, 60),
		middleware.ValidateRequest(new(dto.UpdateUserProfileRequest)),
	)
}
