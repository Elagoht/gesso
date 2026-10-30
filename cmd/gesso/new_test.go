package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/go-sql-driver/mysql"
)

// A project from gesso new — sign-in, a guard, a module with every option gen
// knows and a hook, an empty module, an endpoint, a command, a middleware —
// builds, vets, and passes its tests on each dialect: its own, and one that
// drives the module over HTTP on the database.
func TestNew(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a new project")
	}
	cases := map[string]struct {
		database func(t *testing.T) string // TEST_DATABASE_URL, "" for none
	}{
		"sqlite":   {nil},
		"postgres": {embeddedPostgres},
		"mysql":    {mysqlDatabase},
	}
	for dialect, c := range cases {
		t.Run(dialect, func(t *testing.T) {
			root := t.TempDir()
			var out, errOut bytes.Buffer
			if code := run([]string{"new", "example.com/app", "-dialect", dialect}, root, &out, &errOut, fixedNow); code != 0 {
				t.Fatalf("code %d: %s", code, errOut.String())
			}
			dir := filepath.Join(root, "app")
			for _, f := range []string{"main.go", "register.go", "main_test.go"} {
				if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "internal")); err == nil {
				t.Fatal("a new project has no internal/ until something is added")
			}
			seedModule(t, dir, "")
			do := func(args ...string) string {
				t.Helper()
				out.Reset()
				errOut.Reset()
				if code := run(args, dir, &out, &errOut, fixedNow); code != 0 {
					t.Fatalf("gesso %s: code %d: %s", strings.Join(args, " "), code, errOut.String())
				}
				return out.String()
			}
			if code := run([]string{"add", "module", "x", "-guard", "admin"}, dir, &out, &errOut, fixedNow); code == 0 {
				t.Fatal("a guard that does not exist")
			}
			tokens := map[string]string{"sqlite": "session", "postgres": "jwt", "mysql": "jwt"}[dialect]
			do("add", "auth", "-tokens", tokens, "-access", "10m", "-field", "name:string:optional,self,collapse,max=100", "-field", "avatar:image:self,public,listable,fill=64x64")
			if tokens == "jwt" {
				if env, _ := os.ReadFile(filepath.Join(dir, ".env.development")); !strings.Contains(string(env), "JWT_SECRET=") {
					t.Fatal("no JWT_SECRET in .env.development")
				}
			}
			if code := run([]string{"add", "auth"}, dir, &out, &errOut, fixedNow); code == 0 {
				t.Fatal("auth is added twice")
			}
			do("add", "module", "events", "-sortable", "-guard", "signedIn", "-field", "title:string:listable,max=200")
			if code := run([]string{"add", "module", "events"}, dir, &out, &errOut, fixedNow); code == 0 {
				t.Fatal("an existing module is never overwritten")
			}
			// The model as TestNew wants it, then gen: the model is the source.
			models := filepath.Join(dir, "internal/events/models/models.go")
			_ = os.Remove(filepath.Join(dir, "internal/events/models/schema.gen.json"))
			migrations, _ := filepath.Glob(filepath.Join(dir, "internal/events/migrations/*.sql"))
			for _, m := range migrations {
				_ = os.Remove(m)
			}
			if err := os.WriteFile(models, []byte(eventsModel), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "internal/events/hooks.go"), []byte(eventsHook), 0o644); err != nil {
				t.Fatal(err)
			}
			if gen := do("gen"); !strings.Contains(gen, "module.gen.go") {
				t.Fatalf("gen:\n%s", gen)
			}
			if mod, _ := os.ReadFile(filepath.Join(dir, "internal/events/module.gen.go")); !strings.Contains(string(mod), "m.BeforeCreate(ctx, &e)") {
				t.Fatalf("gen does not call the hook:\n%s", mod)
			}
			do("add", "endpoint", "events/count", "-method", "GET", "-res", "total:int")
			do("add", "module", "posts", "-guard", "signedIn",
				"-field", "title:string:max=200", "-field", "slug:string:key,slug", "-field", "score:int:optional,sort", "-field", "day:date:optional")
			if code := run([]string{"add", "module", "leaky", "-rbac", "-field", "cover:image"}, dir, &out, &errOut, fixedNow); code == 0 {
				t.Fatal("a file of a //gesso:rbac model with no public or routes")
			}
			if _, err := os.Stat(filepath.Join(dir, "internal/leaky")); err == nil {
				t.Fatal("a refused module leaves its folder")
			}
			do("add", "module", "tasks", "-rbac", "-owner", "-field", "title:string:listable,max=100",
				"-field", "visible:bool:gate=admin,default=true", "-field", "brief:file:routes,accept=application/pdf,maxsize=1MiB")
			reg, _ := os.ReadFile(filepath.Join(dir, "register.go"))
			roles := `app.Roles(gesso.Roles{"admin": {"*"}, gesso.Everyone: {}})`
			if !strings.Contains(string(reg), roles) {
				t.Fatalf("add module -rbac wires no role table:\n%s", reg)
			}
			reg = []byte(strings.Replace(string(reg), roles, `app.Roles(gesso.Roles{
		"admin":  {"*"},
		"writer": {"tasks:create", "tasks:list:own", "tasks:get:own", "tasks:update:own", "tasks:delete:own"},
	})`, 1))
			if err := os.WriteFile(filepath.Join(dir, "register.go"), reg, 0o644); err != nil {
				t.Fatal(err)
			}
			do("add", "module", "pages", "-empty")
			do("add", "endpoint", "pages/home", "-method", "GET", "-path", "/", "-res", "title:string")
			do("add", "command", "seed")
			do("add", "middleware", "timing")
			reg, _ = os.ReadFile(filepath.Join(dir, "register.go"))
			for _, want := range []string{"users.New(),", "events.New(),", "pages.New(),", `app.Command("seed", commands.Seed(app))`, "app.Use(middleware.Timing)"} {
				if !strings.Contains(string(reg), want) {
					t.Fatalf("register.go lacks %s:\n%s", want, reg)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "features_test.go"), []byte(featuresTest), 0o644); err != nil {
				t.Fatal(err)
			}
			var dsn string
			if c.database != nil {
				if dsn = c.database(t); dsn != "" {
					t.Setenv("TEST_DATABASE_URL", dsn)
					t.Setenv("DATABASE_URL", dsn)
				}
			}
			goIn(t, dir, "mod", "tidy")
			goIn(t, dir, "vet", "./...")
			testOut := goIn(t, dir, "test", "-count=1", "-v", "./...")
			if dialect != "sqlite" && dsn == "" {
				t.Logf("%s UNVERIFIED: the new app's tests ran without a database", strings.ToUpper(dialect))
			} else if !strings.Contains(testOut, "--- PASS: TestEvents") {
				t.Fatalf("the events test did not run:\n%s", testOut)
			}
			bin := filepath.Join(t.TempDir(), "app")
			goIn(t, dir, "build", "-o", bin, ".")
			if dialect == "sqlite" {
				migrateRoundTrip(t, bin, filepath.Join(t.TempDir(), "m.db"))
				dsn = filepath.Join(t.TempDir(), "change.db")
			}
			if dsn != "" {
				changeModel(t, dir, bin, dialect, dsn)
			}
			if n, err := check(nil, dir, &out); err != nil || n != 0 {
				t.Fatalf("the project breaks the ctx rule: %v %s", err, out.String())
			}
		})
	}
	t.Run("refuses", func(t *testing.T) {
		root := t.TempDir()
		var out, errOut bytes.Buffer
		if code := run([]string{"new", "example.com/x", "-dialect", "oracle"}, root, &out, &errOut, fixedNow); code == 0 {
			t.Fatal("an unknown dialect")
		}
		if code := run([]string{"new", "Not A Path"}, root, &out, &errOut, fixedNow); code == 0 {
			t.Fatal("a bad module path")
		}
		_ = os.MkdirAll(filepath.Join(root, "x"), 0o755)
		_ = os.WriteFile(filepath.Join(root, "x", "keep"), nil, 0o644)
		if code := run([]string{"new", "example.com/x"}, root, &out, &errOut, fixedNow); code == 0 {
			t.Fatal("a folder that is not empty")
		}
	})
}

