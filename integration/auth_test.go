package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/auth"
	"github.com/Elagoht/gesso/auth/authtest"
	"github.com/Elagoht/gesso/password"
)

// newSQLUsers is an empty, migrated users table, hashing cheaply.
func newSQLUsers(t *testing.T, d *sql.DB, q queueDB) *auth.SQL {
	t.Helper()
	users := auth.NewSQL(d, q.dialect)
	users.Hasher = &password.Argon2id{Params: password.Cheap}
	if err := auth.Migrate(context.Background(), d, q.dialect); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("DELETE FROM gesso_users"); err != nil {
		t.Fatal(err)
	}
	return users
}

func TestAuthSQL(t *testing.T) {
	ctx := context.Background()
	for name, q := range queueDBs {
		t.Run(name, func(t *testing.T) {
			d := q.open(t)
			authtest.Run(t, func(t *testing.T) auth.Users {
				users := newSQLUsers(t, d, q)
				if _, err := users.Create(ctx, authtest.Username, authtest.Password, authtest.Role); err != nil {
					t.Fatal(err)
				}
				return users
			})

			users := newSQLUsers(t, d, q)
			if _, err := users.Create(ctx, "ada", "long enough"); err != nil {
				t.Fatal(err)
			}
			if _, err := users.Create(ctx, "ada", "long enough"); !errors.Is(err, auth.ErrUsernameTaken) {
				t.Fatalf("the same username twice: %v", err)
			}
			// Usernames match exactly, MySQL's folding collation notwithstanding.
			if _, err := users.Create(ctx, "Ada", "long enough"); err != nil {
				t.Fatalf("another case is another username: %v", err)
			}
			if _, err := users.Create(ctx, "grace", "short"); !errors.Is(err, auth.ErrWeakPassword) {
				t.Fatalf("a short password: %v", err)
			}
			if _, err := users.Create(ctx, "grace", "long enough", "a,b"); err == nil {
				t.Fatal("a role with a comma")
			}

			// A hash with older parameters is made again at sign-in.
			users.Hasher = &password.Argon2id{Params: password.Params{Memory: 128, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}
			if _, err := users.Authenticate(ctx, "ada", "long enough"); err != nil {
				t.Fatal(err)
			}
			var hash string
			if err := d.QueryRow("SELECT password_hash FROM gesso_users WHERE username = 'ada'").Scan(&hash); err != nil || !strings.Contains(hash, "m=128,") {
				t.Fatalf("not rehashed: %q %v", hash, err)
			}
		})
	}
}

// The module on each database: Start creates both tables; the operator's
// commands sign the user out.
func TestAuthModuleSQL(t *testing.T) {
	ctx := context.Background()
	for name, q := range queueDBs {
		t.Run(name, func(t *testing.T) {
			d := q.open(t)
			users := auth.NewSQL(d, q.dialect)
			users.Hasher = &password.Argon2id{Params: password.Cheap}
			if err := auth.Migrate(ctx, d, q.dialect); err != nil {
				t.Fatal(err)
			}
			app, err := gesso.New(gesso.Config{}, auth.New(auth.Options{DB: d, Dialect: q.dialect, Users: users}))
			if err != nil {
				t.Fatal(err)
			}
			if err := app.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.Shutdown(ctx) })
			api := gesso.NewLocal(app)

			// app users add ada -role editor, the password on standard input.
			var out bytes.Buffer
			run := users.Command(strings.NewReader("first password\n"))
			if err := run(ctx, []string{"add", "ada", "-role", "editor"}, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out.String(), "added ada (id ") {
				t.Fatalf("add: %q", out.String())
			}
			login := func(pass string) (gesso.CallOption, error) {
				s, err := auth.Login.Call(ctx, api, auth.ReqLogin{Username: "ada", Password: pass})
				return gesso.Bearer(s.Token), err
			}
			me, err := login("first password")
			if err != nil {
				t.Fatal(err)
			}
			got, err := auth.Me.Call(ctx, api, gesso.Empty{}, me)
			if err != nil || got.Username != "ada" || !slices.Equal(got.Roles, []string{"editor"}) {
				t.Fatalf("me: %+v %v", got, err)
			}

			s, err := auth.ChangePassword.Call(ctx, api, auth.ReqChangePassword{Current: "first password", New: "second password"}, me)
			if err != nil {
				t.Fatal(err)
			}
			me = gesso.Bearer(s.Token)

			for _, c := range []struct {
				args []string
				in   string
			}{
				{[]string{"roles", "ada", "editor", "admin"}, ""},
				{[]string{"password", "ada"}, "third password\n"},
				{[]string{"delete", "ada"}, ""},
			} {
				if _, err := auth.Me.Call(ctx, api, gesso.Empty{}, me); err != nil {
					t.Fatalf("before %v: %v", c.args, err)
				}
				out.Reset()
				if err := users.Command(strings.NewReader(c.in))(ctx, c.args, &out); err != nil {
					t.Fatalf("%v: %v", c.args, err)
				}
				if _, err := auth.Me.Call(ctx, api, gesso.Empty{}, me); !errors.Is(err, gesso.ErrInvalidToken) {
					t.Fatalf("%v leaves the user signed in: %v", c.args, err)
				}
				if c.args[0] != "delete" {
					pass := map[string]string{"roles": "second password", "password": "third password"}[c.args[0]]
					if me, err = login(pass); err != nil {
						t.Fatalf("after %v: %v", c.args, err)
					}
				}
			}
			out.Reset()
			if err := users.Command(nil)(ctx, []string{"list"}, &out); err != nil || out.Len() != 0 {
				t.Fatalf("list, everyone deleted: %q %v", out.String(), err)
			}
		})
	}
}
