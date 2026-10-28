package lock_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Elagoht/gesso/lock"
)

// A database/sql driver that grants every lock and refuses every unlock, and
// counts the connections it opened and closed: a connection database/sql
// closes, instead of keeping it in the pool, was discarded.
type refusing struct {
	mu             sync.Mutex
	opened, closed int
	unlockErr      error // returned by the unlock; nil answers "not released"
}

func (d *refusing) Open(string) (driver.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opened++
	return &refusingConn{d: d}, nil
}

type refusingConn struct{ d *refusing }

func (c *refusingConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *refusingConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (c *refusingConn) Close() error {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.closed++
	return nil
}

func (c *refusingConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (c *refusingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	unlock := strings.Contains(query, "unlock") || strings.Contains(query, "RELEASE_LOCK")
	if unlock && c.d.unlockErr != nil {
		return nil, c.d.unlockErr
	}
	// pg_advisory_unlock answers false, RELEASE_LOCK 0: not released. Every
	// lock is granted: true, or 1.
	var v driver.Value = !unlock
	if strings.Contains(query, "LOCK(") {
		v = int64(1)
		if unlock {
			v = int64(0)
		}
	}
	return &oneRow{v: v}, nil
}

type oneRow struct {
	v    driver.Value
	done bool
}

func (r *oneRow) Columns() []string { return []string{"v"} }
func (r *oneRow) Close() error      { return nil }
func (r *oneRow) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done, dest[0] = true, r.v
	return nil
}

func refusingDB(t *testing.T, d *refusing) *sql.DB {
	t.Helper()
	db := sql.OpenDB(connector{d})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type connector struct{ d *refusing }

func (c connector) Connect(context.Context) (driver.Conn, error) { return c.d.Open("") }
func (c connector) Driver() driver.Driver                        { return c.d }

// A connection whose unlock fails, or answers that it held no lock, may still
// hold the lock. Back in the pool it would hold it for ever, so it is ended.
func TestUnlockFailureDiscardsTheConnection(t *testing.T) {
	ctx := context.Background()
	for name, mk := range map[string]func(*sql.DB) lock.Locker{
		"postgres": func(d *sql.DB) lock.Locker { return lock.NewPostgres(d) },
		"mysql":    func(d *sql.DB) lock.Locker { return lock.NewMySQL(d) },
	} {
		for _, unlockErr := range []error{nil, errors.New("connection reset")} {
			t.Run(name, func(t *testing.T) {
				d := &refusing{unlockErr: unlockErr}
				l := mk(refusingDB(t, d))
				unlock, err := l.Lock(ctx, "job")
				if err != nil {
					t.Fatal(err)
				}
				if err := unlock(ctx); err == nil {
					t.Fatal("a failed unlock reports it")
				}
				d.mu.Lock()
				opened, closed := d.opened, d.closed
				d.mu.Unlock()
				if opened != 1 || closed != 1 {
					t.Fatalf("opened %d, closed %d: the connection went back to the pool", opened, closed)
				}
			})
		}
	}
}
