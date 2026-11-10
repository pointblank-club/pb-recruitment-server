package main

import (
	"app/internal"
	"app/internal/boot"
	"app/internal/controllers"
	"app/internal/db"
	"app/internal/judge0"
	"app/internal/routes"
	"app/internal/s3"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"log"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	"go.uber.org/fx"
)

// @title                      Point Blank Recruitment API
// @version                    1.0
// @description                Backend API documentation for Point Blank recruitment portal, contests, submissions, and admin services.
// @termsOfService            https://recruitment.pointblank.club

// @contact.name              Point Blank Tech Team
// @contact.url               https://pointblank.club

// @BasePath                   /

// @securityDefinitions.apikey BearerAuth
// @in                         header
// @name                       Authorization
// @description                Type "Bearer " followed by your Firebase ID token.

func main() {
	if err := boot.LoadEnv(); err != nil {
		log.Fatal(err)
	}

	if err := boot.InitSentry(); err != nil {
		log.Printf("[Sentry] Initialization error: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			sentry.CurrentHub().Clone().Recover(r)
			boot.FlushSentry(2 * time.Second)
			panic(r)
		}
	}()

	app := fx.New(
		fx.Provide(
			boot.NewFirebaseAuth,
			controllers.NewContestController,
			controllers.NewUserController,
			controllers.NewSubmissionController,
			services.NewContestService,
			services.NewUserService,
			services.NewSubmissionService,
			services.NewAdminService,
			services.NewDolosService,
			internal.NewEchoServer,
			stores.NewStorage,
			db.NewDBConn,
			s3.NewS3Client,
			judge0.NewClient,
		),
		fx.Invoke(routes.AddUserRoutes),
		fx.Invoke(routes.AddContestRoutes),
		fx.Invoke(routes.AddSubmissionRoutes),
		fx.Invoke(services.NewJudge0Recovery),
		fx.Invoke(services.NewDolosWorker),
		fx.Invoke(routes.AddAdminRoutes),
		fx.Invoke(internal.StartEchoServer),
	)
	if code := run(app); code != 0 {
		os.Exit(code)
	}
}

func run(app *fx.App) int {
	// Fx.Run exits on lifecycle errors before main's defers can flush telemetry.
	defer boot.FlushSentry(2 * time.Second)
	startCtx, cancelStart := context.WithTimeout(context.Background(), app.StartTimeout())
	err := app.Start(startCtx)
	cancelStart()
	if err != nil {
		log.Printf("Application startup failed: %v", err)
		return 1
	}

	signal := <-app.Wait()
	stopCtx, cancelStop := context.WithTimeout(context.Background(), app.StopTimeout())
	err = app.Stop(stopCtx)
	cancelStop()
	if err != nil {
		log.Printf("Application shutdown failed: %v", err)
		return 1
	}
	return signal.ExitCode
}
