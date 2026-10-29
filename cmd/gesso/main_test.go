package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ourRequires is this module's require lines, as a require block.
func ourRequires(t *testing.T) string {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("require (\n")
	inBlock := false
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "" && !strings.HasPrefix(line, "github.com/Elagoht/gesso "):
			path, rest, _ := strings.Cut(line, " ")
			version, _, _ := strings.Cut(rest, " ")
			b.WriteString("\t" + path + " " + version + "\n")
		}
	}
	b.WriteString(")\n")
	return b.String()
}

func fixedNow() time.Time { return time.Date(2026, 10, 28, 15, 30, 0, 0, time.UTC) }

func gessoRoot(t *testing.T) string {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMigrateNew(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"migrate", "new", "add_events", "-dir", "db/migrations"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	for _, f := range []string{"20261028153000_add_events.up.sql", "20261028153000_add_events.down.sql"} {
		if _, err := os.Stat(filepath.Join(dir, "db/migrations", f)); err != nil {
			t.Fatal(err)
		}
	}
	if code := run([]string{"migrate", "new", "add_events", "-dir", "db/migrations"}, dir, &out, &errOut, fixedNow); code == 0 {
		t.Fatal("a second migration of the same name is refused")
	}
	// Another in the same second takes the next one: the versions are one
	// history across the project.
	if code := run([]string{"migrate", "new", "add_city", "-dir", "other/migrations"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "other/migrations/20261028153001_add_city.up.sql")); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"migrate", "new", "Add Events"}, dir, &out, &errOut, fixedNow); code != 2 {
		t.Fatal("a bad name is refused")
	}
}

// goIn runs the go command in dir.
func goIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newApp is an empty module that uses the gesso in this repository. It requires
// every module ours does, at our versions: go never selects lower, so each module
// it resolves is one whose checksum our go.sum already holds.
func newApp(t *testing.T, require string) string {
	dir := t.TempDir()
	seedModule(t, dir, require)
	return dir
}

// seedModule writes dir's go.mod and go.sum as newApp describes.
func seedModule(t *testing.T, dir, require string) {
	t.Helper()
	mod := "module example.com/app\n\ngo 1.26.10\n\nrequire github.com/Elagoht/gesso v0.0.0\n" + require +
		ourRequires(t) + "\nreplace github.com/Elagoht/gesso => " + gessoRoot(t) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	// Every checksum the module will need is already in ours (deps_test.go).
	sums, err := os.ReadFile("go.sum")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sums, 0o644); err != nil {
		t.Fatal(err)
	}
}

// order is a keyword in every dialect, so its column is quoted.
const fields = "title:string,seats:int,starts_at:time,public:bool,price:float,order:int,body:text,day:date"

