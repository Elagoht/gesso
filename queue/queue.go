// Package queue runs work later, outside the request: send the welcome mail,
// resize the upload, call the slow partner API.
//
// It is an outbox. A job is enqueued through the db.DB the caller passes — the
// transaction that writes the user is the one that enqueues the welcome mail — so
// the job exists if and only if the data it is about was committed.
//
// Delivery is at least once. A worker claims a job for a lease; a worker that
// dies, or runs past its lease, loses the job to another, so the same job may
// run twice. Handlers must be idempotent.
//
// SQL is the queue in a database table — PostgreSQL, MySQL or SQLite — and
// Memory is its fake, held to the same suite.
package queue

import (
	"cmp"
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"io/fs"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
)

// NewJob is a job to enqueue.
type NewJob struct {
	Kind    string
	Payload []byte
	// RunAt is the earliest it may run. Zero: now.
	RunAt time.Time
	// MaxAttempts before the job is dead. Zero: 5.
	MaxAttempts int
}

// Job is a claimed job.
type Job struct {
	ID          int64
	Kind        string
	Payload     []byte
	Attempt     int // 1 on the first run
	MaxAttempts int
}

// Last reports whether this attempt is the job's last.
func (j Job) Last() bool { return j.Attempt >= j.MaxAttempts }

// Queue is an outbox of jobs.
type Queue interface {
	// Enqueue adds a job through d — pass the transaction that writes the data
	// the job is about.
	Enqueue(ctx context.Context, d db.DB, j NewJob) (int64, error)
	// Claim takes up to n due jobs of kind for lease: ready ones whose time has
	// come, and running ones whose lease has run out.
	Claim(ctx context.Context, kind string, n int, lease time.Duration) ([]Job, error)
	// Complete removes a job its claimer finished. ErrLeaseLost: the lease ran
	// out and the job is another's now.
	Complete(ctx context.Context, j Job) error
	// Fail records a failed attempt: the job runs again at retryAt, or is dead
	// when it was its last. ErrLeaseLost as for Complete.
	Fail(ctx context.Context, j Job, reason string, retryAt time.Time) error
	// Bury makes a job dead on this attempt, whatever attempts it had left: a
	// payload that will never decode. ErrLeaseLost as for Complete.
	Bury(ctx context.Context, j Job, reason string) error
	// Release hands back a job its claimer never ran: ready again at once, with
	// one attempt more allowed for the one its claim counted. Attempt numbers
	// are never reused, so they keep fencing: a Release may come after the
	// lease ran out, and is ErrLeaseLost once another claimer has taken the
	// job since.
	Release(ctx context.Context, j Job) error
	// Fire enqueues j for the tick of the schedule called name, unless that
	// tick, or a later one, was fired already — by this process or another
	// replica. It reports whether it enqueued. Recording the tick and enqueuing
	// are one unit: both happen, or neither.
	Fire(ctx context.Context, name string, tick time.Time, j NewJob) (bool, error)
	// Dead lists up to n dead jobs of kind, newest first, for a person to look at.
	Dead(ctx context.Context, kind string, n int) ([]DeadJob, error)
}

// DeadJob is a job that failed its last attempt.
type DeadJob struct {
	Job
	LastError string
}

// ErrLeaseLost reports a Complete or Fail by a worker whose lease ran out.
var ErrLeaseLost = errors.New("queue: the lease ran out; the job is another's")

const defaultMaxAttempts = 5

//go:embed migrations
var migrations embed.FS

// Migrations are the gesso_jobs table's migrations for dialect.
func Migrations(dialect migrate.Dialect) (fs.FS, error) {
	dir, err := dialectDir(dialect)
	if err != nil {
		return nil, err
	}
	return fs.Sub(migrations, "migrations/"+dir)
}

func dialectDir(dialect migrate.Dialect) (string, error) {
	switch dialect {
	case migrate.Postgres:
		return "postgres", nil
	case migrate.MySQL:
		return "mysql", nil
	case migrate.SQLite:
		return "sqlite", nil
	}
	return "", errors.New("queue: no migrations for this dialect: PostgreSQL, MySQL or SQLite")
}

