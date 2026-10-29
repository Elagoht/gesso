package main

import (
	"bytes"
	"embed"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
	"unicode"
)

//go:embed scaffold/*.tmpl scaffold/new/*.tmpl
var scaffoldFS embed.FS

var scaffold = template.Must(template.New("").Funcs(template.FuncMap{
	"ph": func(dialect string, n int) string {
		if dialect == "postgres" {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	},
	"add": func(a, b int) int { return a + b },
	"mul": func(a, b int) int { return a * b },
	// lit is SQL written inside a Go string literal: a quoted identifier's
	// double quotes escaped.
	"lit": func(sql string) string { return strings.ReplaceAll(sql, `"`, `\"`) },
}).ParseFS(scaffoldFS, "scaffold/*.tmpl"))

// field is one column of the scaffolded entity.
type field struct {
	Go     string // Title
	JSON   string // title
	Column string // title
	Ident  string // the column as SQL names it: title, or "order", quoted where it is a keyword
	Type   string // string
	Rule   string // required,max=255
	Patch  string // notnull,max=255: the rules of a PATCH, where the field may be left out
	SQL    string // TEXT NOT NULL
}

type moduleSpec struct {
	Pkg      string // events
	Entity   string // Event
	Plural   string // Events
	Table    string // events
	Dialect  string
	Fields   []field
	UsesTime bool
	UsesDate bool
	IDSQL    string
	Models   string // the import path of the module's models package
	Guard    string // Admin: the guard on the writes, from Guards
	Guards   string // the import path of internal/guards
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func addModule(args []string, dir string, stdout io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("add module", flag.ContinueOnError)
	fields := fs.String("fields", "name:string", "the entity's fields, name:type, comma-separated; types: string, text, int, bool, float, time, date")
	dialect := fs.String("dialect", "postgres", "the SQL the repository and migration are written in: postgres, sqlite or mysql (default: the app's, in a gesso new project)")
	parent := fs.String("dir", "internal", "the folder the module's folder goes in")
	entity := fs.String("entity", "", "the entity's Go name (default: the module name, singular, capitalised)")
	guard := fs.String("guard", "", "a guard from internal/guards on create, update, patch and delete (gesso add guard <name> makes one)")
	empty := fs.Bool("empty", false, "only the module's layout — module.go, endpoints.go, models/ — for endpoints that are not CRUD")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		if *empty && f.Name == "fields" {
			err = errors.New("-empty takes no -fields: the module has no table")
		}
	})
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("add module takes one name: gesso add module events")
	}
	app, appErr := os.ReadFile(filepath.Join(dir, *parent, "api", "api.go"))
	regGo := filepath.Join(dir, *parent, "api", "register.go")
	reg, regErr := os.ReadFile(regGo)
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == "dialect" })
	if appErr == nil && !given {
		if d := appDialect(app); d != "" {
			*dialect = d
		}
	}
	spec, err := newSpec(pos[0], *entity, *fields, *dialect)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, *parent, spec.Pkg)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}
	mod, err := readModule(dir)
	if err != nil {
		return err
	}
	spec.Models = mod + "/" + filepath.ToSlash(filepath.Join(*parent, spec.Pkg, "models"))
	if *guard != "" {
		if spec.Guard, err = guardFor(dir, *parent, *guard); err != nil {
			return err
		}
		spec.Guards = mod + "/" + filepath.ToSlash(filepath.Join(*parent, "guards"))
	}
	if err := os.MkdirAll(filepath.Join(target, "models"), 0o755); err != nil {
		return err
	}
	files := []struct{ name, tmpl string }{
		{"module.go", "module.go.tmpl"},
		{"endpoints.go", "endpoints.go.tmpl"},
		{"handler.go", "handler.go.tmpl"},
		{"service.go", "service.go.tmpl"},
		{"repository.go", "repository.go.tmpl"},
		{"service_test.go", "service_test.go.tmpl"},
		{"models/models.go", "models_models.go.tmpl"},
		{"models/requests.go", "models_requests.go.tmpl"},
		{"models/responses.go", "models_responses.go.tmpl"},
	}
	if *empty {
		files = []struct{ name, tmpl string }{
			{"module.go", "empty_module.go.tmpl"},
			{"endpoints.go", "empty_endpoints.go.tmpl"},
			{"models/requests.go", "empty_requests.go.tmpl"},
			{"models/responses.go", "empty_responses.go.tmpl"},
		}
	}
	for _, f := range files {
		var buf bytes.Buffer
		if err := scaffold.ExecuteTemplate(&buf, f.tmpl, spec); err != nil {
			return err
		}
		src, err := format.Source(buf.Bytes())
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", f.name, err, buf.String())
		}
		p := filepath.Join(target, filepath.FromSlash(f.name))
		if err := os.WriteFile(p, src, 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "created", p)
	}
	if *empty {
		return wire(reg, regErr, regGo, spec.Pkg, mod+"/"+filepath.ToSlash(filepath.Join(*parent, spec.Pkg)), false, stdout)
	}
	var up, down bytes.Buffer
	if err := scaffold.ExecuteTemplate(&up, "up.sql.tmpl", spec); err != nil {
		return err
	}
	if err := scaffold.ExecuteTemplate(&down, "down.sql.tmpl", spec); err != nil {
		return err
	}
	paths, err := writeMigration(dir, filepath.Join(target, "migrations"), "create_"+spec.Table, up.String(), down.String(), now)
	for _, p := range paths {
		fmt.Fprintln(stdout, "created", p)
	}
	if err != nil {
		return err
	}
	if spec.Guard == "" {
		fmt.Fprintf(stdout, "note: create, update and delete in %s are open to anyone: -guard <name> guards them\n", spec.Pkg)
	}
	return wire(reg, regErr, regGo, spec.Pkg, mod+"/"+filepath.ToSlash(filepath.Join(*parent, spec.Pkg)), true, stdout)
}