// The scaffold compiles, vets, passes its own tests and the ctx check — and its
// SQL runs: a test written beside it drives the repository against a real
// database, SQLite and PostgreSQL.
func TestAddModule(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs scaffolded modules")
	}
	cases := map[string]struct{ require, dbTest string }{
		"sqlite":   {"require modernc.org/sqlite v1.61.0\n", sqliteRepoTest},
		"postgres": {"require (\n\tgithub.com/fergusstrange/embedded-postgres v1.34.0\n\tgithub.com/jackc/pgx/v5 v5.11.0\n)\n", postgresRepoTest},
		// MySQL runs against GESSO_MYSQL_DSN, and skips — UNVERIFIED — without it.
		"mysql": {"require github.com/go-sql-driver/mysql v1.10.1\n", mysqlRepoTest},
	}
	for dialect, c := range cases {
		t.Run(dialect, func(t *testing.T) {
			dir := newApp(t, c.require)
			var out, errOut bytes.Buffer
			if code := run([]string{"add", "module", "events", "-fields", fields, "-dialect", dialect}, dir, &out, &errOut, fixedNow); code != 0 {
				t.Fatalf("code %d: %s", code, errOut.String())
			}
			if c.dbTest != "" {
				mod := filepath.Join(dir, "internal/events")
				if err := os.WriteFile(filepath.Join(mod, "repository_db_test.go"), []byte(c.dbTest), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			goIn(t, dir, "mod", "tidy")
			goIn(t, dir, "vet", "./...")
			reportUnverified(t, goIn(t, dir, "test", "-count=1", "-v", "./..."))
			var checkOut bytes.Buffer
			if n, err := check(nil, dir, &checkOut); err != nil || n != 0 {
				t.Fatalf("the scaffold breaks the ctx rule: %v\n%s", err, checkOut.String())
			}
			if code := run([]string{"add", "module", "events"}, dir, &out, &errOut, fixedNow); code == 0 {
				t.Fatal("an existing module is never overwritten")
			}
		})
	}
}

// The repository test placed beside a scaffold: migrations, then every method.
const repoTestBody = `
func exercise(t *testing.T, d *sql.DB, dialect migrate.Dialect) {
	ctx := context.Background()
	m, err := migrate.New(d, dialect, Migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	r := newRepository(d)
	at := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	day := date.Date{Year: 2026, Month: time.November, Day: 1}
	long := strings.Repeat("a long body ", 100)
	a, err := r.Insert(ctx, models.ReqCreateEvent{Title: "Jam", Seats: 30, StartsAt: at, Public: true, Price: 9.5, Order: 3, Body: long, Day: day})
	if err != nil || a.ID == 0 {
		t.Fatalf("insert: %+v %v", a, err)
	}
	b, _ := r.Insert(ctx, models.ReqCreateEvent{Title: "Talk", StartsAt: at, Day: day})
	got, err := r.ByKey(ctx, a.ID)
	if err != nil || got.Title != "Jam" || got.Seats != 30 || !got.StartsAt.Equal(at) || !got.Public || got.Price != 9.5 ||
		got.Order != 3 || got.Body != long || got.Day != day {
		t.Fatalf("by id: %+v %v", got, err)
	}
	in := models.ReqUpdateEvent{Key: a.ID, ReqCreateEvent: models.ReqCreateEvent{Title: "Jam 2", StartsAt: at, Order: 1, Body: "short", Day: day.AddDays(1)}}
	if _, err := r.Update(ctx, in); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, _ := r.ByKey(ctx, a.ID); got.Title != "Jam 2" || got.Order != 1 || got.Day != day.AddDays(1) || got.Public {
		t.Fatalf("updated: %+v", got)
	}
	if _, err := r.Update(ctx, in); err != nil {
		t.Fatalf("an update that changes nothing still finds the row: %v", err)
	}
	if _, err := r.Update(ctx, models.ReqUpdateEvent{Key: 999_999, ReqCreateEvent: models.ReqCreateEvent{StartsAt: at, Day: day}}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("update of a missing row: %v", err)
	}
	page, err := r.List(ctx, models.ReqListEvents{Take: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != a.ID || page.Next == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	page, err = r.List(ctx, models.ReqListEvents{After: page.Next, Take: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != b.ID || page.Next != "" {
		t.Fatalf("the page after: %+v %v", page, err)
	}
	// A patch writes what it sets — the keyword column, a date — and keeps
	// the rest.
	if _, err := r.Patch(ctx, models.ReqPatchEvent{Key: b.ID, Order: gesso.Some(int64(7)), Day: gesso.Some(day.AddDays(2))}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if got, _ := r.ByKey(ctx, b.ID); got.Order != 7 || got.Day != day.AddDays(2) || got.Title != "Talk" || !got.StartsAt.Equal(at) {
		t.Fatalf("patched: %+v", got)
	}
	if _, err := r.Patch(ctx, models.ReqPatchEvent{Key: b.ID}); err != nil {
		t.Fatalf("a patch that sets nothing still finds the row: %v", err)
	}
	if _, err := r.Patch(ctx, models.ReqPatchEvent{Key: 999_999, Title: gesso.Some("x")}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("patch of a missing row: %v", err)
	}
	if err := r.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ByKey(ctx, a.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted: %v", err)
	}
	if err := r.Delete(ctx, a.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("delete twice: %v", err)
	}
}
`

const sqliteRepoTest = `package events

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/date"
	"github.com/Elagoht/gesso/migrate"

	"example.com/app/internal/events/models"
	_ "modernc.org/sqlite"
)

func TestRepositorySQLite(t *testing.T) {
	d, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exercise(t, d, migrate.SQLite)
}
` + repoTestBody

const postgresRepoTest = `package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/date"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/Elagoht/gesso/migrate"

	"example.com/app/internal/events/models"
)

func TestRepositoryPostgres(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	dir := t.TempDir()
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "rt")).DataPath(filepath.Join(dir, "data")).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		t.Skipf("POSTGRES UNVERIFIED — no server: %v", err)
	}
	defer pg.Stop()
	d, err := sql.Open("pgx", fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exercise(t, d, migrate.Postgres)
}
` + repoTestBody

const mysqlRepoTest = `package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/date"
	"github.com/Elagoht/gesso/migrate"

	"example.com/app/internal/events/models"
	"github.com/go-sql-driver/mysql"
)

func TestRepositoryMySQL(t *testing.T) {
	exercise(t, openMySQL(t), migrate.MySQL)
}
` + repoTestBody + mysqlOpen

// mysqlOpen is the generated tests' MySQL: a database of their own on the
// server GESSO_MYSQL_DSN names.
const mysqlOpen = `
func openMySQL(t *testing.T) *sql.DB {
	dsn := os.Getenv("GESSO_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MYSQL UNVERIFIED — GESSO_MYSQL_DSN is not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MultiStatements, cfg.ParseTime = true, true
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("gesso_cli_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a, err := sql.Open("mysql", cfg.FormatDSN()); err == nil {
			_, _ = a.Exec("DROP DATABASE " + name)
			_ = a.Close()
		}
	})
	cfg.DBName = name
	d, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
`

// reportUnverified shows a generated test's skip: a database it could not
// reach left its SQL unverified, which CI must see, not mistake for a pass.
func reportUnverified(t *testing.T, out string) {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "UNVERIFIED") {
			t.Log(strings.TrimSpace(line))
		}
	}
}