// MigrationsTable is where Migrate records the queue migrations.
const MigrationsTable = "gesso_queue_migrations"

// Migrate applies the queue migrations, in a history of their own.
func Migrate(ctx context.Context, d *sql.DB, dialect migrate.Dialect) error {
	files, err := Migrations(dialect)
	if err != nil {
		return err
	}
	m, err := migrate.New(d, dialect, files)
	if err != nil {
		return err
	}
	m.Table = MigrationsTable
	_, err = m.Up(ctx)
	return err
}

// SQL is the queue in the gesso_jobs table.
//
// PostgreSQL claims with one statement (FOR UPDATE SKIP LOCKED). MySQL and
// SQLite claim job by job, each with an UPDATE that only takes a job still due,
// so two claimers never take the same one. There, times are stored as Unix
// nanoseconds; in PostgreSQL as timestamptz.
//
// Times come from its clock, not the database's, so tests can move them; across
// replicas the clocks must agree to well within a lease.
type SQL struct {
	db      db.DB
	clock   clock.Clock
	dialect migrate.Dialect
}

// NewPostgres claims and settles jobs through d (the pool); c tells the time (nil
// for the system clock).
func NewPostgres(d db.DB, c clock.Clock) *SQL { return newSQL(d, c, migrate.Postgres) }

// NewMySQL is the queue in a MySQL table.
func NewMySQL(d db.DB, c clock.Clock) *SQL { return newSQL(d, c, migrate.MySQL) }

// NewSQLite is the queue in a SQLite table.
func NewSQLite(d db.DB, c clock.Clock) *SQL { return newSQL(d, c, migrate.SQLite) }

func newSQL(d db.DB, c clock.Clock, dialect migrate.Dialect) *SQL {
	if c == nil {
		c = clock.System
	}
	return &SQL{db: d, clock: c, dialect: dialect}
}

func (p *SQL) pg() bool { return p.dialect == migrate.Postgres }

// ph is the n-th placeholder.
func (p *SQL) ph(n int) string { return p.dialect.Placeholder(n) }

// at is t as the table stores it.
func (p *SQL) at(t time.Time) driver.Value {
	if p.pg() {
		return t
	}
	return t.UnixNano()
}

