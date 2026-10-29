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

//go:embed scaffold/*.tmpl scaffold/new/*.tmpl scaffold/gen/*.tmpl
var scaffoldFS embed.FS

var scaffold = template.Must(template.New("").ParseFS(scaffoldFS, "scaffold/*.tmpl"))

// moduleSpec is what the module's own files — written once, the user's after —
// are rendered from.
type moduleSpec struct {
	*model
	Fields []specField
	Guard  string // Admin: the guard on the writes
	Guards string // the import path of internal/guards
	Sample string // a ReqCreate literal's fields for the service test
}

type specField struct {
	mfield
	Tag string // its gesso tag in models.go
}

// fieldFlags collects -field, which may be given again and again.
type fieldFlags []string

func (f *fieldFlags) String() string     { return strings.Join(*f, " ") }
func (f *fieldFlags) Set(v string) error { *f = append(*f, v); return nil }

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func addModule(args []string, dir string, stdout io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("add module", flag.ContinueOnError)
	var fieldList fieldFlags
	fs.Var(&fieldList, "field", "a field, name:type[:options and rules]: slug:string:key,slug,max=100; again for each")
	short := fs.String("fields", "", "fields without options, comma-separated: title:string,body:text")
	dialect := fs.String("dialect", "postgres", "postgres, sqlite or mysql (default: the app's)")
	parent := fs.String("dir", "internal", "the folder the module's folder goes in")
	entity := fs.String("entity", "", "the entity's Go name (default: the module name, singular, capitalised)")
	guard := fs.String("guard", "", "a guard from internal/guards on create, update, patch and delete")
	empty := fs.Bool("empty", false, "only module.go, endpoints.go and models/, for endpoints that are not CRUD")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("add module takes one name: gesso add module events")
	}
	if *short != "" {
		for part := range strings.SplitSeq(*short, ",") {
			fieldList = append(fieldList, strings.TrimSpace(part))
		}
	}
	if *empty && len(fieldList) > 0 {
		return errors.New("-empty takes no fields: the module has no table")
	}
	if !*empty && len(fieldList) == 0 {
		fieldList = fieldFlags{"name:string"}
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
	spec, err := newSpec(pos[0], *entity, fieldList, *dialect)
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
	files := []struct{ name, tmpl string }{
		{"module.go", "module.go.tmpl"},
		{"endpoints.go", "endpoints.go.tmpl"},
		{"handler.go", "handler.go.tmpl"},
		{"service.go", "service.go.tmpl"},
		{"service_test.go", "service_test.go.tmpl"},
		{"models/models.go", "models_models.go.tmpl"},
	}
	if *empty {
		files = []struct{ name, tmpl string }{
			{"module.go", "empty_module.go.tmpl"},
			{"endpoints.go", "empty_endpoints.go.tmpl"},
			{"models/requests.go", "empty_requests.go.tmpl"},
			{"models/responses.go", "empty_responses.go.tmpl"},
		}
	}
	if err := os.MkdirAll(filepath.Join(target, "models"), 0o755); err != nil {
		return err
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
	importPath := mod + "/" + filepath.ToSlash(filepath.Join(*parent, spec.Pkg))
	if *empty {
		return wire(reg, regErr, regGo, spec.Pkg, importPath, false, stdout)
	}
	paths, err := genModel(filepath.Join(target, "models"), genModelOptions{root: dir, dialect: *dialect, now: now})
	for _, p := range paths {
		fmt.Fprintln(stdout, "created", p)
	}
	if err != nil {
		return err
	}
	if spec.Guard == "" {
		fmt.Fprintf(stdout, "note: create, update and delete in %s are open to anyone: -guard <name> guards them\n", spec.Pkg)
	}
	return wire(reg, regErr, regGo, spec.Pkg, importPath, true, stdout)
}

// wire adds the module to the app's register.go, when there is one.
func wire(reg []byte, regErr error, regGo, pkg, importPath string, crud bool, stdout io.Writer) error {
	if regErr != nil {
		return nil // no gesso new app here: wiring the module is the user's
	}
	wired, err := wireModule(reg, pkg, importPath, crud)
	if err != nil {
		return fmt.Errorf("%s: %w; add %s.New to it by hand", regGo, err, pkg)
	}
	if err := os.WriteFile(regGo, wired, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wired", regGo)
	return nil
}

// newSpec parses the -field values: name:type[:options], type one of string,
// text, int, bool, float, time, date; an optional time or date is a pointer,
// for a column that takes NULL.
func newSpec(name, entity string, fields []string, dialect string) (moduleSpec, error) {
	if !identifier.MatchString(name) {
		return moduleSpec{}, fmt.Errorf("a module name is lower case letters, digits and underscores: %q", name)
	}
	if dialect != "postgres" && dialect != "sqlite" && dialect != "mysql" {
		return moduleSpec{}, fmt.Errorf("unknown dialect %q: postgres, sqlite or mysql", dialect)
	}
	if entity == "" {
		entity = camel(strings.TrimSuffix(name, "s"))
	}
	m := &model{Pkg: strings.ReplaceAll(name, "_", ""), Entity: entity, Table: name, Plural: camel(name), Dialect: dialect}
	if m.Plural == m.Entity {
		m.Plural += "List"
	}
	spec := moduleSpec{model: m}
	seen := map[string]bool{"id": true}
	for _, part := range fields {
		col, rest, ok := strings.Cut(part, ":")
		typ, opts, _ := strings.Cut(rest, ":")
		if !ok || !identifier.MatchString(col) {
			return moduleSpec{}, fmt.Errorf("a field is name:type[:options], got %q", part)
		}
		if seen[col] {
			return moduleSpec{}, fmt.Errorf("field %q given twice (id is always there)", col)
		}
		seen[col] = true
		goType, ok := map[string]string{"string": "string", "text": "string", "int": "int64", "bool": "bool", "float": "float64", "time": "time.Time", "date": "date.Date"}[typ]
		if !ok {
			return moduleSpec{}, fmt.Errorf("field %q: unknown type %q (string, text, int, bool, float, time, date)", col, typ)
		}
		tag := opts
		if typ == "text" {
			tag = strings.TrimSuffix("text,"+opts, ",")
		}
		if (typ == "time" || typ == "date") && strings.Contains(","+opts+",", ",optional,") {
			goType = "*" + goType
		}
		f, err := newMField(camel(col), goType, tag)
		if err != nil {
			return moduleSpec{}, fmt.Errorf("field %q: %w", col, err)
		}
		f.Ident = quoteIdent(f.Column, dialect)
		spec.Fields = append(spec.Fields, specField{f, tag})
		m.Fields = append(m.Fields, f)
	}
	for i := range m.Fields {
		f := &m.Fields[i]
		if f.Key {
			if m.Key != nil {
				return moduleSpec{}, errors.New("two key fields")
			}
			m.Key = f
			spec.Sample = f.Go + ": " + map[string]string{"string": `"a"`, "int": "1"}[f.Base]
		}
		if f.Sort && m.Sort != nil {
			return moduleSpec{}, errors.New("two sort fields")
		}
		if f.Sort {
			m.Sort = f
		}
		m.UsesTime = m.UsesTime || f.Base == "time"
		m.UsesDate = m.UsesDate || f.Base == "date"
	}
	return spec, nil
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
