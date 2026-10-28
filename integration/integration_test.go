package integration

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/gessotest"
	"github.com/Elagoht/gesso/integration/notes"
	"github.com/Elagoht/gesso/migrate"
	"github.com/Elagoht/gesso/token"
	"github.com/Elagoht/gesso/token/tokentest"
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

func TestInTxOnThePool(t *testing.T) { inTxSuite(t, openDB(t)) }

func inTxSuite(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	if _, err := d.Exec("CREATE TABLE notes (slug VARCHAR(40) PRIMARY KEY, title TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
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

func TestMigrations(t *testing.T) { migrationSuite(t, openDB(t), migrate.SQLite) }

func migrationSuite(t *testing.T, d *sql.DB, dialect migrate.Dialect) {
	ctx := context.Background()
	files := fstest.MapFS{
		"20261028100000_a.up.sql":   {Data: []byte("CREATE TABLE a (id INTEGER);")},
		"20261028100000_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"20261028110000_b.up.sql":   {Data: []byte("CREATE TABLE b (id INTEGER); CREATE TABLE b2 (id INTEGER);")},
		"20261028110000_b.down.sql": {Data: []byte("DROP TABLE b2; DROP TABLE b;")},
	}
	m, err := migrate.New(d, dialect, files)
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
	m2, _ := migrate.New(d, dialect, edited)
	if _, err := m2.Up(ctx); !errors.Is(err, migrate.ErrChanged) {
		t.Fatalf("got %v", err)
	}
	if st, _ := m2.Status(ctx); st[0].State != migrate.Changed {
		t.Fatalf("got %+v", st)
	}

	// An older migration that arrives late stops Up, unless allowed.
	late := fstest.MapFS{"20261028090000_late.up.sql": {Data: []byte("CREATE TABLE late (id INTEGER);")}}
	m3, _ := migrate.New(d, dialect, files, late)
	if _, err := m3.Up(ctx); !errors.Is(err, migrate.ErrOutOfOrder) {
		t.Fatalf("got %v", err)
	}
	m3.AllowOutOfOrder = true
	if done, err := m3.Up(ctx); err != nil || len(done) != 1 {
		t.Fatalf("got %v %+v", err, done)
	}

	// A failing migration is rolled back and recorded as nothing.
	bad := fstest.MapFS{"20261028120000_bad.up.sql": {Data: []byte("CREATE TABLE c (id INTEGER); NOT SQL;")}}
	m4, _ := migrate.New(d, dialect, files, late, bad)
	if _, err := m4.Up(ctx); err == nil || !strings.Contains(err.Error(), "20261028120000_bad") {
		t.Fatalf("got %v", err)
	}
	if _, err := d.Exec("SELECT * FROM c"); err == nil {
		t.Fatal("the failed migration's first statement outlived it")
	}
}

func TestTokenSQLStore(t *testing.T) {
	d := openDB(t)
	if err := token.Migrate(context.Background(), d, migrate.SQLite); err != nil {
		t.Fatal(err)
	}
	tokentest.Run(t, func(t *testing.T) token.Store {
		return token.NewSQLStore(gessotest.Tx(t, d), migrate.SQLite)
	})
}

// A whole login flow through the app: issue at login, read with the bearer
// header, refuse after logout.
func TestBearerFlow(t *testing.T) {
	// The application's migrations ran first and are newer; the token migrations
	// keep a history of their own and still apply.
	d := migrated(t)
	if err := token.Migrate(context.Background(), d, migrate.SQLite); err != nil {
		t.Fatal(err)
	}
	tx := gessotest.Tx(t, d)
	iss := token.NewIssuer(token.NewSQLStore(tx, migrate.SQLite), nil, time.Hour)
	type loginIn struct {
		User string `json:"user" validate:"required"`
	}
	type loginOut struct {
		Token string `json:"token"`
	}
	app := gessotest.New(t, gesso.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, moduleFunc(func(r *gesso.Router) {
		r.Authenticate(iss)
		r.Post("/login", gesso.Handle(func(ctx context.Context, in loginIn) (loginOut, error) {
			raw, _, err := iss.Issue(ctx, in.User)
			return loginOut{Token: raw}, err
		}))
		r.Post("/logout", gesso.Handle(func(ctx context.Context, _ gesso.Empty) (gesso.Empty, error) {
			return gesso.Empty{}, iss.Revoke(ctx, token.FromRequest(gesso.Request(ctx)))
		}, gesso.Guards(gesso.RequireIdentity)))
		r.Get("/me", gesso.Handle(func(ctx context.Context, _ gesso.Empty) (loginOut, error) {
			id, _ := gesso.IdentityFrom(ctx)
			return loginOut{Token: id.Subject}, nil
		}, gesso.Guards(gesso.RequireIdentity)))
	}))
	c := gessotest.NewClient(t, app)
	c.Get("/me").ExpectProblem(401, "unauthenticated")
	raw := gessotest.Decode[loginOut](c.Post("/login", gessotest.JSON(loginIn{User: "ada"})).ExpectStatus(200)).Token
	authed := c.WithHeader("Authorization", "Bearer "+raw)
	if got := gessotest.Decode[loginOut](authed.Get("/me").ExpectStatus(200)); got.Token != "ada" {
		t.Fatalf("got %+v", got)
	}
	authed.Post("/logout", gessotest.Body{}).ExpectStatus(204)
	authed.Get("/me").ExpectProblem(401, "invalid_token")
}

type moduleFunc func(r *gesso.Router)

func (f moduleFunc) Routes(r *gesso.Router) { f(r) }

// Two processes: the app behind a real HTTP server, and a caller (collage) that
// shares only the notes package's endpoints and types.
func TestNotesAcrossProcesses(t *testing.T) {
	d := migrated(t)
	app := gessotest.New(t, gesso.Config{
		BasePath:     "/api",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ErrorMappers: []gesso.ErrorMapper{db.Errors(classify, map[string]string{"notes.slug": "slug"})},
	}, notes.New(notes.Deps{DB: d}))
	srv := httptest.NewServer(app)
	defer srv.Close()
	api := gesso.NewRemote(srv.URL+"/api", srv.Client())
	ctx := context.Background()

	created, err := notes.CreateNote.Call(ctx, api, notes.CreateIn{Slug: "jam", Title: "Game jam"})
	if err != nil || created.ID == 0 {
		t.Fatalf("got %+v %v", created, err)
	}
	got, err := notes.GetNote.Call(ctx, api, notes.GetIn{Slug: "jam"})
	if err != nil || got != created {
		t.Fatalf("got %+v %v", got, err)
	}
	_, err = notes.CreateNote.Call(ctx, api, notes.CreateIn{Slug: "jam", Title: "Again"})
	if fe := gesso.FieldErrors(err); len(fe) != 1 || fe[0].Field != "slug" {
		t.Fatalf("a form gets its field errors across the wire: %v %+v", err, fe)
	}
	if _, err := notes.GetNote.Call(ctx, api, notes.GetIn{Slug: "nope"}); !errors.Is(err, gesso.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
