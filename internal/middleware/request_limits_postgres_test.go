package middleware

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestRequestLimitPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL instance")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := "request_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	db.SetMaxOpenConns(20)
	migration, err := os.ReadFile("../migrations/000040_request_rate_limits.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const n = 40
	results := make(chan int, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			retry, err := NewRequestLimiter(db).Allow(ctx, "same-user", 7, 60)
			results <- retry
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	admitted := 0
	for retry := range results {
		if retry == 0 {
			admitted++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if admitted != 7 {
		t.Fatal("concurrent admissions", admitted)
	}
	l := NewRequestLimiter(db)
	if retry, err := l.Allow(ctx, "same-user", 7, 60); retry != 60 || err != nil {
		t.Fatal(retry, err)
	}
	if _, err := db.Exec(`UPDATE request_rate_limits SET resets_at=clock_timestamp()-interval '1 second' WHERE key=$1`, "same-user"); err != nil {
		t.Fatal(err)
	}
	if retry, err := l.Allow(ctx, "same-user", 7, 60); retry != 0 || err != nil {
		t.Fatal(retry, err)
	}
	if _, err := db.Exec(`UPDATE request_rate_limits SET hits=7,resets_at=clock_timestamp()+interval '1 second' WHERE key='same-user'`); err != nil {
		t.Fatal(err)
	}
	if retry, err := l.Allow(ctx, "same-user", 7, 3600); err != nil || retry != 1 {
		t.Fatal("remaining Retry-After", retry, err)
	}
	key := `';DROP TABLE request_rate_limits;--`
	if retry, err := l.Allow(ctx, key, 1, 60); retry != 0 || err != nil {
		t.Fatal(retry, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT key FROM request_rate_limits WHERE key=$1`, key).Scan(&stored); err != nil || stored != key {
		t.Fatal(stored, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Allow(ctx, "closed", 1, 60); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}
