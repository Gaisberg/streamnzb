package persistence

import (
	"fmt"
	"strconv"
	"strings"
)

// Backend names accepted in configuration.
const (
	BackendSQLite   = "sqlite"
	BackendPostgres = "postgres"
)

// Canonical column-type tokens. Schema DDL is written once using these; the
// dialect expands them to concrete types. Everything integral maps to a 64-bit
// column because Postgres INTEGER is 32-bit and several columns (tried_at holds
// UnixMilli) overflow it, whereas SQLite's INTEGER is already 64-bit.
const (
	ddlIdentity = "{ID}"
	ddlInt      = "{INT}"
	ddlReal     = "{REAL}"
	ddlBlob     = "{BLOB}"
	ddlText     = "{TEXT}"
)

// Dialect isolates every SQL difference between the supported backends. Stores
// write one portable query with ? placeholders and canonical type tokens; the
// dialect renders it for the backend actually in use.
type Dialect interface {
	Name() string
	// Rebind converts ? placeholders into whatever the driver expects.
	Rebind(query string) string
	// ExpandDDL replaces the canonical type tokens with concrete column types.
	ExpandDDL(ddl string) string
	// AddColumnSQL renders one ALTER TABLE ADD COLUMN for the migration pass.
	AddColumnSQL(table, column, decl string) string
	// IsDuplicateColumn reports whether err means "that column already exists",
	// which an idempotent migration must tolerate.
	IsDuplicateColumn(err error) bool
	// TableExistsQuery returns a query (and its args) yielding one row when the
	// table exists.
	TableExistsQuery(table string) (string, []any)
	// TableColumnsQuery returns a query (and its args) yielding one row per
	// column of the table, the column name first.
	TableColumnsQuery(table string) (string, []any)
}

func dialectFor(backend string) (Dialect, error) {
	switch normalizeBackend(backend) {
	case BackendSQLite:
		return sqliteDialect{}, nil
	case BackendPostgres:
		return postgresDialect{}, nil
	default:
		return nil, fmt.Errorf("unknown database backend %q (want %q or %q)", backend, BackendSQLite, BackendPostgres)
	}
}

// normalizeBackend maps the spellings users actually write in config and env
// onto the two canonical names.
func normalizeBackend(backend string) string {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", BackendSQLite, "sqlite3":
		return BackendSQLite
	case BackendPostgres, "postgresql", "pgx", "pg":
		return BackendPostgres
	default:
		return strings.ToLower(strings.TrimSpace(backend))
	}
}

// --- SQLite ---

type sqliteDialect struct{}

var sqliteDDLTypes = strings.NewReplacer(
	ddlIdentity, "INTEGER PRIMARY KEY AUTOINCREMENT",
	ddlInt, "INTEGER",
	ddlReal, "REAL",
	ddlBlob, "BLOB",
	ddlText, "TEXT",
)

func (sqliteDialect) Name() string { return BackendSQLite }

// Rebind is a no-op: the sqlite driver already takes ? placeholders.
func (sqliteDialect) Rebind(query string) string { return query }

func (sqliteDialect) ExpandDDL(ddl string) string { return sqliteDDLTypes.Replace(ddl) }

func (sqliteDialect) AddColumnSQL(table, column, decl string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl)
}

// IsDuplicateColumn matches on the message because the sqlite driver does not
// surface a typed error for it.
func (sqliteDialect) IsDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column")
}

func (sqliteDialect) TableExistsQuery(table string) (string, []any) {
	return `SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, []any{table}
}

func (sqliteDialect) TableColumnsQuery(table string) (string, []any) {
	// pragma_table_info is a table-valued function, so the result shape matches
	// Postgres (one row per column, name first) instead of PRAGMA's six columns.
	// The table name is interpolated because pragma arguments are not bindable;
	// every caller passes a package-level constant, never user input.
	return `SELECT name FROM pragma_table_info('` + strings.ReplaceAll(table, "'", "''") + `')`, nil
}

// --- Postgres ---

type postgresDialect struct{}

var postgresDDLTypes = strings.NewReplacer(
	ddlIdentity, "BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY",
	ddlInt, "BIGINT",
	ddlReal, "DOUBLE PRECISION",
	ddlBlob, "BYTEA",
	ddlText, "TEXT",
)

func (postgresDialect) Name() string { return BackendPostgres }

func (postgresDialect) Rebind(query string) string { return rebindPositional(query) }

func (postgresDialect) ExpandDDL(ddl string) string { return postgresDDLTypes.Replace(ddl) }

// AddColumnSQL uses IF NOT EXISTS, so re-running a migration is a no-op rather
// than an error to swallow.
func (postgresDialect) AddColumnSQL(table, column, decl string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", table, column, decl)
}

// IsDuplicateColumn should never fire given AddColumnSQL's IF NOT EXISTS, but
// stays honest in case a caller renders the ALTER some other way.
func (postgresDialect) IsDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists")
}

func (postgresDialect) TableExistsQuery(table string) (string, []any) {
	return `SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = ?`, []any{table}
}

func (postgresDialect) TableColumnsQuery(table string) (string, []any) {
	return `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ?`, []any{table}
}

// rebindPositional rewrites ? placeholders as $1, $2, ... leaving any ? inside
// a single-quoted string literal alone.
func rebindPositional(query string) string {
	if !strings.ContainsRune(query, '?') {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	inLiteral := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			// A doubled '' inside a literal is an escaped quote; toggling twice
			// leaves the state correct, so it needs no special case.
			inLiteral = !inLiteral
		case c == '?' && !inLiteral:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
