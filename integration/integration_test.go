package integration

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/gessotest"
	"github.com/Elagoht/gesso/integration/notes"
	"github.com/Elagoht/gesso/migrate"
	"modernc.org/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func migrated(t *testing.T) *sql.DB {
	t.Helper()
	d := openDB(t)
	m, err := migrate.New(d, migrate.SQLite, notes.Migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d
}

// classify is the one line an application writes for its driver.
func classify(err error) (db.Violation, bool) {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return db.Violation{}, false
	}
	return db.SQLiteViolation(se.Code(), se.Error())
}

func notesApp(t *testing.T, d db.DB) *gessotest.Client {
	app := gessotest.New(t, gesso.Config{
		BasePath:     "/api",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ErrorMappers: []gesso.ErrorMapper{db.Errors(classify, map[string]string{"notes.slug": "slug"})},
	}, notes.New(notes.Deps{DB: d}))
	return gessotest.NewClient(t, app)
}

func TestNotesEndToEnd(t *testing.T) {
	tx := gessotest.Tx(t, migrated(t))
	c := notesApp(t, tx)

	res := c.Post("/api/notes", gessotest.JSON(notes.CreateIn{Slug: "jam", Title: "Game jam"})).ExpectStatus(201)
	created := gessotest.Decode[notes.Note](res)
	if created.ID == 0 || created.Slug != "jam" {
		t.Fatalf("got %+v", created)
	}

	got := gessotest.Decode[notes.Note](c.Get("/api/notes/jam").ExpectStatus(200))
	if got != created {
		t.Fatalf("got %+v, want %+v", got, created)
	}

	c.Get("/api/notes/nope").ExpectProblem(404, "not_found")

	p := c.Post("/api/notes", gessotest.JSON(notes.CreateIn{Slug: "jam", Title: "Again"})).ExpectProblem(409, "conflict")
	if len(p.Errors) != 1 || p.Errors[0].Field != "slug" || p.Errors[0].Code != "unique" {
		t.Fatalf("a duplicate names its field: %+v", p)
	}

	p = c.Post("/api/notes", gessotest.JSON(notes.CreateIn{})).ExpectProblem(400, "invalid_input")
	if len(p.Errors) != 2 {
		t.Fatalf("got %+v", p)
	}
}

func TestBatchIsAllOrNoneInsideATestTransaction(t *testing.T) {
	tx := gessotest.Tx(t, migrated(t))
	c := notesApp(t, tx)

	// The second note collides with the first: the service's InTx, nested in the
	// test's transaction as a savepoint, rolls the whole batch back.
	c.Post("/api/notes/batch", gessotest.JSON(notes.BatchIn{Notes: []notes.CreateIn{
		{Slug: "a", Title: "A"}, {Slug: "a", Title: "A again"},
	}})).ExpectProblem(409, "conflict")
	c.Get("/api/notes/a").ExpectProblem(404, "not_found")

	// The test's transaction is still usable after the inner rollback.
	res := c.Post("/api/notes/batch", gessotest.JSON(notes.BatchIn{Notes: []notes.CreateIn{
		{Slug: "a", Title: "A"}, {Slug: "b", Title: "B"},
	}})).ExpectStatus(201)
	if n := len(gessotest.Decode[[]notes.Note](res)); n != 2 {
		t.Fatalf("got %d notes", n)
	}
}

func TestTestTransactionRollsBack(t *testing.T) {
	d := migrated(t)
	t.Run("writes", func(t *testing.T) {
		c := notesApp(t, gessotest.Tx(t, d))
		c.Post("/api/notes", gessotest.JSON(notes.CreateIn{Slug: "gone", Title: "Gone"})).ExpectStatus(201)
	})
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM notes").Scan(&n); err != nil || n != 0 {
		t.Fatalf("count %d, err %v: the test's writes outlived it", n, err)
	}
}

