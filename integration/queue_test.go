package integration

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/gessotest"
	"github.com/Elagoht/gesso/migrate"
	"github.com/Elagoht/gesso/queue"
	"github.com/Elagoht/gesso/queue/queuetest"
)

// queueDB is one dialect's database for the queue suites, its queue
// constructor, and how it makes and fills a users table.
type queueDB struct {
	open     func(t *testing.T) *sql.DB
	dialect  migrate.Dialect
	newQueue func(db.DB, clock.Clock) *queue.SQL
	users    string // CREATE TABLE users
	addUser  string // INSERT a user, one parameter
}

var queueDBs = map[string]queueDB{
	"Postgres": {pgDB, migrate.Postgres, queue.NewPostgres,
		"CREATE TABLE users (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name TEXT NOT NULL)",
		"INSERT INTO users (name) VALUES ($1)"},
	"MySQL": {myDB, migrate.MySQL, queue.NewMySQL,
		"CREATE TABLE users (id BIGINT AUTO_INCREMENT PRIMARY KEY, name TEXT NOT NULL)",
		"INSERT INTO users (name) VALUES (?)"},
	// Claimers race over one file: they wait for its lock instead of failing.
	"SQLite": {func(t *testing.T) *sql.DB {
		d, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "q.db")+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	}, migrate.SQLite, queue.NewSQLite,
		"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
		"INSERT INTO users (name) VALUES (?)"},
}

func migratedQueue(t *testing.T, q queueDB) *sql.DB {
	t.Helper()
	d := q.open(t)
	if err := queue.Migrate(context.Background(), d, q.dialect); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestQueue(t *testing.T) {
	for name, q := range queueDBs {
		t.Run(name, func(t *testing.T) {
			d := migratedQueue(t, q)
			queuetest.Run(t, func(t *testing.T, c clock.Clock) (queue.Queue, db.DB) {
				tx := gessotest.Tx(t, d)
				return q.newQueue(tx, c), tx
			})
		})
	}
}

// The outbox's point: a job exists if and only if the transaction that enqueued
// it committed.
func TestQueueOutboxIsAtomic(t *testing.T) {
	for name, q := range queueDBs {
		t.Run(name, func(t *testing.T) {
			d := migratedQueue(t, q)
			ctx := context.Background()
			if _, err := d.Exec(q.users); err != nil {
				t.Fatal(err)
			}
			qu := q.newQueue(d, nil)
			signup := func(name string, fail bool) error {
				return db.InTx(ctx, d, func(tx db.DB) error {
					if _, err := tx.ExecContext(ctx, q.addUser, name); err != nil {
						return err
					}
					if _, err := qu.Enqueue(ctx, tx, queue.NewJob{Kind: "welcome"}); err != nil {
						return err
					}
					if fail {
						return errors.New("a later step failed")
					}
					return nil
				})
			}
			if err := signup("ada", true); err == nil {
				t.Fatal("want the error")
			}
			if err := signup("bob", false); err != nil {
				t.Fatal(err)
			}
			var users, jobs int
			_ = d.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
			_ = d.QueryRow("SELECT COUNT(*) FROM gesso_jobs").Scan(&jobs)
			if users != 1 || jobs != 1 {
				t.Fatalf("users %d, jobs %d: the rolled-back signup left its job, or the committed one lost it", users, jobs)
			}
		})
	}
}

// Replicas firing one tick at once enqueue it once.
func TestQueueFireOnceAcrossReplicas(t *testing.T) {
	for name, q := range queueDBs {
		t.Run(name, func(t *testing.T) {
			d := migratedQueue(t, q)
			ctx := context.Background()
			tick := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
			var mu sync.Mutex
			won := 0
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					ok, err := q.newQueue(d, nil).Fire(ctx, "nightly", tick, queue.NewJob{Kind: "report", RunAt: tick})
					if err != nil {
						t.Error(err)
					}
					if ok {
						mu.Lock()
						won++
						mu.Unlock()
					}
				})
			}
			wg.Wait()
			var jobs int
			_ = d.QueryRow("SELECT COUNT(*) FROM gesso_jobs").Scan(&jobs)
			if won != 1 || jobs != 1 {
				t.Fatalf("%d replicas fired, %d jobs", won, jobs)
			}
		})
	}
}

// Claimers racing over the same jobs take each exactly once, and between them
// take them all.
func TestQueueClaimsEachJobOnce(t *testing.T) {
	for name, q := range queueDBs {
		t.Run(name, func(t *testing.T) {
			d := migratedQueue(t, q)
			ctx := context.Background()
			qu := q.newQueue(d, nil)
			const n = 200
			for range n {
				if _, err := qu.Enqueue(ctx, d, queue.NewJob{Kind: "k"}); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			seen := map[int64]int{}
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					for {
						jobs, err := qu.Claim(ctx, "k", 7, time.Minute)
						if err != nil {
							t.Error(err)
							return
						}
						if len(jobs) == 0 {
							return
						}
						mu.Lock()
						for _, j := range jobs {
							seen[j.ID]++
						}
						mu.Unlock()
					}
				})
			}
			wg.Wait()
			if len(seen) != n {
				t.Fatalf("claimed %d distinct jobs of %d", len(seen), n)
			}
			for id, c := range seen {
				if c != 1 {
					t.Fatalf("job %d claimed %d times", id, c)
				}
			}
		})
	}
}