// wire adds the module to the app's register.go, when there is one.
func wire(app []byte, appErr error, appGo, pkg, importPath string, withMigrations bool, stdout io.Writer) error {
	if appErr != nil {
		return nil // no gesso new app here: wiring the module is the user's
	}
	wired, err := wireModule(app, pkg, importPath, withMigrations)
	if err != nil {
		if withMigrations {
			return fmt.Errorf("%s: %w; add %s.New and %s.Migrations to it by hand", appGo, err, pkg, pkg)
		}
		return fmt.Errorf("%s: %w; add %s.New to it by hand", appGo, err, pkg)
	}
	if err := os.WriteFile(appGo, wired, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wired", appGo)
	return nil
}

func newSpec(name, entity, fieldList, dialect string) (moduleSpec, error) {
	if !identifier.MatchString(name) {
		return moduleSpec{}, fmt.Errorf("a module name is lower case letters, digits and underscores: %q", name)
	}
	if dialect != "postgres" && dialect != "sqlite" && dialect != "mysql" {
		return moduleSpec{}, fmt.Errorf("unknown dialect %q: postgres, sqlite or mysql", dialect)
	}
	if entity == "" {
		entity = camel(strings.TrimSuffix(name, "s"))
	}
	spec := moduleSpec{
		Pkg: strings.ReplaceAll(name, "_", ""), Entity: entity, Plural: camel(name), Table: name, Dialect: dialect,
		IDSQL: map[string]string{
			"postgres": "id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY",
			"sqlite":   "id INTEGER PRIMARY KEY",
			"mysql":    "id BIGINT AUTO_INCREMENT PRIMARY KEY",
		}[dialect],
	}
	if spec.Plural == spec.Entity {
		spec.Plural += "List"
	}
	seen := map[string]bool{"id": true}
	for part := range strings.SplitSeq(fieldList, ",") {
		col, typ, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok || !identifier.MatchString(col) {
			return moduleSpec{}, fmt.Errorf("a field is name:type, got %q", part)
		}
		if seen[col] {
			return moduleSpec{}, fmt.Errorf("field %q given twice (id is always there)", col)
		}
		seen[col] = true
		f, err := newField(col, typ, dialect)
		if err != nil {
			return moduleSpec{}, err
		}
		spec.UsesTime = spec.UsesTime || f.Type == "time.Time"
		spec.UsesDate = spec.UsesDate || f.Type == "date.Date"
		spec.Fields = append(spec.Fields, f)
	}
	return spec, nil
}

func newField(col, typ, dialect string) (field, error) {
	f := field{Go: camel(col), JSON: lowerCamel(col), Column: col, Ident: quoteIdent(col, dialect)}
	sqlTypes := map[string]map[string]string{
		"string": {"postgres": "TEXT", "sqlite": "TEXT", "mysql": "VARCHAR(255)"},
		"text":   {"postgres": "TEXT", "sqlite": "TEXT", "mysql": "TEXT"},
		"date":   {"postgres": "DATE", "sqlite": "DATE", "mysql": "DATE"},
		"int":    {"postgres": "BIGINT", "sqlite": "INTEGER", "mysql": "BIGINT"},
		"bool":   {"postgres": "BOOLEAN", "sqlite": "BOOLEAN", "mysql": "BOOLEAN"},
		"float":  {"postgres": "DOUBLE PRECISION", "sqlite": "REAL", "mysql": "DOUBLE"},
		"time":   {"postgres": "TIMESTAMPTZ", "sqlite": "TIMESTAMP", "mysql": "DATETIME(6)"},
	}
	types, ok := sqlTypes[typ]
	if !ok {
		return f, fmt.Errorf("field %q: unknown type %q (string, text, int, bool, float, time, date)", col, typ)
	}
	f.SQL = types[dialect] + " NOT NULL"
	switch typ {
	case "string":
		f.Type, f.Rule = "string", "required,max=255"
	case "text":
		f.Type, f.Rule = "string", "required"
	case "date":
		f.Type, f.Rule = "date.Date", "required"
	case "int":
		f.Type = "int64"
	case "bool":
		f.Type = "bool"
	case "float":
		f.Type = "float64"
	case "time":
		f.Type, f.Rule = "time.Time", "required"
	}
	// A PATCH may leave a field out, but not clear it: the column is NOT NULL.
	f.Patch = strings.TrimSuffix("notnull,"+strings.TrimPrefix(strings.TrimPrefix(f.Rule, "required"), ","), ",")
	return f, nil
}

// keywords are words a column cannot be named in one of the dialects without
// quoting: SQL's reserved words, PostgreSQL's and MySQL's.
var keywords = map[string]bool{}

func init() {
	for w := range strings.FieldsSeq(`all alter analyse analyze and any array as asc asymmetric authorization
		between binary both by call case cast change check collate column condition constraint
		create cross current_catalog current_date current_role current_time current_timestamp
		current_user database databases default deferrable delete desc describe distinct do drop
		else end except exists explain false fetch for foreign freeze from full grant group having
		ilike in index initially inner insert intersect interval into is isnull join key keys
		lateral leading left like limit localtime localtimestamp match natural not notnull null
		offset on only or order outer overlaps placing primary range rank read references
		regexp rename replace returning right row rows select session_user set show similar some
		symmetric table tablesample then to trailing trigger true union unique update usage
		user using values variadic verbose when where window with write`) {
		keywords[w] = true
	}
}

// quoteIdent is col as the dialect's SQL must spell it: as it is, or, when it is
// a keyword, in double quotes (PostgreSQL, SQLite) or backquotes (MySQL).
func quoteIdent(col, dialect string) string {
	if !keywords[col] {
		return col
	}
	if dialect == "mysql" {
		return "`" + col + "`"
	}
	return `"` + col + `"`
}

// camel is snake_case in CamelCase, with Go's initialisms: user_id is UserID.
func camel(s string) string {
	var b strings.Builder
	for part := range strings.SplitSeq(s, "_") {
		switch up := strings.ToUpper(part); up {
		case "ID", "URL", "API", "HTTP", "JSON", "SQL", "UUID", "IP":
			b.WriteString(up)
		default:
			r := []rune(part)
			if len(r) > 0 {
				r[0] = unicode.ToUpper(r[0])
			}
			b.WriteString(string(r))
		}
	}
	return b.String()
}

// lowerCamel is snake_case in lowerCamelCase, for JSON: user_id is userId.
func lowerCamel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		r := []rune(parts[i])
		if len(r) > 0 {
			r[0] = unicode.ToUpper(r[0])
		}
		parts[i] = string(r)
	}
	return strings.Join(parts, "")
}
