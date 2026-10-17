package db

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/fx"
)

// DBConfig holds database configuration
type DBConfig struct {
	Addr            string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// LoadDBConfig loads database configuration from environment variables
func LoadDBConfig() *DBConfig {
	maxOpenConns, _ := strconv.Atoi(getEnv("DB_MAX_OPEN_CONNS", "25"))
	maxIdleConns, _ := strconv.Atoi(getEnv("DB_MAX_IDLE_CONNS", "25"))
	connMaxLifetime, _ := time.ParseDuration(getEnv("DB_CONN_MAX_LIFETIME", "5m"))

	return &DBConfig{
		Addr:            getEnv("DB_ADDR", ""),
		MaxOpenConns:    maxOpenConns,
		MaxIdleConns:    maxIdleConns,
		ConnMaxLifetime: connMaxLifetime,
	}
}

// getEnv gets an environment variable with a fallback default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func (config *DBConfig) GetConnectionString() string {
	if config.Addr == "" {
		panic("DB_ADDR environment variable is required")
	}
	return config.Addr
}

func NewDBConn(lc fx.Lifecycle) *sql.DB {
	config := LoadDBConfig()

	db, err := sql.Open("pgx", config.GetConnectionString())
	if err != nil {
		log.Fatalf("failed to open database connection: %v", err)
	}

	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := db.PingContext(ctx); err != nil {
				_ = db.Close()
				log.Fatalf("database ping failed: %v", err)
			}
			log.Println("Database connection established successfully")
			return nil
		},

		OnStop: func(ctx context.Context) error {
			if db != nil {
				return db.Close()
			}
			return nil
		},
	})

	return db
}
