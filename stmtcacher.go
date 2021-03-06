package sqrl

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

const maxAge = 4 * time.Hour

// Preparer is the interface that wraps the Prepare method.
//
// Prepare executes the given query as implemented by database/sql.Prepare.
// Prepare executes the given query as implemented by database/sql.PrepareContext.
type Preparer interface {
	Prepare(query string) (*sql.Stmt, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// DBProxy groups the Execer, Queryer, QueryRower, and Preparer interfaces.
type DBProxy interface {
	Execer
	Queryer
	QueryRower
	Preparer
	ExecerContext
	QueryerContext
	QueryRowerContext
}

type savedStmt struct {
	stmt       *sql.Stmt
	expiration *time.Timer
}

type stmtCacher struct {
	prep  Preparer
	cache map[string]*savedStmt
	mu    sync.Mutex
}

// NewStmtCacher returns a DBProxy wrapping prep that caches Prepared Stmts.
//
// Stmts are cached based on the string value of their queries.
func NewStmtCacher(prep Preparer) DBProxy {
	return &stmtCacher{prep: prep, cache: make(map[string]*savedStmt)}
}

func (sc *stmtCacher) remove(query string) func() {
	return func() {
		sc.mu.Lock()
		defer sc.mu.Unlock()
		if s, ok := sc.cache[query]; ok {
			s.stmt.Close()
		}
		delete(sc.cache, query)
	}
}

func (sc *stmtCacher) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if s, ok := sc.cache[query]; ok {
		if !s.expiration.Stop() {
			<-s.expiration.C
		}
		s.expiration.Reset(maxAge)
		return s.stmt, nil
	}
	stmt, err := sc.prep.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}

	sc.cache[query] = &savedStmt{
		stmt:       stmt,
		expiration: time.AfterFunc(maxAge, sc.remove(query)),
	}

	return stmt, nil
}

func (sc *stmtCacher) ExecContext(ctx context.Context, query string, args ...interface{}) (res sql.Result, err error) {
	stmt, err := sc.PrepareContext(ctx, query)
	if err != nil {
		return
	}
	return stmt.ExecContext(ctx, args...)
}

func (sc *stmtCacher) QueryContext(ctx context.Context, query string, args ...interface{}) (rows RowsScanner, err error) {
	stmt, err := sc.PrepareContext(ctx, query)
	if err != nil {
		return
	}
	return stmt.QueryContext(ctx, args...)
}

func (sc *stmtCacher) QueryRowContext(ctx context.Context, query string, args ...interface{}) RowScanner {
	stmt, err := sc.PrepareContext(ctx, query)
	if err != nil {
		return &Row{err: err}
	}
	return stmt.QueryRowContext(ctx, args...)
}

func (sc *stmtCacher) Prepare(query string) (*sql.Stmt, error) {
	return sc.PrepareContext(context.Background(), query)
}

func (sc *stmtCacher) Exec(query string, args ...interface{}) (res sql.Result, err error) {
	return sc.ExecContext(context.Background(), query, args...)
}

func (sc *stmtCacher) Query(query string, args ...interface{}) (rows RowsScanner, err error) {
	return sc.QueryContext(context.Background(), query, args...)
}

func (sc *stmtCacher) QueryRow(query string, args ...interface{}) RowScanner {
	return sc.QueryRowContext(context.Background(), query, args...)
}

// DBProxyBeginner describes a DBProxy that can start transactions
type DBProxyBeginner interface {
	DBProxy
	Begin() (*sql.Tx, error)
}

type stmtCacheProxy struct {
	DBProxy
	db *sql.DB
}

// NewStmtCacheProxy creates new cache proxy for statements
func NewStmtCacheProxy(db *sql.DB) DBProxyBeginner {
	return &stmtCacheProxy{DBProxy: NewStmtCacher(db), db: db}
}

func (sp *stmtCacheProxy) Begin() (*sql.Tx, error) {
	return sp.db.Begin()
}
