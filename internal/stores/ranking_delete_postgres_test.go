package stores_test

import (
	"app/internal/controllers"
	"app/internal/services"
	"app/internal/stores"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/labstack/echo/v4"
)

type deleteContestBeforeRankingInsert struct {
	deleteContest func()
}

func (tracer deleteContestBeforeRankingInsert) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	if strings.Contains(query.SQL, "INSERT INTO rankings") {
		tracer.deleteContest()
	}
	return ctx
}

func (deleteContestBeforeRankingInsert) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func TestModerationConcurrentContestDeletionPostgres(t *testing.T) {
	db := postgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	user, contest := "firebaseUidExample28Characters", "deleted-contest"
	for _, query := range []string{
		`INSERT INTO users(id,name,email,usn,current_year,department) VALUES('firebaseUidExample28Characters','Test','test@example.test','USN',1,'CS')`,
		`INSERT INTO contests(id,name,description,registration_start_time,registration_end_time,start_time,end_time) VALUES('deleted-contest','Test','',1,2,3,4)`,
		`INSERT INTO contest_registrations(contest_id,user_id,registered_at) VALUES('deleted-contest','firebaseUidExample28Characters',1)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := db.QueryRowContext(ctx, `SHOW search_path`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	deleted := false
	// Commit deletion on another connection after participant validation, before INSERT.
	cfg.Tracer = deleteContestBeforeRankingInsert{deleteContest: func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM contests WHERE id=$1`, contest); err != nil {
			t.Fatal(err)
		}
		deleted = true
	}}
	moderationDB := stdlib.OpenDB(*cfg)
	defer moderationDB.Close()
	controller := controllers.NewContestController(services.NewContestService(&stores.Storage{Rankings: stores.NewRankingStore(moderationDB)}, nil))
	req := httptest.NewRequest(http.MethodPut, "/admin/"+contest+"/leaderboard/"+user, strings.NewReader(`{"hidden":true}`)).WithContext(ctx)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	requestCtx := echo.New().NewContext(req, recorder)
	requestCtx.SetParamNames("contestid", "userid")
	requestCtx.SetParamValues(contest, user)
	if err := controller.HandleUpdateLeaderboardUser(requestCtx); err != nil {
		t.Fatal(err)
	}
	if !deleted || recorder.Code != http.StatusNotFound {
		t.Fatalf("deleted=%v HTTP %d, want 404: %s", deleted, recorder.Code, recorder.Body.String())
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM rankings WHERE contest_id=$1`, contest).Scan(&count); err != nil || count != 0 {
		t.Fatalf("remaining rankings=%d, error=%v", count, err)
	}
}
