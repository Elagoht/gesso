package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	gotoken "go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"golang.org/x/tools/go/ast/astutil"
)

// addAuth is gesso add auth: sign-in from gesso/auth — a table of users, its
// users command — wired into register.go, and a signedIn guard. None of auth's
// code is copied in, so a fix to it arrives with go get.
func addAuth(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("add auth")
	parent := fs.String("dir", "internal", "the folder api/ is in")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usage("add auth takes no arguments")
	}
	regGo := filepath.Join(dir, *parent, "api", "register.go")
	src, err := os.ReadFile(regGo)
	if err != nil {
		return fmt.Errorf("no %s: gesso add auth wires an app gesso new or gesso init made", regGo)
	}
	wired, err := wireAuth(src)
	if err != nil {
		return fmt.Errorf("%s: %w", regGo, err)
	}
	if err := os.WriteFile(regGo, wired, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wired", regGo)
	guard := filepath.Join(dir, *parent, "guards", "signedIn.go")
	if _, err := os.Stat(guard); err != nil {
		if err := os.MkdirAll(filepath.Dir(guard), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(guard, []byte(signedInGuard), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "created", guard)
	}
	fmt.Fprintln(stdout, "\nnext:")
	if v := gessoVersion(); v != "" {
		fmt.Fprintf(stdout, "  go get github.com/Elagoht/gesso@%s\n", v)
	}
	fmt.Fprint(stdout, `  go mod tidy
  go run . users add <username> -role admin
  gesso add module <name> -guard signedIn -field <field>:<type>
`)
	return nil
}

const signedInGuard = `package guards

import (
	"net/http"

	"github.com/Elagoht/gesso"
)

func SignedIn(r *http.Request) error {
	return gesso.RequireIdentity(r)
}
`

// insert puts text at at, in place of the cut bytes there.
type insert struct {
	at, cut int
	text    string
}

// wireAuth adds sign-in to a register.go as gesso new writes it, each piece
// found by its function's name; anything elsewhere is refused, not guessed at.
func wireAuth(src []byte) ([]byte, error) {
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "register.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "github.com/Elagoht/gesso/auth" {
			return nil, errors.New("auth is wired already")
		}
	}
	off := func(p gotoken.Pos) int { return fset.Position(p).Offset }
	var inserts []insert

	fields := structFields(f, "Deps")
	if fields == nil {
		return nil, errors.New("no type Deps struct")
	}
	inserts = append(inserts, insert{at: off(fields.Closing), text: "Auth  *auth.Module\nUsers *auth.SQL\n"})

	nd := funcDecl(f, "newDeps")
	ret := lastReturn(nd)
	var deps *ast.CompositeLit
	if ret != nil && len(ret.Results) == 2 {
		deps, _ = ret.Results[0].(*ast.CompositeLit)
	}
	if deps == nil || !isName(deps.Type, "Deps") || !names(nd.Type.Params, "d") {
		return nil, errors.New("newDeps(d *sql.DB) does not end in return Deps{…}, nil")
	}
	inserts = append(inserts,
		insert{at: off(ret.Pos()), text: `users := auth.NewSQL(d, dialect)
	authn, err := auth.New(auth.Options{DB: d, Dialect: dialect, Users: users})
	if err != nil {
		return Deps{}, err
	}
	`},
		listInsert(fset, deps, "Auth: authn, Users: users"))

	an := funcDecl(f, "authenticator")
	if an == nil || len(an.Body.List) != 1 || !names(an.Type.Params, "d") {
		return nil, errors.New("authenticator(d Deps) is not the one-line return nil gesso new writes")
	}
	body := an.Body.List[0]
	if r, ok := body.(*ast.ReturnStmt); !ok || len(r.Results) != 1 || !isName(r.Results[0], "nil") {
		return nil, errors.New("authenticator returns an Authenticator already")
	}
	inserts = append(inserts, insert{at: off(body.Pos()), cut: off(body.End()) - off(body.Pos()), text: "return d.Auth"})

	for _, l := range []struct{ fn, entry string }{
		{"modules", "d.Auth"},
		{"stores", "auth.Migrate"},
		{"appCommands", `"users": d.Users.Command(os.Stdin)`},
	} {
		lit := returnedList(f, l.fn)
		if lit == nil {
			return nil, fmt.Errorf("no func %s returning a literal", l.fn)
		}
		inserts = append(inserts, listInsert(fset, lit, l.entry))
	}
	return reformat(fset, applyInserts(src, inserts), "github.com/Elagoht/gesso/auth", "os")
}

// applyInserts applies inserts, the later offset first so the earlier stay right.
func applyInserts(src []byte, inserts []insert) []byte {
	sort.SliceStable(inserts, func(i, j int) bool { return inserts[i].at > inserts[j].at })
	out := bytes.Clone(src)
	for _, in := range inserts {
		out = append(out[:in.at:in.at], append([]byte(in.text), out[in.at+in.cut:]...)...)
	}
	return out
}

// reformat parses src again, adds imports and formats it.
func reformat(fset *gotoken.FileSet, src []byte, imports ...string) ([]byte, error) {
	f, err := parser.ParseFile(fset, "register.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("wiring broke it: %w", err)
	}
	for _, p := range imports {
		astutil.AddImport(fset, f, p)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return format.Source(buf.Bytes())
}

// listInsert adds entry as the last element of lit.
func listInsert(fset *gotoken.FileSet, lit *ast.CompositeLit, entry string) insert {
	at := fset.Position(lit.Rbrace).Offset
	text := entry + ","
	if len(lit.Elts) == 0 || fset.Position(lit.Lbrace).Line == fset.Position(lit.Rbrace).Line {
		if len(lit.Elts) > 0 { // on one line: {a, b} becomes {a, b, entry}
			return insert{at: at, text: ", " + entry}
		}
		text = "\n" + text + "\n"
	} else if last := lit.Elts[len(lit.Elts)-1]; fset.Position(last.End()).Line == fset.Position(lit.Rbrace).Line {
		text = ",\n" + entry + ",\n" // the last element sits on the brace's line
	} else {
		text += "\n"
	}
	return insert{at: at, text: text}
}

func funcDecl(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	return nil
}

func structFields(f *ast.File, name string) *ast.FieldList {
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != gotoken.TYPE {
			continue
		}
		for _, s := range g.Specs {
			if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.Name == name {
				if st, ok := ts.Type.(*ast.StructType); ok {
					return st.Fields
				}
			}
		}
	}
	return nil
}

// names reports whether the parameter list names name.
func names(params *ast.FieldList, name string) bool {
	for _, p := range params.List {
		for _, n := range p.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

func lastReturn(fn *ast.FuncDecl) *ast.ReturnStmt {
	if fn == nil {
		return nil
	}
	if n := len(fn.Body.List); n > 0 {
		r, _ := fn.Body.List[n-1].(*ast.ReturnStmt)
		return r
	}
	return nil
}

func isName(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
