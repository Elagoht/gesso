package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"text/template"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

var newTemplates = template.Must(template.New("").ParseFS(scaffoldFS, "scaffold/new/*.tmpl"))

type projectSpec struct {
	Module      string // example.com/<name>
	Name        string // <name>
	Dialect     string // sqlite
	DialectGo   string // SQLite
	DialectName string // SQLite
	DSNDoc      string
	DefaultDSN  string
}

var modulePath = regexp.MustCompile(`^[a-z0-9][a-z0-9._~-]*(/[A-Za-z0-9._~-]+)*$`)

// newProject is gesso new: a project with its main, the app that opens the
// database and migrates it, its register.go and a test that starts it. With
// -template demo, sign-in and a notes module too; with -ai, AGENTS.md.
func newProject(args []string, dir string, stdout io.Writer, now func() time.Time) error {
	fs := newFlags("new")
	dialect := fs.String("dialect", "sqlite", "the database: sqlite, postgres or mysql")
	into := fs.String("dir", "", "the folder to create (default: the module path's last element)")
	tmpl := fs.String("template", "minimal", "minimal, the app alone; demo, with sign-in and a notes module")
	force := fs.Bool("force", false, "write into a folder that is not empty, replacing the files the scaffold has")
	ai := fs.Bool("ai", false, "write AGENTS.md, gesso's rules for coding agents")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !modulePath.MatchString(pos[0]) {
		return usage("new takes a module path: gesso new <module path>")
	}
	if *tmpl != "minimal" && *tmpl != "demo" {
		msg := fmt.Sprintf("unknown -template %q: minimal or demo", *tmpl)
		if c := closest(*tmpl, []string{"minimal", "demo"}); c != "" {
			msg += fmt.Sprintf(" (did you mean %q?)", c)
		}
		return usage(msg)
	}
	spec, err := newProjectSpec(pos[0], *dialect)
	if err != nil {
		return err
	}
	target := *into
	if target == "" {
		target = spec.Name
	}
	target = filepath.Join(dir, target)
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 && !*force {
		return fmt.Errorf("%s exists and is not empty: -force writes into it", target)
	}
	files := map[string]string{
		"main.go":                  "main.go.tmpl",
		"internal/api/api.go":      "api.go.tmpl",
		"internal/api/register.go": "register.go.tmpl",
		"internal/api/api_test.go": "api_test.go.tmpl",
		".gitignore":               "gitignore.tmpl",
		".env.example":             "env.example.tmpl",
		"README.md":                "README.md.tmpl",
	}
	for _, name := range []string{"main.go", "internal/api/api.go", "internal/api/register.go", "internal/api/api_test.go", ".gitignore", ".env.example", "README.md"} {
		var buf bytes.Buffer
		if err := newTemplates.ExecuteTemplate(&buf, files[name], spec); err != nil {
			return err
		}
		src := buf.Bytes()
		if strings.HasSuffix(name, ".go") {
			if src, err = format.Source(src); err != nil {
				return fmt.Errorf("%s: %w\n%s", name, err, buf.String())
			}
		}
		if err := writeNew(filepath.Join(target, name), src, stdout); err != nil {
			return err
		}
	}
	if err := writeNew(filepath.Join(target, "go.mod"), goModFile(spec.Module, gessoVersion()), stdout); err != nil {
		return err
	}
	if *tmpl == "demo" {
		if err := addAuth(nil, target, stdout); err != nil {
			return err
		}
		if err := addModule([]string{"notes", "-guard", "signedIn", "-field", "title:string:max=200", "-field", "body:text", "-field", "published:date:optional"}, target, stdout, now); err != nil {
			return err
		}
	}
	if *ai {
		if err := aiInit(nil, target, stdout); err != nil {
			return err
		}
	}
	rel, _ := filepath.Rel(dir, target)
	fmt.Fprintf(stdout, "\nnext:\n  cd %s\n  go mod tidy\n", rel)
	if *tmpl == "demo" {
		fmt.Fprintln(stdout, "  go run . users add <username>")
	} else {
		fmt.Fprintln(stdout, "  gesso add auth\n  gesso add module <name> -field <field>:<type>   (gesso help add module)")
	}
	fmt.Fprintln(stdout, "  gesso dev")
	return nil
}

