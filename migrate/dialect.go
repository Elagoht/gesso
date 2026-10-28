package migrate

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// Dialect is what differs between databases for migrating: how a statement
// spells its parameters, and how one migrator keeps the others out.
type Dialect interface {
	Placeholder(n int) string
	Lock(ctx context.Context, conn *sql.Conn) error
	Unlock(ctx context.Context, conn *sql.Conn) error
}

// lockKey is the advisory lock gesso migrations take: an arbitrary constant, the
// same in every process.
const lockKey = 7_236_105_115_115_111

type postgres struct{}

func (postgres) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

func (postgres) Lock(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", int64(lockKey))
	return err
}

func (postgres) Unlock(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", int64(lockKey))
	return err
}

type mysql struct{}

func (mysql) Placeholder(int) string { return "?" }

func (mysql) Lock(ctx context.Context, conn *sql.Conn) error {
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('gesso_migrate', 600)").Scan(&got); err != nil {
		return err
	}
	if got.Int64 != 1 {
		return errors.New("another migrator held the lock for 10 minutes")
	}
	return nil
}

func (mysql) Unlock(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "SELECT RELEASE_LOCK('gesso_migrate')")
	return err
}

type sqlite struct{}

func (sqlite) Placeholder(int) string { return "?" }

// SQLite has one writer at a time already; each migration's transaction is the
// lock.
func (sqlite) Lock(context.Context, *sql.Conn) error   { return nil }
func (sqlite) Unlock(context.Context, *sql.Conn) error { return nil }

// The dialects gesso ships. An application may implement Dialect for another.
var (
	Postgres Dialect = postgres{}
	MySQL    Dialect = mysql{}
	SQLite   Dialect = sqlite{}
)
