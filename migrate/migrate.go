// Package migrate applies SQL migrations.
//
// A migration is a pair of files, the down one optional:
//
//	20261028153000_create_events.up.sql
//	20261028153000_create_events.down.sql
//
// The version is a timestamp, so each module can carry its own folder and still
// sort with every other module's into one history. New takes any number of
// fs.FS — a module's embed.FS as it is — and finds the files anywhere in each.
//
// Every migration runs in its own transaction, under a lock that keeps two
// replicas from migrating at once. A migration whose first line is
//
//	-- gesso:no-transaction
//
// runs outside one, for statements that refuse it (CREATE INDEX CONCURRENTLY).
// MySQL commits DDL as it goes whatever is asked, so a failed MySQL migration may
// be half applied; and its DSN needs multiStatements=true for a file with several
// statements.
//
// What was applied is recorded with a checksum. Up refuses to go on when an
// applied migration's file has changed since, or when an older migration was
// never applied (a merge brought it in after a newer one ran) — unless
// AllowOutOfOrder says to apply it anyway.
package migrate

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Migration is one version, read from its files.
type Migration struct {
	Version  int64
	Name     string
	Up       string
	Down     string // empty when there is no down file
	Checksum string // of Up
	// NoTransaction is set by a first line of "-- gesso:no-transaction".
	NoTransaction bool
}

// Migrator applies one set of migrations to one database.
type Migrator struct {
	db         *sql.DB
	dialect    Dialect
	migrations []Migration // by version
	// Table records what was applied. Default "gesso_migrations".
	Table string
	// AllowOutOfOrder applies a migration older than the newest applied one,
	// instead of refusing.
	AllowOutOfOrder bool
}

// New reads the migrations in sources. Two files for one version and direction,
// a down without an up, and a malformed name are errors.
func New(db *sql.DB, dialect Dialect, sources ...fs.FS) (*Migrator, error) {
	byVersion := map[int64]*Migration{}
	for _, src := range sources {
		err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".sql") {
				return nil
			}
			return addFile(src, path, d.Name(), byVersion)
		})
		if err != nil {
			return nil, err
		}
	}
	m := &Migrator{db: db, dialect: dialect, Table: "gesso_migrations"}
	for _, mg := range byVersion {
		if mg.Up == "" {
			return nil, fmt.Errorf("migrate: version %d has a down file but no up file", mg.Version)
		}
		m.migrations = append(m.migrations, *mg)
	}
	slices.SortFunc(m.migrations, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })
	return m, nil
}

func addFile(src fs.FS, path, name string, byVersion map[int64]*Migration) error {
	base, dir, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), ".")
	if !ok || (dir != "up" && dir != "down") {
		return fmt.Errorf("migrate: %s: a migration file ends in .up.sql or .down.sql", path)
	}
	digits, label, _ := strings.Cut(base, "_")
	version, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || version <= 0 {
		return fmt.Errorf("migrate: %s: a migration file begins with its version: 20261028153000_name.up.sql", path)
	}
	body, err := fs.ReadFile(src, path)
	if err != nil {
		return err
	}
	mg := byVersion[version]
	if mg == nil {
		mg = &Migration{Version: version, Name: label}
		byVersion[version] = mg
	}
	if mg.Name != label {
		return fmt.Errorf("migrate: version %d is both %q and %q", version, mg.Name, label)
	}
	text := string(body)
	if dir == "up" {
		if mg.Up != "" {
			return fmt.Errorf("migrate: version %d has two up files", version)
		}
		sum := sha256.Sum256(body)
		mg.Up, mg.Checksum = text, hex.EncodeToString(sum[:])
		first, _, _ := strings.Cut(text, "\n")
		mg.NoTransaction = strings.TrimSpace(first) == "-- gesso:no-transaction"
	} else {
		if mg.Down != "" {
			return fmt.Errorf("migrate: version %d has two down files", version)
		}
		mg.Down = text
	}
	return nil
}

// Migrations are every migration New read, by version.
func (m *Migrator) Migrations() []Migration { return slices.Clone(m.migrations) }

// State is a migration's standing in the database.
type State int

const (
	Pending State = iota
	Applied
	// Changed is applied, but its file is not what was applied.
	Changed
	// Unknown is applied, but no file has its version.
	Unknown
)

func (s State) String() string {
	return [...]string{"pending", "applied", "changed", "unknown"}[s]
}

// Status is one version's state.
type Status struct {
	Version   int64
	Name      string
	State     State
	AppliedAt time.Time // zero when pending
}

// ErrChanged reports an applied migration whose file has changed since.
var ErrChanged = errors.New("migrate: an applied migration has changed")

// ErrOutOfOrder reports a pending migration older than the newest applied one.
var ErrOutOfOrder = errors.New("migrate: a pending migration is older than an applied one")

type record struct {
	name      string
	checksum  string
	appliedAt time.Time
}

// Status reports every version, from the files and the database.
func (m *Migrator) Status(ctx context.Context) ([]Status, error) {
	var out []Status
	err := m.locked(ctx, func(conn *sql.Conn) error {
		applied, err := m.applied(ctx, conn)
		if err != nil {
			return err
		}
		out = m.status(applied)
		return nil
	})
	return out, err
}

func (m *Migrator) status(applied map[int64]record) []Status {
	var out []Status
	seen := map[int64]bool{}
	for _, mg := range m.migrations {
		s := Status{Version: mg.Version, Name: mg.Name}
		if rec, ok := applied[mg.Version]; ok {
			s.State, s.AppliedAt = Applied, rec.appliedAt
			if rec.checksum != mg.Checksum {
				s.State = Changed
			}
		}
		seen[mg.Version] = true
		out = append(out, s)
	}
	for v, rec := range applied {
		if !seen[v] {
			out = append(out, Status{Version: v, Name: rec.name, State: Unknown, AppliedAt: rec.appliedAt})
		}
	}
	slices.SortFunc(out, func(a, b Status) int { return cmp.Compare(a.Version, b.Version) })
	return out
}

