package persistence

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const dbFilename = "streamnzb.db"

// sqliteDSN appends the per-connection pragmas every handle on a StreamNZB
// database must carry (the modernc driver executes each _pragma on every new
// connection, busy_timeout first).
func sqliteDSN(dbPath string) string {
	return dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

// openSQLite opens the shared read pool and a dedicated single-connection write
// handle on the same database file. Routing every write through the
// single-connection handle makes writers queue in-process instead of colliding
// inside SQLite; busy_timeout covers the remaining cross-handle overlap (a
// write committing while the read pool holds a checkpoint, startup migrations)
// by waiting instead of failing SQLITE_BUSY. Both pragmas ride the DSN because
// busy_timeout is per-connection — a plain db.Exec would only reach one pooled
// connection.
func openSQLite(dataDir string) (db, wdb *sql.DB, err error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, nil, err
	}
	dbPath := filepath.Join(dataDir, dbFilename)
	dsn := sqliteDSN(dbPath)
	db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, nil, err
	}
	wdb, err = sql.Open("sqlite", dsn)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("open sqlite writer %s: %w", dbPath, err)
	}
	wdb.SetMaxOpenConns(1)
	if err := wdb.Ping(); err != nil {
		db.Close()
		wdb.Close()
		return nil, nil, err
	}
	return db, wdb, nil
}
