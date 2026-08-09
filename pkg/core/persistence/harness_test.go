package persistence

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/core/logger"
)

// testPostgresDSNEnv names a Postgres server the suite may create throwaway
// schemas in, e.g.
//
//	STREAMNZB_TEST_POSTGRES_DSN=postgres://postgres:postgres@localhost:5432/postgres
//
// Unset — the common case — means every Postgres variant skips and only the
// SQLite variant runs.
const testPostgresDSNEnv = "STREAMNZB_TEST_POSTGRES_DSN"

// testDB is one isolated database a test may open (and reopen) a manager on.
type testDB struct {
	settings Settings
	dataDir  string
}

// open builds a manager on this database, bypassing the process-wide singleton
// so backends can be exercised independently. Calling it again after Close
// reopens the same database, which is what the persistence-across-restart tests
// rely on.
func (d testDB) open(t *testing.T) *StateManager {
	t.Helper()
	m, err := newManager(d.settings, d.dataDir)
	if err != nil {
		t.Fatalf("open %s manager: %v", d.settings.Backend, err)
	}
	return m
}

// suiteDB returns an isolated database on the backend this run targets: the
// Postgres server named by testPostgresDSNEnv when it is set, SQLite otherwise.
//
// The backend is per-run rather than a subtest of every test because wrapping
// each existing test body in a closure would mean re-indenting them, and these
// files carry raw string literals whose indentation is data. Running the suite
// twice — plain, then with the env var set — covers both backends; CI does
// exactly that.
func suiteDB(t *testing.T) testDB {
	t.Helper()
	logger.Init("DEBUG")

	if base := strings.TrimSpace(os.Getenv(testPostgresDSNEnv)); base != "" {
		return testDB{
			settings: Settings{Backend: BackendPostgres, DSN: newPostgresSchema(t, base)},
			dataDir:  t.TempDir(),
		}
	}
	return testDB{settings: Settings{Backend: BackendSQLite}, dataDir: t.TempDir()}
}

// openTestManager opens a manager on a fresh database and closes it with the test.
func openTestManager(t *testing.T) *StateManager {
	t.Helper()
	m := suiteDB(t).open(t)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

var testSchemaSeq atomic.Uint64

// newPostgresSchema creates an empty schema on the configured server and
// returns a DSN scoped to it, so concurrent runs and successive tests cannot
// see each other's tables. The schema is dropped when the test ends.
func newPostgresSchema(t *testing.T, baseDSN string) string {
	t.Helper()
	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := admin.Ping(); err != nil {
		admin.Close()
		t.Fatalf("connect postgres (%s): %v", testPostgresDSNEnv, err)
	}

	schema := testSchemaName(t.Name())
	if _, err := admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`); err != nil {
		admin.Close()
		t.Fatalf("drop schema %s: %v", schema, err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
		admin.Close()
	})
	return withSearchPath(baseDSN, schema)
}

// testSchemaName derives a valid, unique Postgres identifier from a test name.
// Postgres truncates identifiers at 63 bytes, so the counter goes first — it is
// what actually guarantees uniqueness.
func testSchemaName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	cleaned := strings.Trim(b.String(), "_")
	if len(cleaned) > 40 {
		cleaned = cleaned[:40]
	}
	return fmt.Sprintf("snzb_t%d_%s", testSchemaSeq.Add(1), cleaned)
}

// withSearchPath scopes a DSN to one schema. pgx forwards unrecognized URL
// query parameters as Postgres runtime parameters, and the keyword form takes
// the setting directly.
func withSearchPath(dsn, schema string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "search_path=" + schema
	}
	return dsn + " search_path=" + schema
}
