package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	gotoken "go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
)

var endpointTemplate = template.Must(template.New("").Parse(`package {{.Pkg}}

import (
	"context"
{{- if or (eq .In "gesso.Empty") (eq .Out "gesso.Empty")}}

	"github.com/Elagoht/gesso"
{{- end}}
{{- if .HasTypes}}

	"{{.Models}}"
{{- end}}
)

func (m *Module) {{.Handler}}(ctx context.Context, in {{.In}}) ({{.Out}}, error) {
	return {{.Out}}{}, nil
}
`))

type endpointSpec struct {
	Pkg, Models  string
	Go, Handler  string // the Endpoint and its handler
	Method, Path string
	In, Out      string // models.Req…, gesso.Empty
	HasTypes     bool
	reqFields    []string // Go field lines
	resFields    []string
}

var pathParam = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)(\.\.\.)?\}`)

var endpointTypes = map[string]string{
	"string": "string", "text": "string", "int": "int64", "bool": "bool", "float": "float64",
	"time": "time.Time", "date": "date.Date", "strings": "[]string", "ints": "[]int64",
}

// addEndpoint is gesso add endpoint <module>/<name>: the handler in
// <module>/<name>.go, its Req and Res in the module's models, and its route —
// gesso.Post(r, "/docs/x", m.x) — in the module's routes: its own Routes, or,
// in a module gesso gen writes, routes in routes.go, which gen calls.
func addEndpoint(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("add endpoint")
	method := fs.String("method", "POST", "GET, POST, PUT, PATCH or DELETE")
	pattern := fs.String("path", "", `the URL pattern (default "/<module>/<name>"); a {param} in it is a field of the Req`)
	guard := fs.String("guard", "", "a guard from internal/guards")
	parent := fs.String("dir", "internal", "the folder the module is in")
	var req, res fieldFlags
	fs.Var(&req, "field", "a field of the Req, name:type[:rules] — types: string, text, int, bool, float, time, date, strings, ints; again for each")
	fs.Var(&res, "res", "a field of the Res, name:type; without any the endpoint answers nothing (204)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usage("add endpoint takes <module>/<name>")
	}
	modName, name, ok := strings.Cut(pos[0], "/")
	if !ok || !identifier.MatchString(modName) || !identifier.MatchString(name) {
		return usage("add endpoint takes <module>/<name>, lower case")
	}
	*method = strings.ToUpper(*method)
	if !map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}[*method] {
		return usage("unknown -method " + *method + ": GET, POST, PUT, PATCH or DELETE")
	}
	pkg := strings.ReplaceAll(modName, "_", "")
	modDir := filepath.Join(dir, *parent, pkg)
	moduleGo, routesFn := filepath.Join(modDir, "module.go"), "Routes"
	generated := false
	if _, err := os.Stat(filepath.Join(modDir, "module.gen.go")); err == nil {
		moduleGo, routesFn, generated = filepath.Join(modDir, "routes.go"), "routes", true
		if _, err := os.Stat(moduleGo); err != nil {
			if err := os.WriteFile(moduleGo, []byte(routesFile(pkg)), 0o644); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "created", moduleGo)
		}
	}
	modSrc, err := os.ReadFile(moduleGo)
	if err != nil {
		return fmt.Errorf("no module %s: gesso add module %s first", moduleGo, modName)
	}
	mod, err := readModule(dir)
	if err != nil {
		return err
	}
	s := endpointSpec{Pkg: pkg, Models: mod + "/" + filepath.ToSlash(filepath.Join(*parent, pkg, "models")),
		Go: camel(name) + camel(modName), Method: *method, Path: *pattern}
	s.Handler = strings.ToLower(s.Go[:1]) + s.Go[1:]
	if s.Path == "" {
		s.Path = "/" + modName + "/" + strings.ReplaceAll(name, "_", "-")
	}
	if !strings.HasPrefix(s.Path, "/") {
		return usage("-path begins with /")
	}
	target := filepath.Join(modDir, name+".go")
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}
	for _, m := range pathParam.FindAllStringSubmatch(s.Path, -1) {
		s.reqFields = append(s.reqFields, fmt.Sprintf("%s string `path:%q`", camel(m[1]), m[1]))
	}
	for _, f := range req {
		line, err := s.field(f, true)
		if err != nil {
			return err
		}
		s.reqFields = append(s.reqFields, line)
	}
	for _, f := range res {
		line, err := s.field(f, false)
		if err != nil {
			return err
		}
		s.resFields = append(s.resFields, line)
	}
	s.In, s.Out = "gesso.Empty", "gesso.Empty"
	if len(s.reqFields) > 0 {
		s.In, s.HasTypes = "models.Req"+s.Go, true
	}
	if len(s.resFields) > 0 {
		s.Out, s.HasTypes = "models.Res"+s.Go, true
	}
	var guardName, guardsImport string
	if *guard != "" {
		if guardName, err = guardFor(dir, *parent, *guard); err != nil {
			return err
		}
		guardsImport = mod + "/" + filepath.ToSlash(filepath.Join(*parent, "guards"))
	}
	wired, err := addRoute(modSrc, routesFn, s, guardName, guardsImport)
	if err != nil {
		return fmt.Errorf("%s: %w; add gesso.%s(r, %q, m.%s) to %s by hand", moduleGo, err, methodFunc(s.Method), s.Path, s.Handler, routesFn)
	}
	var buf bytes.Buffer
	if err := endpointTemplate.Execute(&buf, s); err != nil {
		return err
	}
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("%w\n%s", err, buf.String())
	}
	if len(s.reqFields) > 0 {
		if err := appendType(filepath.Join(modDir, "models", "requests.go"), "Req"+s.Go, s.reqFields, stdout); err != nil {
			return err
		}
	}
	if len(s.resFields) > 0 {
		if err := appendType(filepath.Join(modDir, "models", "responses.go"), "Res"+s.Go, s.resFields, stdout); err != nil {
			return err
		}
	}
	if err := os.WriteFile(target, src, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "created", target)
	if err := os.WriteFile(moduleGo, wired, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wired", moduleGo)
	if generated {
		// module.gen.go calls routes once gen has seen it.
		paths, err := genModel(filepath.Join(modDir, "models"), genModelOptions{root: dir, dialect: projectDialect(dir), now: time.Now})
		for _, p := range paths {
			fmt.Fprintln(stdout, "generated", p)
		}
		return err
	}
	return nil
}

func routesFile(pkg string) string {
	return "package " + pkg + `

