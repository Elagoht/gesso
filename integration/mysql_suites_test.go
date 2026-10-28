package integration

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/date"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/gessotest"
	"github.com/Elagoht/gesso/lock"
	"github.com/Elagoht/gesso/lock/locktest"
	"github.com/Elagoht/gesso/migrate"
	"github.com/Elagoht/gesso/token"
	"github.com/Elagoht/gesso/token/tokentest"
	"github.com/go-sql-driver/mysql"
)

func TestMySQLMigrations(t *testing.T) { migrationSuite(t, myDB(t), migrate.MySQL) }

func TestMySQLInTx(t *testing.T) { inTxSuite(t, myDB(t)) }

// Two migrators at once — two replicas starting — apply each migration once.
func TestMySQLMigrationLock(t *testing.T) {
	d := myDB(t)
	files := fstest.MapFS{
		"20261028100000_a.up.sql": {Data: []byte("CREATE TABLE a (id INTEGER); SELECT SLEEP(0.3);")},
		"20261028110000_b.up.sql": {Data: []byte("CREATE TABLE b (id INTEGER);")},
	}
	var wg sync.WaitGroup
	results := make([]int, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			m, err := migrate.New(d, migrate.MySQL, files)
			if err != nil {
				errs[i] = err
				return
			}
			done, err := m.Up(context.Background())
			results[i], errs[i] = len(done), err
		})
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0]+results[1] != 2 {
		t.Fatalf("applied %v, errors %v: the lock let both in or kept one out", results, errs)
	}
}

func TestMySQLTokenStore(t *testing.T) {
	d := myDB(t)
	if err := token.Migrate(context.Background(), d, migrate.MySQL); err != nil {
		t.Fatal(err)
	}
	tokentest.Run(t, func(t *testing.T) token.Store {
		return token.NewSQLStore(gessotest.Tx(t, d), migrate.MySQL)
	})
}

// myClassify is the one line an application writes for go-sql-driver/mysql.
func myClassify(err error) (db.Violation, bool) {
	var me *mysql.MySQLError
	if !errors.As(err, &me) {
		return db.Violation{}, false
	}
	return db.MySQLViolation(me.Number, me.Message)
}

// myTagsApp is a module on MySQL: placeholders are ?, ids come from
// LastInsertId.
func myTagsApp(t *testing.T, d db.DB) *gessotest.Client {
	app := gessotest.New(t, gesso.Config{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ErrorMappers: []gesso.ErrorMapper{db.Errors(myClassify, map[string]string{"tags.name": "name"})},
	}, moduleFunc(func(r *gesso.Router) {
		r.Post("/tags", gesso.Handle(func(ctx context.Context, in tagIn) (tagOut, error) {
			res, err := d.ExecContext(ctx, "INSERT INTO tags (name) VALUES (?)", in.Name)
			if err != nil {
				return tagOut{}, err
			}
			id, err := res.LastInsertId()
			return tagOut{ID: id}, err
		}, gesso.Status(201)))
		r.Get("/tags/count", gesso.Handle(func(ctx context.Context, _ gesso.Empty) (tagOut, error) {
			var out tagOut
			err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM tags").Scan(&out.ID)
			return out, err
		}))
	}))
	return gessotest.NewClient(t, app)
}

