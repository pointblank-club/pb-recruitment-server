package internal

import (
	"app/internal/controllers"
	"app/internal/middleware"
	"context"
	"database/sql"
	"log"
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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"
)

func NewEchoServer(
	authClient *auth.Client,
	contestController *controllers.ContestController,
	database *sql.DB,
) *echo.Echo {
	e := echo.New()

	// 1. Panic recovery & Sentry error reporting
	e.Use(mdw.Recover())

	if dsn := os.Getenv("SENTRY_BACKEND_DSN"); dsn != "" {
		err := sentry.Init(sentry.ClientOptions{
			Dsn:              dsn,
			Environment:      os.Getenv("STAGE"),
			TracesSampleRate: 0.2,
			AttachStacktrace: true,
		})
		if err != nil {
			log.Printf("Sentry initialization failed: %v", err)
		} else {
			e.Use(sentryecho.New(sentryecho.Options{
				Repanic: true,
			}))
			log.Println("Sentry initialized for Echo server")
		}
	}

	// 2. Metrics Middleware (records throughput and duration per normalized route)
	e.Use(middleware.PrometheusMetrics())

	// 3. Structured Logging (stdout picked up by Promtail -> Loki)
	e.Use(mdw.LoggerWithConfig(mdw.LoggerConfig{Format: `{"time":"${time_rfc3339}","id":"${id}","remote_ip":"${remote_ip}","host":"${host}","method":"${method}","uri":"${path}","user_agent":"${user_agent}","status":${status},"error":"${error}","latency":${latency},"latency_human":"${latency_human}","bytes_in":${bytes_in},"bytes_out":${bytes_out}}` + "\n"}))

	// 4. CORS
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

	// Register database connection pool stats with Prometheus
	if database != nil {
		_ = prometheus.Register(collectors.NewDBStatsCollector(database, "pb_db"))
	}

	// Swagger UI - only exposed in development environment or when explicitly enabled
	if os.Getenv("STAGE") == "dev" || os.Getenv("ENABLE_SWAGGER") == "true" {
		e.GET("/swagger/*", echoSwagger.WrapHandler)
	}

	// Expose Prometheus /metrics endpoint (for Prometheus scraping)
	e.GET("/metrics", echo.WrapHandler(promhttp.Handler()))

	// Health check endpoint (for Uptime Kuma and orchestrators)
	// Pings the database to ensure both API and DB connectivity are healthy
	healthHandler := func(c echo.Context) error {
		if database != nil {
			ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
			defer cancel()
			if err := database.PingContext(ctx); err != nil {
				return c.JSON(http.StatusServiceUnavailable, map[string]string{
					"status":  "unhealthy",
					"error":   "database unreachable: " + err.Error(),
					"service": "pb-recruitment-server",
				})
			}
		}
		return c.JSON(http.StatusOK, map[string]string{
			"status":  "healthy",
			"service": "pb-recruitment-server",
		})
	}
	e.GET("/health", healthHandler)
	e.GET("/healthz", healthHandler)

	// Test endpoint to verify Sentry integration (only available outside production)
	if os.Getenv("STAGE") != "production" {
		e.GET("/debug/sentry-test", func(c echo.Context) error {
			if os.Getenv("SENTRY_BACKEND_DSN") == "" {
				return c.JSON(http.StatusBadRequest, map[string]string{
					"status":  "error",
					"message": "SENTRY_BACKEND_DSN is not set in environment or .env",
				})
			}
			hub := sentryecho.GetHubFromContext(c)
			if hub != nil {
				hub.CaptureMessage("Sentry integration verified successfully on PB Recruitment Server!")
			} else {
				sentry.CaptureMessage("Sentry integration verified successfully on PB Recruitment Server!")
			}
			sentry.Flush(2 * time.Second)
			return c.JSON(http.StatusOK, map[string]string{
				"status":  "success",
				"message": "Test event dispatched to Sentry! Check your Sentry.io dashboard under Issues.",
			})
		})
	}

	return e
}

func StartEchoServer(lc fx.Lifecycle, e *echo.Echo) *echo.Echo {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go e.Start(":8080")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if dsn := os.Getenv("SENTRY_BACKEND_DSN"); dsn != "" {
				sentry.Flush(2 * time.Second)
			}
			return e.Shutdown(ctx)
		},
	})

	return e
}
