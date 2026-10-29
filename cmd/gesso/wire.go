package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var dialectImport = regexp.MustCompile(`"github\.com/Elagoht/gesso/(sqlite|postgres|mysql)"`)

// projectDialect is the database the app in dir opens — the gesso/sqlite,
// gesso/postgres or gesso/mysql its main.go (gesso new) or internal/api/api.go
// (gesso init) imports — or "".
func projectDialect(dir string) string {
	for _, p := range []string{"main.go", filepath.Join("internal", "api", "api.go")} {
		if src, err := os.ReadFile(filepath.Join(dir, p)); err == nil {
			if m := dialectImport.FindSubmatch(src); m != nil {
				return string(m[1])
			}
		}
	}
	return ""
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

// registerFile is the app's register.go: the project root's (gesso new), or
// internal/api's (gesso init).
func registerFile(dir string) (string, []byte, error) {
	for _, p := range []string{filepath.Join(dir, "register.go"), filepath.Join(dir, "internal", "api", "register.go")} {
		if src, err := os.ReadFile(p); err == nil {
			return p, src, nil
		}
	}
	return "", nil, errors.New("no register.go: this adds to an app gesso new or gesso init made")
}

// registerFunc is func register(app *gesso.App) error, the name its app goes
// by, and the app.Register call it returns.
func registerFunc(f *ast.File) (*ast.FuncDecl, string, *ast.CallExpr, error) {
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "register" || fn.Body == nil {
			continue
		}
		if len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) != 1 {
			break
		}
		app := fn.Type.Params.List[0].Names[0].Name
		if n := len(fn.Body.List); n > 0 {
			if ret, ok := fn.Body.List[n-1].(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
				if call, ok := ret.Results[0].(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && isName(sel.X, app) && sel.Sel.Name == "Register" {
						return fn, app, call, nil
					}
				}
			}
		}
		return nil, "", nil, fmt.Errorf("func register does not end in return %s.Register(…)", app)
	}
	return nil, "", nil, errors.New("no func register(app *gesso.App) error")
}

// wireRegister adds entry — docs.New() — to what register passes to
// app.Register, and its import.
func wireRegister(src []byte, entry, importPath string) ([]byte, error) {
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "register.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	_, _, call, err := registerFunc(f)
	if err != nil {
		return nil, err
	}
	for _, a := range call.Args {
		if string(src[fset.Position(a.Pos()).Offset:fset.Position(a.End()).Offset]) == entry {
			return nil, fmt.Errorf("%s is registered already", entry)
		}
	}
	at := fset.Position(call.Rparen).Offset
	text := "\n" + entry + ",\n"
	if n := len(call.Args); n > 0 {
		if fset.Position(call.Args[n-1].End()).Line == fset.Position(call.Rparen).Line {
			text = ",\n" + entry + ",\n"
		} else {
			text = entry + ",\n"
		}
	}
	return reformat(fset, applyInserts(src, []insert{{at: at, text: text}}), importPath)
}

// wireStatement adds stmt — app.Command("seed", commands.Seed(app)) — to
// register before it registers the modules, and its import. A word in stmt
// naming the app is written as register names it.
func wireStatement(src []byte, stmt func(app string) string, importPath string) ([]byte, error) {
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "register.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	fn, app, _, err := registerFunc(f)
	if err != nil {
		return nil, err
	}
	ret := fn.Body.List[len(fn.Body.List)-1]
	line := stmt(app)
	if strings.Contains(string(src), line) {
		return nil, fmt.Errorf("%s is there already", line)
	}
	return reformat(fset, applyInserts(src, []insert{{at: fset.Position(ret.Pos()).Offset, text: line + "\n"}}), importPath)
}