func myCreateTags(t *testing.T, d *sql.DB) {
	t.Helper()
	if _, err := d.Exec("CREATE TABLE tags (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255) NOT NULL UNIQUE)"); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLViolationAnswersTheField(t *testing.T) {
	d := myDB(t)
	myCreateTags(t, d)
	c := myTagsApp(t, d)
	c.Post("/tags", gessotest.JSON(tagIn{Name: "go"})).ExpectStatus(201)
	p := c.Post("/tags", gessotest.JSON(tagIn{Name: "go"})).ExpectProblem(409, "conflict")
	if len(p.Errors) != 1 || p.Errors[0].Field != "name" || p.Errors[0].Code != "unique" {
		t.Fatalf("got %+v", p)
	}
}

func TestMySQLTestTransaction(t *testing.T) {
	d := myDB(t)
	myCreateTags(t, d)
	func() {
		c := myTagsApp(t, gessotest.Tx(t, d))
		c.Post("/tags", gessotest.JSON(tagIn{Name: "go"})).ExpectStatus(201)
		c.Post("/tags", gessotest.JSON(tagIn{Name: "go"})).ExpectProblem(409, "conflict")
		c.Post("/tags", gessotest.JSON(tagIn{Name: "rust"})).ExpectStatus(201)
		if n := gessotest.Decode[tagOut](c.Get("/tags/count").ExpectStatus(200)).ID; n != 2 {
			t.Fatalf("count %d", n)
		}
	}()
}

func TestMySQLLocker(t *testing.T) {
	d := myDB(t)
	locktest.Run(t, func(*testing.T) lock.Locker { return lock.NewMySQL(d) })
}

// When the holder's session dies, its lock is freed for the next replica and
// unlock says it failed.
func TestMySQLLockerDiscardsADeadSession(t *testing.T) {
	d := myDB(t)
	ctx := context.Background()
	unlock, err := lock.NewMySQL(d).Lock(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	var holder int64
	err = d.QueryRow(`SELECT t.PROCESSLIST_ID FROM performance_schema.metadata_locks m
		JOIN performance_schema.threads t ON t.THREAD_ID = m.OWNER_THREAD_ID
		WHERE m.OBJECT_TYPE = 'USER LEVEL LOCK' AND m.LOCK_STATUS = 'GRANTED'`).Scan(&holder)
	if err != nil {
		t.Fatalf("the lock is held: %v", err)
	}
	if _, err := d.Exec("KILL ?", holder); err != nil {
		t.Fatal(err)
	}
	if err := unlock(ctx); err == nil {
		t.Fatal("unlocking on a dead session reports it")
	}
	u, ok, err := lock.NewMySQL(d).TryLock(ctx, "job")
	if err != nil || !ok {
		t.Fatalf("the lock is free again: %v %v", ok, err)
	}
	_ = u(ctx)
	if err := d.PingContext(ctx); err != nil {
		t.Fatalf("the pool still works: %v", err)
	}
}

// A date.Date and a time.Time round-trip through DATE and DATETIME(6), with the
// connection's parseTime on and off.
func TestMySQLDates(t *testing.T) {
	for _, parseTime := range []bool{true, false} {
		cfg := mysqlConfig(t)
		cfg.ParseTime = parseTime
		d := myDB(t)
		var name string
		if err := d.QueryRow("SELECT DATABASE()").Scan(&name); err != nil {
			t.Fatal(err)
		}
		cfg.DBName = name
		conn, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Exec("CREATE TABLE days (day DATE NOT NULL, at DATETIME(6) NOT NULL)"); err != nil {
			t.Fatal(err)
		}
		day := date.Date{Year: 2026, Month: time.October, Day: 28}
		at := time.Date(2026, 10, 28, 9, 30, 0, 123456000, time.UTC)
		if _, err := conn.Exec("INSERT INTO days VALUES (?, ?)", day, at); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec("INSERT INTO days VALUES (?, ?)", date.Date{}, at); err == nil {
			t.Fatal("the zero Date went into a NOT NULL column")
		}
		var got date.Date
		if err := conn.QueryRow("SELECT day FROM days").Scan(&got); err != nil || got != day {
			t.Fatalf("parseTime=%v: %v %v", parseTime, got, err)
		}
		if parseTime {
			var gotAt time.Time
			if err := conn.QueryRow("SELECT at FROM days").Scan(&gotAt); err != nil || !gotAt.Equal(at) {
				t.Fatalf("DATETIME(6): %v %v", gotAt, err)
			}
		}
	}
}
