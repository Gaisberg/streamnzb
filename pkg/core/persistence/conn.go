package persistence

import (
	"context"
	"database/sql"
	"sync/atomic"
)

// conn pairs an open pool with the dialect that renders SQL for it.
type conn struct {
	db      *sql.DB
	dialect Dialect
}

// connRef is the handle every store actually holds. The delegating methods
// rebind ? placeholders for the active dialect, which is what lets every store
// write one portable query regardless of backend.
//
// The pool sits behind an atomic pointer so it can be repointed at a different
// database underneath in-flight callers: no store caches a *sql.DB, and the
// stores share the connRefs the manager owns, so one swap repoints all of them
// at once. That is what makes StateManager.Reopen possible without rewiring
// every holder of the manager.
type connRef struct {
	ptr atomic.Pointer[conn]
}

func newConnRef(db *sql.DB, d Dialect) *connRef {
	r := &connRef{}
	r.ptr.Store(&conn{db: db, dialect: d})
	return r
}

// swap repoints the reference and returns the pool that was in use. The caller
// must close that pool only once no query can still be holding it: sql.DB.Close
// waits on in-use connections rather than aborting them, so closing it inline
// would block on an open *sql.Rows.
func (r *connRef) swap(db *sql.DB, d Dialect) *sql.DB {
	old := r.ptr.Swap(&conn{db: db, dialect: d})
	if old == nil {
		return nil
	}
	return old.db
}

func (r *connRef) dialect() Dialect { return r.ptr.Load().dialect }

// pool exposes the raw handle, for the few callers that need to compare or
// close it rather than run a query through it.
func (r *connRef) pool() *sql.DB { return r.ptr.Load().db }

func (r *connRef) Exec(query string, args ...any) (sql.Result, error) {
	c := r.ptr.Load()
	return c.db.Exec(c.dialect.Rebind(query), args...)
}

func (r *connRef) Query(query string, args ...any) (*sql.Rows, error) {
	c := r.ptr.Load()
	return c.db.Query(c.dialect.Rebind(query), args...)
}

func (r *connRef) QueryRow(query string, args ...any) *sql.Row {
	c := r.ptr.Load()
	return c.db.QueryRow(c.dialect.Rebind(query), args...)
}

func (r *connRef) Begin() (*txn, error) {
	return r.BeginTx(context.Background())
}

func (r *connRef) BeginTx(ctx context.Context) (*txn, error) {
	c := r.ptr.Load()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &txn{tx: tx, dialect: c.dialect}, nil
}

func (r *connRef) Close() error { return r.ptr.Load().db.Close() }

// txn is the transaction-scoped counterpart of connRef. It pins the dialect it
// began with, so a swap mid-transaction cannot render half its statements for
// the wrong backend.
type txn struct {
	tx      *sql.Tx
	dialect Dialect
}

func (t *txn) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.Exec(t.dialect.Rebind(query), args...)
}

func (t *txn) Query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.Query(t.dialect.Rebind(query), args...)
}

func (t *txn) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRow(t.dialect.Rebind(query), args...)
}

func (t *txn) Prepare(query string) (*sql.Stmt, error) {
	return t.tx.Prepare(t.dialect.Rebind(query))
}

func (t *txn) Commit() error { return t.tx.Commit() }

func (t *txn) Rollback() error { return t.tx.Rollback() }
