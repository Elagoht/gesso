// Package db is what gesso asks of a database: the three methods repositories
// call, transactions that nest, and constraint violations answered as the client's
// fault. It imports no driver; the application does.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
)

// DB is what a repository takes. *sql.DB, *sql.Conn and *sql.Tx all satisfy it,
// so the same repository runs against the pool, or inside a transaction a
// service began, or inside the transaction a test rolls back.
//
// Its argument lists are database/sql's own, which is why they are ...any.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// beginner is a DB that can begin a transaction: *sql.DB and *sql.Conn.
type beginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// ErrNoTransactions reports an InTx on a DB that is neither a pool, a connection
// nor a transaction.
var ErrNoTransactions = errors.New("db: this DB cannot begin a transaction")

// InTx runs fn in a transaction and commits it when fn returns nil; an error or a
// panic rolls it back. A service uses it to run several repositories as one unit:
//
//	err := db.InTx(ctx, s.db, func(tx db.DB) error {
//	    if err := newEvents(tx).Reserve(ctx, id); err != nil { return err }
//	    return newLedger(tx).Charge(ctx, user, price)
//	})
//
// Given a transaction already (*sql.Tx, as gessotest.Tx hands out), it nests with
// a SAVEPOINT instead of beginning another: rolling back the inner unit leaves the
// outer one going. SAVEPOINT is standard SQL; PostgreSQL, MySQL and SQLite have it.
func InTx(ctx context.Context, d DB, fn func(tx DB) error) error {
	return InTxOptions(ctx, d, nil, fn)
}

// InTxOptions is InTx with an isolation level or read-only flag. Options do not
// apply to a nested (savepoint) unit, which runs under its outer transaction's.
func InTxOptions(ctx context.Context, d DB, opts *sql.TxOptions, fn func(tx DB) error) (err error) {
	switch d := d.(type) {
	case *sql.Tx:
		return inSavepoint(ctx, d, fn)
	case beginner:
		tx, err := d.BeginTx(ctx, opts)
		if err != nil {
			return fmt.Errorf("db: begin: %w", err)
		}
		defer func() {
			if p := recover(); p != nil {
				_ = tx.Rollback()
				panic(p)
			}
		}()
		if err := fn(tx); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return errors.Join(err, fmt.Errorf("db: rollback: %w", rbErr))
			}
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("db: commit: %w", err)
		}
		return nil
	}
	return ErrNoTransactions
}

var savepoints atomic.Uint64

func inSavepoint(ctx context.Context, tx *sql.Tx, fn func(tx DB) error) error {
	name := "gesso_sp_" + strconv.FormatUint(savepoints.Add(1), 10)
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return fmt.Errorf("db: savepoint: %w", err)
	}
	rollback := func() error {
		_, err := tx.ExecContext(context.WithoutCancel(ctx), "ROLLBACK TO SAVEPOINT "+name)
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		if rbErr := rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("db: rollback to savepoint: %w", rbErr))
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name); err != nil {
		return fmt.Errorf("db: release savepoint: %w", err)
	}
	return nil
}