func TestCheck(t *testing.T) {
	dir := newApp(t, "")
	files := map[string]string{
		"internal/users/repository.go": `package users

import (
	"context"
	"database/sql"
)

type Repository struct{ db *sql.DB }

func (r *Repository) Count() (int, error) {
	var n int
	err := r.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

func (r *Repository) Rename(ctx context.Context, id int64, name string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE users SET name = $1 WHERE id = $2", name, id)
	return err
}

func (r *Repository) helper() {}
`,
		"internal/users/service.go": `package users

import "context"

type Service struct{ r *Repository }

func (s *Service) Total() (int, error) { return s.r.Count() }

func (s *Service) Rename(ctx context.Context, id int64, name string) error { return s.r.Rename(ctx, id, name) }
`,
		"internal/users/jobs.go": `package users

import "database/sql"

func Purge(d *sql.DB) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM users WHERE deleted")
	if err != nil {
		return tx.Rollback()
	}
	return tx.Commit()
}

// Total is not a service method: jobs.go is not a layer file.
func (s *Service) Report() int { return 0 }
`,
	}
	for name, src := range files {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	goIn(t, dir, "mod", "tidy")
	var out, errOut bytes.Buffer
	code := run([]string{"check"}, dir, &out, &errOut, fixedNow)
	got := out.String()
	if code != 1 {
		t.Fatalf("code %d, out %s, err %s", code, got, errOut.String())
	}
	for _, want := range []string{
		"internal/users/jobs.go:6:13: sql.DB.Begin runs without a context: call BeginTx",
		"internal/users/jobs.go:10:11: sql.Tx.Exec runs without a context: call ExecContext",
		"internal/users/repository.go:10:1: Count takes no ctx",
		"internal/users/repository.go:12:9: sql.DB.QueryRow runs without a context",
		"internal/users/service.go:7:1: Total takes no ctx",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 5 {
		t.Errorf("want 5 findings, got %d:\n%s", n, got)
	}
}
