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
	"log"

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

	fx.New(
		fx.Provide(
			boot.NewFirebaseAuth,
			controllers.NewContestController,
			controllers.NewUserController,
			controllers.NewSubmissionController,
			services.NewContestService,
			services.NewUserService,
			services.NewSubmissionService,
			services.NewAdminService,
			internal.NewEchoServer,
			stores.NewStorage,
			db.NewDBConn,
			s3.NewS3Client,
			judge0.NewClient,
		),
		fx.Invoke(routes.AddUserRoutes),
		fx.Invoke(routes.AddContestRoutes),
		fx.Invoke(routes.AddSubmissionRoutes),
		fx.Invoke(routes.AddAdminRoutes),
		fx.Invoke(internal.StartEchoServer),
	).Run()
}