func TestInTxOnThePool(t *testing.T) {
	d := migrated(t)
	ctx := context.Background()
	errStop := errors.New("stop")

	err := db.InTx(ctx, d, func(tx db.DB) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO notes (slug, title) VALUES ('x', 'X')"); err != nil {
			return err
		}
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("got %v", err)
	}
	if err := db.InTx(ctx, d, func(tx db.DB) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO notes (slug, title) VALUES ('y', 'Y')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		_ = db.InTx(ctx, d, func(tx db.DB) error {
			_, _ = tx.ExecContext(ctx, "INSERT INTO notes (slug, title) VALUES ('z', 'Z')")
			panic("mid-transaction")
		})
	}()
	var slugs []string
	rows, err := d.Query("SELECT slug FROM notes ORDER BY slug")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		slugs = append(slugs, s)
	}
	if strings.Join(slugs, ",") != "y" {
		t.Fatalf("only the committed unit stays: %v", slugs)
	}
}

func TestMigrations(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	files := fstest.MapFS{
		"20261028100000_a.up.sql":   {Data: []byte("CREATE TABLE a (id INTEGER);")},
		"20261028100000_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"20261028110000_b.up.sql":   {Data: []byte("CREATE TABLE b (id INTEGER); CREATE TABLE b2 (id INTEGER);")},
		"20261028110000_b.down.sql": {Data: []byte("DROP TABLE b2; DROP TABLE b;")},
	}
	m, err := migrate.New(d, migrate.SQLite, files)
	if err != nil {
		t.Fatal(err)
	}
	done, err := m.Up(ctx)
	if err != nil || len(done) != 2 {
		t.Fatalf("up: %v, %d applied", err, len(done))
	}
	if done, err := m.Up(ctx); err != nil || len(done) != 0 {
		t.Fatalf("a second up applies nothing: %v, %d", err, len(done))
	}
	if _, err := d.Exec("INSERT INTO b2 VALUES (1)"); err != nil {
		t.Fatalf("every statement of a file runs: %v", err)
	}

	if done, err := m.Down(ctx, 1); err != nil || len(done) != 1 || done[0].Name != "b" {
		t.Fatalf("down: %v %+v", err, done)
	}
	st, err := m.Status(ctx)
	if err != nil || len(st) != 2 || st[0].State != migrate.Applied || st[1].State != migrate.Pending {
		t.Fatalf("status: %v %+v", err, st)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}

	// An edited applied migration stops Up.
	edited := fstest.MapFS{}
	for k, v := range files {
		edited[k] = v
	}
	edited["20261028100000_a.up.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE a (id INTEGER, extra TEXT);")}
	m2, _ := migrate.New(d, migrate.SQLite, edited)
	if _, err := m2.Up(ctx); !errors.Is(err, migrate.ErrChanged) {
		t.Fatalf("got %v", err)
	}
	if st, _ := m2.Status(ctx); st[0].State != migrate.Changed {
		t.Fatalf("got %+v", st)
	}

	// An older migration that arrives late stops Up, unless allowed.
	late := fstest.MapFS{"20261028090000_late.up.sql": {Data: []byte("CREATE TABLE late (id INTEGER);")}}
	m3, _ := migrate.New(d, migrate.SQLite, files, late)
	if _, err := m3.Up(ctx); !errors.Is(err, migrate.ErrOutOfOrder) {
		t.Fatalf("got %v", err)
	}
	m3.AllowOutOfOrder = true
	if done, err := m3.Up(ctx); err != nil || len(done) != 1 {
		t.Fatalf("got %v %+v", err, done)
	}

	// A failing migration is rolled back and recorded as nothing.
	bad := fstest.MapFS{"20261028120000_bad.up.sql": {Data: []byte("CREATE TABLE c (id INTEGER); NOT SQL;")}}
	m4, _ := migrate.New(d, migrate.SQLite, files, late, bad)
	if _, err := m4.Up(ctx); err == nil || !strings.Contains(err.Error(), "20261028120000_bad") {
		t.Fatalf("got %v", err)
	}
	if _, err := d.Exec("SELECT * FROM c"); err == nil {
		t.Fatal("the failed migration's first statement outlived it")
	}
}