import (
	"github.com/Elagoht/gesso"
)

// routes are the module's own, beside those gesso gen writes.
func (m *Module) routes(r *gesso.Router) {}
`
}

// methodFunc is gesso's function for method: Get, Post, Put, Patch, Delete.
func methodFunc(method string) string { return method[:1] + strings.ToLower(method[1:]) }

// field is one -field or -res as a Go struct field line.
func (s *endpointSpec) field(spec string, isReq bool) (string, error) {
	col, rest, ok := strings.Cut(spec, ":")
	typ, rules, _ := strings.Cut(rest, ":")
	goType, known := endpointTypes[typ]
	if !ok || !identifier.MatchString(col) || !known {
		return "", usage(fmt.Sprintf("a field is name:type[:rules], types string, text, int, bool, float, time, date, strings, ints; got %q", spec))
	}
	src := "json"
	if !isReq || s.Method == "GET" || s.Method == "DELETE" {
		// A GET and a DELETE carry no body: their fields are the query's.
		src = map[bool]string{true: "query", false: "json"}[isReq]
	}
	line := fmt.Sprintf("%s %s `%s:%q", camel(col), goType, src, lowerCamel(col))
	if isReq {
		var vs []string
		optional := false
		for r := range strings.SplitSeq(rules, ",") {
			switch r = strings.TrimSpace(r); {
			case r == "":
			case r == "optional":
				optional = true
			default:
				if name, _, _ := strings.Cut(r, "="); !ruleNames[name] {
					return "", usage(fmt.Sprintf("field %s: unknown rule %q", col, r))
				}
				vs = append(vs, r)
			}
		}
		if !optional && typ != "int" && typ != "bool" && typ != "float" {
			vs = append([]string{"required"}, vs...)
		}
		if len(vs) > 0 {
			line += fmt.Sprintf(" validate:%q", strings.Join(vs, ","))
		}
	}
	return line + "`", nil
}

// addRoute adds the endpoint's route to the end of the module's method fn.
func addRoute(src []byte, fn string, s endpointSpec, guard, guardsImport string) ([]byte, error) {
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "module.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var routes *ast.FuncDecl
	for _, d := range f.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Recv != nil && f.Name.Name == fn && f.Body != nil {
			routes = f
		}
	}
	if routes == nil || len(routes.Type.Params.List) != 1 || len(routes.Type.Params.List[0].Names) != 1 {
		return nil, fmt.Errorf("no %s(r *gesso.Router) method", fn)
	}
	r := routes.Type.Params.List[0].Names[0].Name
	if r == "_" {
		return nil, fmt.Errorf("the %s method names its router _", fn)
	}
	recv := ""
	if names := routes.Recv.List[0].Names; len(names) == 1 {
		recv = names[0].Name
	}
	if recv == "" || recv == "_" {
		return nil, fmt.Errorf("the %s method has no receiver name", fn)
	}
	line := fmt.Sprintf("gesso.%s(%s, %q, %s.%s", methodFunc(s.Method), r, s.Path, recv, s.Handler)
	if guard != "" {
		line += ", gesso.Guards(guards." + guard + ")"
	}
	line += ")\n"
	end := fset.Position(routes.Body.Rbrace).Offset
	imports := []string{"github.com/Elagoht/gesso"}
	if guardsImport != "" {
		imports = append(imports, guardsImport)
	}
	return reformat(fset, applyInserts(src, []insert{{at: end, text: line}}), imports...)
}

// appendType adds a struct to a models file, making the file if need be.
func appendType(p, name string, fields []string, stdout io.Writer) error {
	src, err := os.ReadFile(p)
	if err != nil {
		src = []byte("package models\n")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
	}
	if regexp.MustCompile(`(?m)^type ` + name + `\b`).Match(src) {
		return fmt.Errorf("%s has a %s already", p, name)
	}
	src = append(src, []byte("\ntype "+name+" struct {\n\t"+strings.Join(fields, "\n\t")+"\n}\n")...)
	var imports []string
	joined := strings.Join(fields, "\n")
	if strings.Contains(joined, "time.Time") {
		imports = append(imports, "time")
	}
	if strings.Contains(joined, "date.Date") {
		imports = append(imports, "github.com/Elagoht/gesso/date")
	}
	out, err := reformat(gotoken.NewFileSet(), src, imports...)
	if err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wrote", p)
	return nil
}