// migrateRoundTrip runs the built app's migrate command: up, status, down.
func migrateRoundTrip(t *testing.T, bin, dbPath string) {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command(bin, append([]string{"migrate"}, args...)...)
		cmd.Env = append(os.Environ(), "DATABASE_URL="+dbPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("migrate %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := run("up"); strings.Count(out, "applied") != 4 {
		t.Fatalf("up: %s", out)
	}
	if out := run("down"); strings.Count(out, "reverted") != 1 {
		t.Fatalf("down: %s", out)
	}
	out := run("status")
	if strings.Count(out, "applied") != 3 || strings.Count(out, "pending") != 1 {
		t.Fatalf("status: %s", out)
	}
	cmd := exec.Command(bin, "migrate", "sideways")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dbPath)
	if err := cmd.Run(); err == nil {
		t.Fatal("an unknown subcommand succeeded")
	}
}

// embeddedPostgres starts a server for the test and names its database.
func embeddedPostgres(t *testing.T) string {
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
		t.Logf("POSTGRES UNVERIFIED — no server: %v", err)
		return ""
	}
	t.Cleanup(func() { _ = pg.Stop() })
	return fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
}

// mysqlDatabase makes a database on GESSO_MYSQL_DSN's server and names it.
func mysqlDatabase(t *testing.T) string {
	dsn := os.Getenv("GESSO_MYSQL_DSN")
	if dsn == "" {
		t.Log("MYSQL UNVERIFIED — GESSO_MYSQL_DSN is not set")
		return ""
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("gesso_new_%d", time.Now().UnixNano())
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
	return cfg.FormatDSN()
}

// gesso init puts internal/api in a module that exists — a collage site — and
// add module registers into it, as into a gesso new project.
func TestInit(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"init"}, dir, &out, &errOut, fixedNow); code == 0 {
		t.Fatal("init without a go.mod")
	}
	seedModule(t, dir, "")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"init"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `api.New(cmp.Or(os.Getenv("DATABASE_URL"), "app.db"), "/api")`) {
		t.Fatalf("the next steps:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod.orig")); err == nil {
		t.Fatal("init touched go.mod")
	}
	if main, _ := os.ReadFile(filepath.Join(dir, "main.go")); string(main) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("init wrote over main.go: %s", main)
	}
	if code := run([]string{"init"}, dir, &out, &errOut, fixedNow); code == 0 {
		t.Fatal("init twice")
	}
	out.Reset()
	if code := run([]string{"add", "module", "notes", "-fields", "title:string"}, dir, &out, &errOut, fixedNow); code != 0 || !strings.Contains(out.String(), "wired") {
		t.Fatalf("add module: %d %s %s", code, out.String(), errOut.String())
	}
	goIn(t, dir, "mod", "tidy")
	goIn(t, dir, "vet", "./...")
	goIn(t, dir, "test", "-count=1", "./...")
}

