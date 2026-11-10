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
	}))

	// Enrich Sentry scope with route tags and capture unhandled 5xx errors
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			started := time.Now()
			defer func() {
				if recovered := recover(); recovered != nil {
					c.Response().Status = http.StatusInternalServerError
					panic(recovered)
				}
			}()
			hub := sentryecho.GetHubFromContext(c)
			var previousEventID sentry.EventID
			if hub != nil {
				previousEventID = hub.LastEventID()
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

			if hub != nil && hub.LastEventID() == previousEventID {
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
						scope.SetLevel(sentry.LevelError)
						scope.SetTag("error_type", "server_error_status")
						scope.SetTag("status_code", fmt.Sprintf("%d", c.Response().Status))
						hub.CaptureMessage(fmt.Sprintf("HTTP %d on %s %s", c.Response().Status, c.Request().Method, c.Path()))
					})
				}
			}

			// Only sampled requests and server failures need a remote request log.
			span := sentryecho.GetSpanFromContext(c)
			status := c.Response().Status
			if err != nil && !c.Response().Committed {
				status = http.StatusInternalServerError
				var he *echo.HTTPError
				if errors.As(err, &he) {
					status = he.Code
				}
			}
			if span != nil && c.Path() != "" && c.Path() != "/health" && c.Request().Method != echo.OPTIONS && (span.Sampled == sentry.SampledTrue || status >= 500) {
				entry := sentry.NewLogger(span.Context()).Info()
				if status >= 500 {
					entry = sentry.NewLogger(span.Context()).Error()
				}
				entry.String("http.route", c.Path()).String("http.request.method", c.Request().Method).
					Int("http.response.status_code", status).Int64("duration_ms", time.Since(started).Milliseconds()).Emit("HTTP request completed")
			}
			return err
		}
	})

	e.Use(mdw.LoggerWithConfig(mdw.LoggerConfig{Format: `{"time":"${time_rfc3339}","id":"${id}","remote_ip":"${remote_ip}","host":"${host}","method":"${method}","uri":"${path}","user_agent":"${user_agent}","status":${status},"error":"${error}","latency":${latency},"latency_human":"${latency_human}","bytes_in":${bytes_in},"bytes_out":${bytes_out}}` + "\n"}))
	e.Use(mdw.CORSWithConfig(mdw.CORSConfig{
		AllowOrigins: []string{
			"https://recruitment.pointblank.club",
			"https://recruitment1.pointblank.club",
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