// goModFile is a new project's go.mod: it requires gesso at version, so that
// go mod tidy does not pick whatever the module proxy last listed as latest —
// which, for a while after a release, is the one before it.
func goModFile(mod, version string) []byte {
	s := "module " + mod + "\n\ngo 1.26.10\n"
	if version != "" {
		s += "\nrequire github.com/Elagoht/gesso " + version + "\n"
	}
	return []byte(s)
}

// gessoVersion is the gesso release this CLI belongs to: the two are tagged
// together, so the CLI installed as cmd/gesso@v0.9.1 scaffolds for gesso v0.9.1.
// A build from a checkout has no release, and it is "".
func gessoVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	v := bi.Main.Version
	if !semver.IsValid(v) || module.IsPseudoVersion(v) || semver.Build(v) != "" {
		return ""
	}
	return v
}

// versionLine is what gesso version prints: the release, or for a build from a
// checkout its commit, and the Go it was built with.
func versionLine() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "gesso (unknown)"
	}
	v := bi.Main.Version
	if !semver.IsValid(v) || module.IsPseudoVersion(v) {
		v = "devel"
		for _, st := range bi.Settings {
			if st.Key == "vcs.revision" && len(st.Value) >= 7 {
				v += " " + st.Value[:7]
			}
		}
	}
	return "gesso " + v + " " + bi.GoVersion
}

func writeNew(p string, data []byte, stdout io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "created", p)
	return nil
}

func newProjectSpec(module, dialect string) (projectSpec, error) {
	name := path.Base(module)
	s := projectSpec{Module: module, Name: name, Dialect: dialect}
	switch dialect {
	case "sqlite":
		s.DialectGo, s.DialectName = "SQLite", "SQLite"
		s.DSNDoc = "the SQLite file"
		s.DefaultDSN = name + ".db"
	case "postgres":
		s.DialectGo, s.DialectName = "Postgres", "PostgreSQL"
		s.DSNDoc = "the PostgreSQL URL"
		s.DefaultDSN = "postgres://localhost:5432/" + name + "?sslmode=disable"
	case "mysql":
		s.DialectGo, s.DialectName = "MySQL", "MySQL"
		s.DSNDoc = "the MySQL DSN"
		s.DefaultDSN = "root@tcp(127.0.0.1:3306)/" + name
	default:
		return s, fmt.Errorf("unknown dialect %q: sqlite, postgres or mysql", dialect)
	}
	return s, nil
}

// initProject is gesso init: the API of a gesso new project, internal/api, in a
// module that exists already — a collage site that serves its API in the same
// process. No main: the module has one.
func initProject(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("init")
	dialect := fs.String("dialect", "sqlite", "the database: sqlite, postgres or mysql")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usage("init takes no arguments: run it in the module's root")
	}
	module, err := readModule(dir)
	if err != nil {
		return err
	}
	spec, err := newProjectSpec(module, *dialect)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "api")); err == nil {
		return errors.New("internal/api exists already")
	}
	for name, tmpl := range map[string]string{"internal/api/api.go": "api.go.tmpl", "internal/api/register.go": "register.go.tmpl", "internal/api/api_test.go": "api_test.go.tmpl"} {
		var buf bytes.Buffer
		if err := newTemplates.ExecuteTemplate(&buf, tmpl, spec); err != nil {
			return err
		}
		src, err := format.Source(buf.Bytes())
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := writeNew(filepath.Join(dir, name), src, stdout); err != nil {
			return err
		}
	}
	if v := gessoVersion(); v != "" {
		fmt.Fprintf(stdout, "\nnext: go get github.com/Elagoht/gesso@%s\n", v)
	}
	fmt.Fprintf(stdout, `
next: build the API where the program starts, and serve it. In a collage site,
in newApp, before collage.New:

	gessoAPI, err := api.NewWith(api.Config{
		BasePath: "/api",
		Database: cmp.Or(os.Getenv("DATABASE_URL"), %q), // %s
		Logger:   slog.Default(),
	})

then, after it, app.Handle("/api/", gessoAPI), and run its lifecycle with
collage plugin add elagoht/gesso (pass gessoAPI to plugins). Pages call it with
gesso.NewLocal(gessoAPI). Modules: gesso add module <name> -field <field>:<type>
`, spec.DefaultDSN, spec.DSNDoc)
	return nil
}
