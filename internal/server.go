package internal

import (
	"app/internal/controllers"
	"context"

	_ "app/docs"
	echoSwagger "github.com/swaggo/echo-swagger"

	"firebase.google.com/go/v4/auth"
	"github.com/labstack/echo/v4"
	mdw "github.com/labstack/echo/v4/middleware"
	"go.uber.org/fx"
)

func NewEchoServer(
	authClient *auth.Client,
	contestController *controllers.ContestController,
) *echo.Echo {
	e := echo.New()
	e.Use(mdw.Recover())
	e.Use(mdw.Logger())
	e.Use(mdw.CORSWithConfig(mdw.CORSConfig{
		AllowOrigins: []string{
			"https://recruitment.pointblank.club",
			"http://recruitment.pointblank.club",
			"http://localhost:5173",
			"http://127.0.0.1:5173",
		},
		AllowMethods: []string{
			echo.GET,
			echo.POST,
			echo.PUT,
			echo.DELETE,
			echo.PATCH,
			echo.OPTIONS,
		},
		AllowHeaders: []string{
			echo.HeaderOrigin,
			echo.HeaderContentType,
			echo.HeaderAccept,
			echo.HeaderAuthorization,
		},
		ExposeHeaders: []string{
			"X-Total-Count",
			"X-Total-Pages",
			"X-Current-Page",
		},
		AllowCredentials: true,
	}))

	// Swagger UI
	e.GET("/swagger/*", echoSwagger.WrapHandler)

	// Health check endpoint
	// This can be used by Kubernetes or any load balancer
	// to check if the server is running and healthy
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(200, map[string]string{
			"status": "ok",
		})
	})

	return e
}

func StartEchoServer(lc fx.Lifecycle, e *echo.Echo) *echo.Echo {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go e.Start(":8080")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return e.Shutdown(ctx)
		},
	})

	return e
}
