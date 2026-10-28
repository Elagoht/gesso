// Package lock makes sure a piece of work runs in one place at a time, across
// every replica: a scheduled job, a migration of data, a report built once. Memory
// is here for one process and for tests; Postgres holds the lock in the database.
// Every implementation passes locktest.Run.
package lock

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"hash/fnv"
	"sync"
)

// Unlock releases a lock. Calling it again does nothing.
type Unlock func(ctx context.Context) error

// Locker hands out named locks.
type Locker interface {
	// Lock waits until it holds name, or ctx is done.
	Lock(ctx context.Context, name string) (Unlock, error)
	// TryLock takes name if it is free, and reports false if another holds it.
	TryLock(ctx context.Context, name string) (Unlock, bool, error)
}

// Memory locks within one process.
type Memory struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// NewMemory is a locker with nothing held.
func NewMemory() *Memory { return &Memory{locks: map[string]chan struct{}{}} }

func (m *Memory) slot(name string) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.locks[name]
	if !ok {
		ch = make(chan struct{}, 1)
		m.locks[name] = ch
	}
	return ch
}

// Lock waits for name.
func (m *Memory) Lock(ctx context.Context, name string) (Unlock, error) {
	ch := m.slot(name)
	select {
	case ch <- struct{}{}:
		return once(func(context.Context) error { <-ch; return nil }), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TryLock takes name if it is free.
func (m *Memory) TryLock(_ context.Context, name string) (Unlock, bool, error) {
	ch := m.slot(name)
	select {
	case ch <- struct{}{}:
		return once(func(context.Context) error { <-ch; return nil }), true, nil
	default:
		return nil, false, nil
	}
}

func once(fn Unlock) Unlock {
	var done sync.Once
	return func(ctx context.Context) error {
		var err error
		done.Do(func() { err = fn(ctx) })
		return err
	}
}

// Postgres holds locks as PostgreSQL session advisory locks: each on a
// connection of its own, taken from the pool for as long as the lock is held.
// A lock is released when the connection ends, so a crashed holder never leaves
// one behind. Names are hashed to the 64-bit keys PostgreSQL locks on.
//
// Hold few at once: every held lock is a pooled connection out of the pool.
type Postgres struct {
	db *sql.DB
}

// NewPostgres locks in d.
func NewPostgres(d *sql.DB) *Postgres { return &Postgres{db: d} }

// Lock waits for name; ctx ending cancels the wait.
func (p *Postgres) Lock(ctx context.Context, name string) (Unlock, error) {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", key(name)); err != nil {
		discard(conn)
		return nil, err
	}
	return p.unlock(conn, name), nil
}

// TryLock takes name if it is free.
func (p *Postgres) TryLock(ctx context.Context, name string) (Unlock, bool, error) {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key(name)).Scan(&got); err != nil {
		discard(conn)
		return nil, false, err
	}
	if !got {
		_ = conn.Close()
		return nil, false, nil
	}
	return p.unlock(conn, name), true, nil
}

func (p *Postgres) unlock(conn *sql.Conn, name string) Unlock {
	return once(func(ctx context.Context) error {
		var released bool
		err := conn.QueryRowContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", key(name)).Scan(&released)
		if err != nil || !released {
			// A connection still holding the lock must not go back to the pool,
			// where it would hold it for ever: end the session instead.
			discard(conn)
			return errors.Join(err, errNotReleased(released))
		}
		return conn.Close()
	})
}

func errNotReleased(released bool) error {
	if released {
		return nil
	}
	return errors.New("lock: the lock was not held by its connection")
}

// discard ends conn's session instead of returning it to the pool.
func discard(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func key(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64())
}
