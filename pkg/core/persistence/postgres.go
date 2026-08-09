package persistence

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Default pool sizing. Postgres has no writer-serialization problem, so unlike
// SQLite there is no single-connection write handle: reads and writes share one
// pool and the manager hands the same reference to both roles.
const (
	postgresMaxOpenConns    = 16
	postgresMaxIdleConns    = 8
	postgresConnMaxLifetime = 30 * time.Minute
)

// openPostgres dials the configured server and returns one pool used for both
// reads and writes.
func openPostgres(dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres selected but no connection string configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(postgresMaxOpenConns)
	db.SetMaxIdleConns(postgresMaxIdleConns)
	db.SetConnMaxLifetime(postgresConnMaxLifetime)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return db, nil
}
