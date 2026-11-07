package internal

import (
	"app/internal/controllers"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	_ "app/docs"
	echoSwagger "github.com/swaggo/echo-swagger"

	"firebase.google.com/go/v4/auth"
	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
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
	e.Use(sentryecho.New(sentryecho.Options{
		Repanic: true,
		Timeout: 2 * time.Second,
	}))

	// Enrich Sentry scope with route tags and capture unhandled 5xx errors
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			hub := sentryecho.GetHubFromContext(c)
			if hub != nil {
				hub.ConfigureScope(func(scope *sentry.Scope) {
					if path := c.Path(); path != "" {
						scope.SetTag("route", path)
					}
					for _, name := range c.ParamNames() {
						val := c.Param(name)
						if val != "" {
							scope.SetTag(name, val)
						}
					}
				})
			}

			err := next(c)

			if hub != nil {
				if err != nil {
					var he *echo.HTTPError
					if errors.As(err, &he) {
						if he.Code >= 500 {
							hub.WithScope(func(scope *sentry.Scope) {
								scope.SetTag("error_type", "http_error")
								scope.SetTag("status_code", fmt.Sprintf("%d", he.Code))
								hub.CaptureException(err)
							})
						}
					} else {
						hub.WithScope(func(scope *sentry.Scope) {
							scope.SetTag("error_type", "unhandled_error")
							hub.CaptureException(err)
						})
					}
				} else if c.Response().Status >= 500 {
					hub.WithScope(func(scope *sentry.Scope) {
						scope.SetTag("error_type", "server_error_status")
						scope.SetTag("status_code", fmt.Sprintf("%d", c.Response().Status))
						hub.CaptureMessage(fmt.Sprintf("HTTP %d on %s %s", c.Response().Status, c.Request().Method, c.Path()))
					})
				}
			}

			return err
		}
	})

	e.Use(mdw.LoggerWithConfig(mdw.LoggerConfig{Format: `{"time":"${time_rfc3339}","id":"${id}","remote_ip":"${remote_ip}","host":"${host}","method":"${method}","uri":"${path}","user_agent":"${user_agent}","status":${status},"error":"${error}","latency":${latency},"latency_human":"${latency_human}","bytes_in":${bytes_in},"bytes_out":${bytes_out}}` + "\n"}))
	e.Use(mdw.CORSWithConfig(mdw.CORSConfig{
		AllowOrigins: []string{
			"https://recruitment.pointblank.club",
			"http://recruitment.pointblank.club",
			"https://pb-recruitment-website-green.vercel.app",
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
			echo.HeaderRetryAfter,
			"X-Total-Count",
			"X-Total-Pages",
			"X-Current-Page",
			"X-Truncated",
			"X-Max-Pairs-Per-Report",
			"X-Min-Similarity",
		},
		AllowCredentials: true,
	}))

	// Swagger UI - only exposed in development environment or when explicitly enabled
	if os.Getenv("STAGE") == "dev" || os.Getenv("ENABLE_SWAGGER") == "true" {
		e.GET("/swagger/*", echoSwagger.WrapHandler)
	}

	// Health check endpoint
	// This can be used by Kubernetes or any load balancer
	// to check if the server is running and healthy
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(200, map[string]string{
			"status": "ok",
		})
	})

	// Test endpoint to verify Sentry end-to-end (panics, errors, messages)
	// Gated in production unless ENABLE_SENTRY_TEST=true
	e.GET("/debug/sentry-test", func(c echo.Context) error {
		if os.Getenv("STAGE") == "production" && os.Getenv("ENABLE_SENTRY_TEST") != "true" {
			return c.JSON(http.StatusForbidden, map[string]string{
				"error": "debug endpoint disabled in production (set ENABLE_SENTRY_TEST=true to enable)",
			})
		}

		testType := c.QueryParam("type")
		switch testType {
		case "panic":
			panic("Sentry E2E Test: unhandled panic")
		case "error":
			testErr := errors.New("sentry E2E Test: captured error")
			if hub := sentryecho.GetHubFromContext(c); hub != nil {
				hub.CaptureException(testErr)
			} else {
				sentry.CaptureException(testErr)
			}
			sentry.Flush(2 * time.Second)
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"status":  "error_triggered",
				"message": testErr.Error(),
			})
		case "message":
			if hub := sentryecho.GetHubFromContext(c); hub != nil {
				hub.CaptureMessage("Sentry E2E Test: test message")
			} else {
				sentry.CaptureMessage("Sentry E2E Test: test message")
			}
			sentry.Flush(2 * time.Second)
			return c.JSON(http.StatusOK, map[string]string{
				"status": "message_sent",
			})
		default:
			return c.JSON(http.StatusOK, map[string]string{
				"status":  "ready",
				"usage":   "/debug/sentry-test?type=panic|error|message",
				"warning": "type=panic triggers a panic; type=error captures an exception; type=message sends an info message",
			})
		}
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
			sentry.Flush(2 * time.Second)
			return e.Shutdown(ctx)
		},
	})

	return e
}