func TestWireRegister(t *testing.T) {
	src := `package app

import "github.com/Elagoht/gesso"

func register(a *gesso.App) error {
	return a.Register(b.New())
}
`
	got, err := wireRegister([]byte(src), "notes.New()", "example.com/app/internal/notes")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"b.New(),\n\t\tnotes.New(),\n\t)", `"example.com/app/internal/notes"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if _, err := wireRegister(got, "notes.New()", "example.com/app/internal/notes"); err == nil {
		t.Error("registering a module twice")
	}
	got, err = wireStatement(got, func(app string) string { return app + ".Use(m.Log)" }, "example.com/app/internal/m")
	if err != nil || !strings.Contains(string(got), "a.Use(m.Log)\n\treturn a.Register(") {
		t.Fatalf("statement: %v\n%s", err, got)
	}
	for name, src := range map[string]string{
		"no register":       "package app\n",
		"another ending":    "package app\n\nfunc register(app *gesso.App) error {\n\treturn nil\n}\n",
		"another parameter": "package app\n\nfunc register() error {\n\treturn nil\n}\n",
	} {
		if _, err := wireRegister([]byte(src), "x.New()", "x"); err == nil {
			t.Errorf("%s: wired", name)
		}
	}
}

// TestGoModFile: a released CLI pins the gesso it was released with; a build
// from a checkout leaves the choice to go mod tidy.
func TestGoModFile(t *testing.T) {
	if got, want := string(goModFile("example.com/docs", "v0.9.2")), "module example.com/docs\n\ngo 1.26.10\n\nrequire github.com/Elagoht/gesso v0.9.2\n"; got != want {
		t.Errorf("released:\n%s\nwant:\n%s", got, want)
	}
	if got := string(goModFile("example.com/docs", "")); strings.Contains(got, "require") {
		t.Errorf("devel build requires a version:\n%s", got)
	}
	if v := gessoVersion(); v != "" {
		t.Errorf("a test binary is no release, and gessoVersion = %q", v)
	}
}

// changeModel changes the posts model of a migrated app with rows in it — a new
// field, a field made unique — and gen's migration applies over the rows; a
// field taken away is refused, and gen is idempotent.
func changeModel(t *testing.T, dir, bin, dialect, dsn string) {
	t.Helper()
	migrateUp := func() {
		cmd := exec.Command(bin, "migrate", "up")
		cmd.Env = append(os.Environ(), "DATABASE_URL="+dsn)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("migrate up: %v\n%s", err, out)
		}
	}
	migrateUp()
	driver := map[string]string{"sqlite": "sqlite", "postgres": "pgx", "mysql": "mysql"}[dialect]
	d, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("INSERT INTO posts (title, slug, score) VALUES ('One', 'one', 2), ('Two', 'two', 1)"); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(dir, "internal/posts/models/models.go")
	src, _ := os.ReadFile(models)
	changed := strings.Replace(string(src), "`gesso:\"max=200\"`", "`gesso:\"max=200,unique\"`\n\tSubtitle string `gesso:\"optional,max=80\"`", 1)
	if changed == string(src) {
		t.Fatalf("the model is not as add module wrote it:\n%s", src)
	}
	if err := os.WriteFile(models, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"gen"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("gen: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "posts_index_title_add_subtitle.up.sql") {
		t.Fatalf("gen wrote no migration:\n%s", out.String())
	}
	out.Reset()
	if code := run([]string{"gen"}, dir, &out, &errOut, fixedNow); code != 0 || out.Len() != 0 {
		t.Fatalf("a second gen changed something: %d\n%s", code, out.String())
	}
	if code := run([]string{"gen", "-check"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("gen -check after gen: %s", out.String())
	}
	goIn(t, dir, "build", "-o", bin, ".")
	migrateUp()
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM posts WHERE subtitle = ''").Scan(&n); err != nil || n != 2 {
		t.Fatalf("the rows that were there have the new column empty: %d %v", n, err)
	}
	_, err = d.Exec("INSERT INTO posts (title, slug, score, subtitle) VALUES ('One', 'uno', 3, '')")
	if err == nil {
		t.Fatal("title is unique now")
	}
	// The name the database's error gives is the one Constraints maps to the
	// field, so a duplicate is a 409 on title, not a 500.
	name := map[string]string{"sqlite": "posts.title", "postgres": "posts_title_unique", "mysql": "posts.posts_title_unique"}[dialect]
	repo, _ := os.ReadFile(filepath.Join(dir, "internal/posts/module.gen.go"))
	if !strings.Contains(err.Error(), name) || !strings.Contains(string(repo), `"`+name+`": "title"`) {
		t.Fatalf("the error names %q, Constraints:\n%s", err, repo)
	}
	if err := os.WriteFile(models, []byte(strings.Replace(changed, "\tSubtitle string `gesso:\"optional,max=80\"`\n", "", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"gen"}, dir, &out, &errOut, fixedNow); code == 0 || !strings.Contains(errOut.String(), "-drop") {
		t.Fatalf("a field taken away is dropped without -drop: %s", errOut.String())
	}
	if err := os.WriteFile(models, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
}
