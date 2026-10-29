package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

var dialectVar = regexp.MustCompile(`(?m)^var dialect = migrate\.(Postgres|MySQL|SQLite)\b`)

// appDialect is the dialect a gesso new app declares, or "".
func appDialect(src []byte) string {
	m := dialectVar.FindSubmatch(src)
	if m == nil {
		return ""
	}
	return map[string]string{"Postgres": "postgres", "MySQL": "mysql", "SQLite": "sqlite"}[string(m[1])]
}

// readModule is the module path in dir's go.mod.
func readModule(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", errors.New("no go.mod: run gesso from the project's root")
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`), nil
		}
	}
	return "", errors.New("go.mod has no module line")
}

// wireModule adds a module to an api.go: pkg.New(pkg.Deps{DB: d.DB}) to what
// modules returns, pkg.Migrations to what migrations returns, and the import.
// Each list is found by its function's name, and the text is inserted before
// the list's closing brace, so the rest of the file is left as it is.
func wireModule(src []byte, pkg, importPath string, withMigrations bool) ([]byte, error) {
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "api.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == importPath {
			return nil, fmt.Errorf("%s is wired already", pkg)
		}
	}
	entries := map[string]string{"modules": pkg + ".New(" + pkg + ".Deps{DB: d.DB})"}
	if withMigrations {
		entries["migrations"] = pkg + ".Migrations"
	}
	var inserts []insert
	for fn, entry := range entries {
		lit := returnedList(f, fn)
		if lit == nil {
			return nil, fmt.Errorf("no func %s returning a list literal", fn)
		}
		inserts = append(inserts, listInsert(fset, lit, entry))
	}
	if len(inserts) == 2 && inserts[0].at < inserts[1].at {
		inserts[0], inserts[1] = inserts[1], inserts[0]
	}
	out := src
	for _, in := range inserts { // the later offset first, so the earlier stays right
		out = append(out[:in.at:in.at], append([]byte(in.text), out[in.at:]...)...)
	}
	f, err = parser.ParseFile(fset, "api.go", out, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	astutil.AddImport(fset, f, importPath)
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return format.Source(buf.Bytes())
}

// returnedList is the composite literal func name returns, or nil.
func returnedList(f *ast.File, name string) *ast.CompositeLit {
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != name || fn.Body == nil {
			continue
		}
		for _, st := range fn.Body.List {
			if ret, ok := st.(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
				if lit, ok := ret.Results[0].(*ast.CompositeLit); ok {
					return lit
				}
			}
		}
	}
	return nil
}
