// Package lock makes sure a piece of work runs in one place at a time, across
// every replica: a scheduled job, a migration of data, a report built once. Memory
// is here for one process and for tests; Postgres and MySQL hold the lock in the
// database.
// Every implementation passes locktest.Run.
package lock

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
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

// Memory locks within one process. A name is kept only while someone holds or
// waits for it, so names made from data do not pile up.
type Memory struct {
	mu    sync.Mutex
	locks map[string]*memLock
}

type memLock struct {
	ch   chan struct{}
	refs int // the holder and the waiters
}

// NewMemory is a locker with nothing held.
func NewMemory() *Memory { return &Memory{locks: map[string]*memLock{}} }

// enter counts one more user of name's lock, and returns it.
func (m *Memory) enter(name string) *memLock {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[name]
	if !ok {
		l = &memLock{ch: make(chan struct{}, 1)}
		m.locks[name] = l
	}
	l.refs++
	return l
}

// leave counts one user less, forgetting the name with the last.
func (m *Memory) leave(name string, l *memLock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.refs--; l.refs == 0 {
		delete(m.locks, name)
	}
}

func (m *Memory) unlock(name string, l *memLock) Unlock {
	return once(func(context.Context) error {
		<-l.ch
		m.leave(name, l)
		return nil
	})
}

// Lock waits for name.
func (m *Memory) Lock(ctx context.Context, name string) (Unlock, error) {
	l := m.enter(name)
	select {
	case l.ch <- struct{}{}:
		return m.unlock(name, l), nil
	case <-ctx.Done():
		m.leave(name, l)
		return nil, ctx.Err()
	}
}

// TryLock takes name if it is free.
func (m *Memory) TryLock(_ context.Context, name string) (Unlock, bool, error) {
	l := m.enter(name)
	select {
	case l.ch <- struct{}{}:
		return m.unlock(name, l), true, nil
	default:
		m.leave(name, l)
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
// one behind. Names are hashed to the 64-bit keys PostgreSQL locks on: two
// names may share a key, rarely by chance and on purpose if an attacker picks
// them, so name locks after your own data, not a client's.
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

// MySQL holds locks as MySQL user-level locks (GET_LOCK), on the same terms as
// Postgres: a connection of its own per held lock, released when that
// connection ends. A name is hashed into the 64 characters MySQL allows.
type MySQL struct {
	db *sql.DB
}

// NewMySQL locks in d.
func NewMySQL(d *sql.DB) *MySQL { return &MySQL{db: d} }

// Lock waits for name; ctx ending cancels the wait.
func (m *MySQL) Lock(ctx context.Context, name string) (Unlock, error) {
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, -1)", mysqlName(name)).Scan(&got); err != nil {
		discard(conn)
		return nil, err
	}
	if got.Int64 != 1 {
		discard(conn)
		return nil, errors.New("lock: GET_LOCK failed")
	}
	return m.unlock(conn, name), nil
}

// TryLock takes name if it is free.
func (m *MySQL) TryLock(ctx context.Context, name string) (Unlock, bool, error) {
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", mysqlName(name)).Scan(&got); err != nil {
		discard(conn)
		return nil, false, err
	}
	if got.Int64 != 1 {
		_ = conn.Close()
		return nil, false, nil
	}
	return m.unlock(conn, name), true, nil
}

func (m *MySQL) unlock(conn *sql.Conn, name string) Unlock {
	return once(func(ctx context.Context) error {
		var released sql.NullInt64
		err := conn.QueryRowContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", mysqlName(name)).Scan(&released)
		if err != nil || released.Int64 != 1 {
			discard(conn)
			return errors.Join(err, errNotReleased(released.Int64 == 1))
		}
		return conn.Close()
	})
}

// mysqlName is name as a MySQL lock name, which may be 64 characters at most.
func mysqlName(name string) string { return fmt.Sprintf("gesso:%016x", uint64(key(name))) }

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