// Up applies every pending migration in order and returns those it applied. It
// stops at the first failure; what was applied before it stays applied.
func (m *Migrator) Up(ctx context.Context) ([]Migration, error) {
	var done []Migration
	err := m.locked(ctx, func(conn *sql.Conn) error {
		applied, err := m.applied(ctx, conn)
		if err != nil {
			return err
		}
		var newest int64
		for v := range applied {
			newest = max(newest, v)
		}
		var pending []Migration
		for _, mg := range m.migrations {
			rec, ok := applied[mg.Version]
			switch {
			case ok && rec.checksum != mg.Checksum:
				return fmt.Errorf("%w: %d_%s", ErrChanged, mg.Version, mg.Name)
			case !ok && mg.Version < newest && !m.AllowOutOfOrder:
				return fmt.Errorf("%w: %d_%s is older than applied %d", ErrOutOfOrder, mg.Version, mg.Name, newest)
			case !ok:
				pending = append(pending, mg)
			}
		}
		for _, mg := range pending {
			if err := m.apply(ctx, conn, mg, true); err != nil {
				return err
			}
			done = append(done, mg)
		}
		return nil
	})
	return done, err
}

// Down reverts the newest steps applied migrations, newest first, and returns
// those it reverted. A migration without a down file stops it.
func (m *Migrator) Down(ctx context.Context, steps int) ([]Migration, error) {
	var done []Migration
	err := m.locked(ctx, func(conn *sql.Conn) error {
		applied, err := m.applied(ctx, conn)
		if err != nil {
			return err
		}
		versions := make([]int64, 0, len(applied))
		for v := range applied {
			versions = append(versions, v)
		}
		slices.Sort(versions)
		slices.Reverse(versions)
		byVersion := map[int64]Migration{}
		for _, mg := range m.migrations {
			byVersion[mg.Version] = mg
		}
		for _, v := range versions[:min(steps, len(versions))] {
			mg, ok := byVersion[v]
			if !ok {
				return fmt.Errorf("migrate: applied version %d has no file to revert it with", v)
			}
			if mg.Down == "" {
				return fmt.Errorf("migrate: %d_%s has no down file", mg.Version, mg.Name)
			}
			if err := m.apply(ctx, conn, mg, false); err != nil {
				return err
			}
			done = append(done, mg)
		}
		return nil
	})
	return done, err
}

func (m *Migrator) apply(ctx context.Context, conn *sql.Conn, mg Migration, up bool) error {
	body, verb := mg.Up, "apply"
	record := func(ctx context.Context, x execer) error {
		_, err := x.ExecContext(ctx,
			"INSERT INTO "+m.Table+" (version, name, checksum, applied_at) VALUES ("+m.placeholders(4)+")",
			mg.Version, mg.Name, mg.Checksum, time.Now().UTC().Format(time.RFC3339))
		return err
	}
	if !up {
		body, verb = mg.Down, "revert"
		record = func(ctx context.Context, x execer) error {
			_, err := x.ExecContext(ctx, "DELETE FROM "+m.Table+" WHERE version = "+m.dialect.Placeholder(1), mg.Version)
			return err
		}
	}
	wrap := func(err error) error { return fmt.Errorf("migrate: %s %d_%s: %w", verb, mg.Version, mg.Name, err) }
	if mg.NoTransaction {
		if _, err := conn.ExecContext(ctx, body); err != nil {
			return wrap(err)
		}
		if err := record(ctx, conn); err != nil {
			return wrap(err)
		}
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return wrap(err)
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		_ = tx.Rollback()
		return wrap(err)
	}
	if err := record(ctx, tx); err != nil {
		_ = tx.Rollback()
		return wrap(err)
	}
	if err := tx.Commit(); err != nil {
		return wrap(err)
	}
	return nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (m *Migrator) placeholders(n int) string {
	ps := make([]string, n)
	for i := range ps {
		ps[i] = m.dialect.Placeholder(i + 1)
	}
	return strings.Join(ps, ", ")
}

// locked runs fn on one connection holding the migration lock, with the table in
// place.
func (m *Migrator) locked(ctx context.Context, fn func(*sql.Conn) error) (err error) {
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: connect: %w", err)
	}
	defer conn.Close()
	if err := m.dialect.Lock(ctx, conn); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer func() {
		if uerr := m.dialect.Unlock(context.WithoutCancel(ctx), conn); uerr != nil {
			err = errors.Join(err, fmt.Errorf("migrate: unlock: %w", uerr))
		}
	}()
	if _, err := conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+m.Table+
		" (version BIGINT PRIMARY KEY, name VARCHAR(255) NOT NULL, checksum CHAR(64) NOT NULL, applied_at VARCHAR(32) NOT NULL)"); err != nil {
		return fmt.Errorf("migrate: create %s: %w", m.Table, err)
	}
	return fn(conn)
}

func (m *Migrator) applied(ctx context.Context, conn *sql.Conn) (map[int64]record, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version, name, checksum, applied_at FROM "+m.Table)
	if err != nil {
		return nil, fmt.Errorf("migrate: read %s: %w", m.Table, err)
	}
	defer rows.Close()
	out := map[int64]record{}
	for rows.Next() {
		var v int64
		var rec record
		var at string
		if err := rows.Scan(&v, &rec.name, &rec.checksum, &at); err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", m.Table, err)
		}
		rec.appliedAt, _ = time.Parse(time.RFC3339, at)
		rec.checksum = strings.TrimSpace(rec.checksum)
		out[v] = rec
	}
	return out, rows.Err()
}
