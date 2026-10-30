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
	"sort"
	"time"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/imports"
)

// addAuth is gesso add auth: the users who sign in, as a module of the app's —
// internal/users, its model //gesso:auth with the fields given, managed by
// admins, each user changing the fields marked self on themselves — and the
// signedIn and admin guards. Sign-in itself is gesso/auth's: login, logout,
// the tokens, the users command.
func addAuth(args []string, dir string, stdout io.Writer, now func() time.Time) error {
	fs := newFlags("add auth")
	parent := fs.String("dir", "internal", "the folder users/ and guards/ go in")
	var fieldList fieldFlags
	fs.Var(&fieldList, "field", "a user's field beside username, name:type[:options]: avatar:image:self,public,fill=256x256; again for each")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usage("add auth takes no arguments, only -field")
	}
	for name, src := range map[string]string{"signedIn": signedInGuard, "admin": adminGuard} {
		guard := filepath.Join(dir, *parent, "guards", name+".go")
		if _, err := os.Stat(guard); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(guard), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(guard, []byte(src), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "created", guard)
	}
	moduleArgs := []string{"users", "-auth", "-entity", "User", "-guard", "admin", "-read", "admin", "-dir", *parent,
		"-field", "username:string:key,lower,max=100"}
	for _, f := range fieldList {
		moduleArgs = append(moduleArgs, "-field", f)
	}
	if err := addModule(moduleArgs, dir, stdout, now); err != nil {
		return err
	}
	fmt.Fprint(stdout, `
next:
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

const adminGuard = `package guards

import (
	"net/http"

	"github.com/Elagoht/gesso"
)

func Admin(r *http.Request) error {
	return gesso.RequireRole("admin")(r)
}
`

// insert puts text at at, in place of the cut bytes there.
type insert struct {
	at, cut int
	text    string
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
func reformat(fset *gotoken.FileSet, src []byte, paths ...string) ([]byte, error) {
	f, err := parser.ParseFile(fset, "register.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("wiring broke it: %w", err)
	}
	for _, p := range paths {
		astutil.AddImport(fset, f, p)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	// The project's own imports in a group of their own, after the others.
	return imports.Process("", buf.Bytes(), &imports.Options{Comments: true, TabIndent: true, TabWidth: 8, FormatOnly: true})
}

func isName(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
