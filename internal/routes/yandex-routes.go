package routes

import (
	"app/internal/controllers"
	"app/internal/middleware"
	"app/internal/models/dto"

	"firebase.google.com/go/v4/auth"
	"github.com/labstack/echo/v4"
)

// AddYandexRoutes serves the candidate's backup Yandex connection (see recruitment-backup).
func AddYandexRoutes(
	e *echo.Echo,
	authClient *auth.Client,
	yandexController *controllers.YandexController,
	limiter middleware.RequestLimiter,
) {
	e.GET("/yandex/connection",
		yandexController.GetConnection,
		middleware.RequireFirebaseAuth(authClient),
		middleware.LimitReads(limiter),
	)

	e.POST("/yandex/connection/start",
		yandexController.StartConnection,
		middleware.RequireFirebaseAuth(authClient),
		middleware.LimitRequests(limiter, "yandex-connect", 30, 600),
		middleware.ValidateRequest(new(dto.StartYandexConnectionRequest)),
	)

	e.POST("/yandex/connection/complete",
		yandexController.CompleteConnection,
		middleware.RequireFirebaseAuth(authClient),
		middleware.LimitRequests(limiter, "yandex-connect", 30, 600),
		middleware.ValidateRequest(new(dto.CompleteYandexConnectionRequest)),
	)
}