// Enqueue inserts the job through d.
func (p *SQL) Enqueue(ctx context.Context, d db.DB, j NewJob) (int64, error) {
	now := p.clock.Now()
	j = defaults(j, now)
	insert := `INSERT INTO gesso_jobs (kind, payload, max_attempts, run_at, created_at, last_error) VALUES (` +
		p.ph(1) + `, ` + p.ph(2) + `, ` + p.ph(3) + `, ` + p.ph(4) + `, ` + p.ph(5) + `, '')`
	if p.pg() {
		var id int64
		err := d.QueryRowContext(ctx, insert+` RETURNING id`, j.Kind, j.Payload, j.MaxAttempts, p.at(j.RunAt), p.at(now)).Scan(&id)
		return id, err
	}
	res, err := d.ExecContext(ctx, insert, j.Kind, j.Payload, j.MaxAttempts, p.at(j.RunAt), p.at(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func defaults(j NewJob, now time.Time) NewJob {
	if j.RunAt.IsZero() {
		j.RunAt = now
	}
	if j.MaxAttempts <= 0 {
		j.MaxAttempts = defaultMaxAttempts
	}
	if j.Payload == nil {
		j.Payload = []byte{}
	}
	return j
}

// Claim takes due jobs, skipping those another claimer is taking right now.
func (p *SQL) Claim(ctx context.Context, kind string, n int, lease time.Duration) ([]Job, error) {
	if !p.pg() {
		return p.claimEach(ctx, kind, n, lease)
	}
	now := p.clock.Now()
	if err := p.buryExpired(ctx, kind, now); err != nil {
		return nil, err
	}
	rows, err := p.db.QueryContext(ctx, `
		UPDATE gesso_jobs SET status = 'running', attempts = attempts + 1, locked_until = $3
		WHERE id IN (
			SELECT id FROM gesso_jobs
			WHERE kind = $1 AND (
				(status = 'ready' AND run_at <= $2) OR
				(status = 'running' AND locked_until <= $2 AND attempts < max_attempts))
			ORDER BY run_at, id
			LIMIT $4
			FOR UPDATE SKIP LOCKED)
		RETURNING id, kind, payload, attempts, max_attempts`,
		kind, now, now.Add(lease), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempt, &j.MaxAttempts); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	slices.SortFunc(jobs, func(a, b Job) int { return cmp.Compare(a.ID, b.ID) })
	return jobs, rows.Err()
}

// claimEach is Claim without UPDATE … RETURNING over a locked subquery, which
// MySQL cannot do: it picks due jobs, then takes each with an UPDATE that only
// matches a job still due. A job another claimer took in between is skipped, and
// when every pick was taken it picks again, so "nothing claimed" means nothing
// was due.
func (p *SQL) claimEach(ctx context.Context, kind string, n int, lease time.Duration) ([]Job, error) {
	now := p.clock.Now()
	if err := p.buryExpired(ctx, kind, now); err != nil {
		return nil, err
	}
	due := `kind = ? AND ((status = 'ready' AND run_at <= ?) OR (status = 'running' AND locked_until <= ? AND attempts < max_attempts))`
	for {
		ids, err := p.dueIDs(ctx, due, kind, now, n)
		if err != nil || len(ids) == 0 {
			return nil, err
		}
		var jobs []Job
		for _, id := range ids {
			res, err := p.db.ExecContext(ctx,
				`UPDATE gesso_jobs SET status = 'running', attempts = attempts + 1, locked_until = ? WHERE id = ? AND `+due,
				p.at(now.Add(lease)), id, kind, p.at(now), p.at(now))
			if err != nil {
				return jobs, err
			}
			if got, err := res.RowsAffected(); err != nil || got == 0 {
				if err != nil {
					return jobs, err
				}
				continue
			}
			var j Job
			err = p.db.QueryRowContext(ctx, `SELECT id, kind, payload, attempts, max_attempts FROM gesso_jobs WHERE id = ?`, id).
				Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempt, &j.MaxAttempts)
			if err != nil {
				return jobs, err
			}
			jobs = append(jobs, j)
		}
		if len(jobs) > 0 {
			return jobs, nil
		}
	}
}

func (p *SQL) dueIDs(ctx context.Context, due, kind string, now time.Time, n int) ([]int64, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM gesso_jobs WHERE `+due+` ORDER BY run_at, id LIMIT ?`,
		kind, p.at(now), p.at(now), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// buryExpired makes dead the jobs of kind whose lease ran out on their last
// attempt. Their worker never settled them — it crashed, or the job crashed
// it — so taking them again would let a job that kills its worker do so for
// ever.
func (p *SQL) buryExpired(ctx context.Context, kind string, now time.Time) error {
	_, err := p.db.ExecContext(ctx, `UPDATE gesso_jobs SET status = 'dead', locked_until = NULL,
		last_error = 'the lease ran out on its last attempt'
		WHERE kind = `+p.ph(1)+` AND status = 'running' AND locked_until <= `+p.ph(2)+` AND attempts >= max_attempts`,
		kind, p.at(now))
	return err
}

// held is the WHERE that fences a settle to the claimer still holding the job:
// placeholders 1 to 3 are the id, the attempt and now.
func (p *SQL) held() string {
	return `id = ` + p.ph(1) + ` AND attempts = ` + p.ph(2) + ` AND status = 'running' AND locked_until > ` + p.ph(3)
}

// Complete deletes the job, if its claimer still holds it.
func (p *SQL) Complete(ctx context.Context, j Job) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM gesso_jobs WHERE `+p.held(), j.ID, j.Attempt, p.at(p.clock.Now()))
	return fenced(res, err)
}

// Fail schedules the job again, or buries it.
func (p *SQL) Fail(ctx context.Context, j Job, reason string, retryAt time.Time) error {
	set := `UPDATE gesso_jobs SET
		status = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'ready' END,
		run_at = ` + p.ph(4) + `, locked_until = NULL, last_error = ` + p.ph(5) + ` WHERE ` + p.held()
	now := p.at(p.clock.Now())
	var res sql.Result
	var err error
	if p.pg() {
		// Numbered: the fence's $1 to $3, then the SET's $4 and $5.
		res, err = p.db.ExecContext(ctx, set, j.ID, j.Attempt, now, p.at(retryAt), reason)
	} else {
		// Positional: the SET's ? come first.
		res, err = p.db.ExecContext(ctx, set, p.at(retryAt), reason, j.ID, j.Attempt, now)
	}
	return fenced(res, err)
}

// Bury makes the job dead now.
func (p *SQL) Bury(ctx context.Context, j Job, reason string) error {
	set := `UPDATE gesso_jobs SET status = 'dead', locked_until = NULL, last_error = ` + p.ph(4) + ` WHERE ` + p.held()
	now := p.at(p.clock.Now())
	var res sql.Result
	var err error
	if p.pg() {
		res, err = p.db.ExecContext(ctx, set, j.ID, j.Attempt, now, reason)
	} else {
		res, err = p.db.ExecContext(ctx, set, reason, j.ID, j.Attempt, now)
	}
	return fenced(res, err)
}

// Fire records the tick and enqueues j, in one transaction, if no replica has
// fired this tick or a later one.
func (p *SQL) Fire(ctx context.Context, name string, tick time.Time, j NewJob) (bool, error) {
	// The schedule's row, "never fired", made on its own: inside the
	// transaction, an insert and an update of one row by replicas at once
	// deadlock in MySQL.
	insert := `INSERT INTO gesso_schedule (name, fired) VALUES (` + p.ph(1) + `, ` + p.ph(2) + `) ON CONFLICT (name) DO NOTHING`
	if p.dialect == migrate.MySQL {
		insert = `INSERT IGNORE INTO gesso_schedule (name, fired) VALUES (?, ?)`
	}
	if _, err := p.db.ExecContext(ctx, insert, name, int64(math.MinInt64)); err != nil {
		return false, err
	}
	fired := false
	err := db.InTx(ctx, p.db, func(tx db.DB) error {
		// Of replicas firing at once, the database lets one change the row;
		// the others find the tick fired when their turn comes.
		res, err := tx.ExecContext(ctx, `UPDATE gesso_schedule SET fired = `+p.ph(1)+`
			WHERE name = `+p.ph(2)+` AND fired < `+p.ph(3), tick.UnixNano(), name, tick.UnixNano())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		fired = true
		_, err = p.Enqueue(ctx, tx, j)
		return err
	})
	return fired && err == nil, err
}

// Release hands the job back, unrun.
func (p *SQL) Release(ctx context.Context, j Job) error {
	res, err := p.db.ExecContext(ctx,
		`UPDATE gesso_jobs SET status = 'ready', max_attempts = max_attempts + 1, locked_until = NULL
		WHERE id = `+p.ph(1)+` AND attempts = `+p.ph(2)+` AND status = 'running'`,
		j.ID, j.Attempt)
	return fenced(res, err)
}

// Dead lists dead jobs.
func (p *SQL) Dead(ctx context.Context, kind string, n int) ([]DeadJob, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, kind, payload, attempts, max_attempts, last_error FROM gesso_jobs
		WHERE kind = `+p.ph(1)+` AND status = 'dead' ORDER BY id DESC LIMIT `+p.ph(2), kind, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeadJob
	for rows.Next() {
		var d DeadJob
		if err := rows.Scan(&d.ID, &d.Kind, &d.Payload, &d.Attempt, &d.MaxAttempts, &d.LastError); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func fenced(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Memory is the queue's fake, with the same claims, leases and fencing. It has
// no transactions: a job enqueued "through" a transaction that rolls back stays
// enqueued. Test the outbox's atomicity against Postgres.
type Memory struct {
	mu    sync.Mutex
	clock clock.Clock
	next  int64
	jobs  map[int64]*memJob
	fired map[string]time.Time // schedules' last ticks
}

type memJob struct {
	Job
	status      string
	runAt       time.Time
	lockedUntil time.Time
	lastError   string
}

// NewMemory is an empty queue; c tells the time (nil for the system clock).
func NewMemory(c clock.Clock) *Memory {
	if c == nil {
		c = clock.System
	}
	return &Memory{clock: c, jobs: map[int64]*memJob{}}
}

// Enqueue adds the job; d is not used.
func (m *Memory) Enqueue(_ context.Context, _ db.DB, j NewJob) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j = defaults(j, m.clock.Now())
	m.next++
	m.jobs[m.next] = &memJob{
		Job:    Job{ID: m.next, Kind: j.Kind, Payload: slices.Clone(j.Payload), MaxAttempts: j.MaxAttempts},
		status: "ready", runAt: j.RunAt,
	}
	return m.next, nil
}

// Claim takes due jobs.
func (m *Memory) Claim(_ context.Context, kind string, n int, lease time.Duration) ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	var due []*memJob
	for _, j := range m.jobs {
		expired := j.Kind == kind && j.status == "running" && !j.lockedUntil.After(now)
		if expired && j.Attempt >= j.MaxAttempts {
			j.status, j.lockedUntil, j.lastError = "dead", time.Time{}, "the lease ran out on its last attempt"
			continue
		}
		if j.Kind == kind && (j.status == "ready" && !j.runAt.After(now) || expired) {
			due = append(due, j)
		}
	}
	slices.SortFunc(due, func(a, b *memJob) int {
		if c := a.runAt.Compare(b.runAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	var out []Job
	for _, j := range due[:min(n, len(due))] {
		j.status, j.Attempt, j.lockedUntil = "running", j.Attempt+1, now.Add(lease)
		c := j.Job
		c.Payload = slices.Clone(j.Payload)
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Job) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (m *Memory) held(j Job) (*memJob, bool) {
	cur, ok := m.jobs[j.ID]
	if !ok || cur.Attempt != j.Attempt || cur.status != "running" || !cur.lockedUntil.After(m.clock.Now()) {
		return nil, false
	}
	return cur, true
}

// Complete removes the job.
func (m *Memory) Complete(_ context.Context, j Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.held(j); !ok {
		return ErrLeaseLost
	}
	delete(m.jobs, j.ID)
	return nil
}

// Fail schedules the job again, or buries it.
func (m *Memory) Fail(_ context.Context, j Job, reason string, retryAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.held(j)
	if !ok {
		return ErrLeaseLost
	}
	cur.status, cur.runAt, cur.lockedUntil, cur.lastError = "ready", retryAt, time.Time{}, reason
	if cur.Attempt >= cur.MaxAttempts {
		cur.status = "dead"
	}
	return nil
}

// Bury makes the job dead now.
func (m *Memory) Bury(_ context.Context, j Job, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.held(j)
	if !ok {
		return ErrLeaseLost
	}
	cur.status, cur.lockedUntil, cur.lastError = "dead", time.Time{}, reason
	return nil
}

// Fire enqueues j unless the tick, or a later one, was fired.
func (m *Memory) Fire(ctx context.Context, name string, tick time.Time, j NewJob) (bool, error) {
	m.mu.Lock()
	if last, ok := m.fired[name]; ok && !last.Before(tick) {
		m.mu.Unlock()
		return false, nil
	}
	if m.fired == nil {
		m.fired = map[string]time.Time{}
	}
	m.fired[name] = tick
	m.mu.Unlock()
	_, err := m.Enqueue(ctx, nil, j)
	return err == nil, err
}

// Release hands the job back, unrun.
func (m *Memory) Release(_ context.Context, j Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.jobs[j.ID]
	if !ok || cur.Attempt != j.Attempt || cur.status != "running" {
		return ErrLeaseLost
	}
	cur.status, cur.MaxAttempts, cur.lockedUntil = "ready", cur.MaxAttempts+1, time.Time{}
	return nil
}

// Dead lists dead jobs.
func (m *Memory) Dead(_ context.Context, kind string, n int) ([]DeadJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []DeadJob
	for _, j := range m.jobs {
		if j.Kind == kind && j.status == "dead" {
			out = append(out, DeadJob{Job: j.Job, LastError: j.lastError})
		}
	}
	slices.SortFunc(out, func(a, b DeadJob) int { return cmp.Compare(b.ID, a.ID) })
	return out[:min(n, len(out))], nil
}
