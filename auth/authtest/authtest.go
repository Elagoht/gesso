// Package authtest holds every auth.Users to the same contract.
//
//	authtest.Run(t, func(t *testing.T) auth.Users {
//		users := auth.NewSQL(db, migrate.Postgres)
//		… // add authtest.Username with authtest.Password and the role authtest.Role
//		return users
//	})
//
// open returns Users holding one user: Username, with Password, holding Role.
// Users that are an auth.PasswordChanger have that checked too.
package authtest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Elagoht/gesso/auth"
)

// The user open's Users hold.
const (
	Username = "ada"
	Password = "correct horse battery"
	Role     = "admin"
)

// Run checks the contract.
func Run(t *testing.T, open func(t *testing.T) auth.Users) {
	ctx := context.Background()

	t.Run("the right password signs in", func(t *testing.T) {
		users := open(t)
		u, err := users.Authenticate(ctx, Username, Password)
		if err != nil {
			t.Fatal(err)
		}
		if u.ID == "" || u.Username != Username || !slices.Equal(u.Roles, []string{Role}) {
			t.Fatalf("user: %+v", u)
		}
		again, err := users.Lookup(ctx, u.ID)
		if err != nil || again.Username != Username || !slices.Equal(again.Roles, []string{Role}) {
			t.Fatalf("Lookup(%q): %+v %v", u.ID, again, err)
		}
	})

	t.Run("anything else is ErrBadCredentials", func(t *testing.T) {
		users := open(t)
		for _, c := range []struct{ name, username, password string }{
			{"wrong password", Username, "wrong horse battery"},
			{"empty password", Username, ""},
			{"no such user", "grace", Password},
			{"the username in another case", "ADA", Password},
			{"a prefix of the password", Username, Password[:len(Password)-1]},
		} {
			if _, err := users.Authenticate(ctx, c.username, c.password); !errors.Is(err, auth.ErrBadCredentials) {
				t.Errorf("%s: %v, want ErrBadCredentials", c.name, err)
			}
		}
	})

	t.Run("an id nobody has is ErrNoUser", func(t *testing.T) {
		users := open(t)
		for _, id := range []string{"", "nobody", "999999", "-1"} {
			if _, err := users.Lookup(ctx, id); !errors.Is(err, auth.ErrNoUser) {
				t.Errorf("Lookup(%q): %v, want ErrNoUser", id, err)
			}
		}
	})

	t.Run("a user changes their password", func(t *testing.T) {
		users := open(t)
		pc, ok := users.(auth.PasswordChanger)
		if !ok {
			t.Skip("not a PasswordChanger")
		}
		u, err := users.Authenticate(ctx, Username, Password)
		if err != nil {
			t.Fatal(err)
		}
		const next = "battery staple horse"
		if err := pc.ChangePassword(ctx, u.ID, "not it at all", next); !errors.Is(err, auth.ErrBadCredentials) {
			t.Fatalf("a wrong current password: %v", err)
		}
		if err := pc.ChangePassword(ctx, u.ID, Password, next); err != nil {
			t.Fatal(err)
		}
		if _, err := users.Authenticate(ctx, Username, Password); !errors.Is(err, auth.ErrBadCredentials) {
			t.Fatalf("the old password still signs in: %v", err)
		}
		if _, err := users.Authenticate(ctx, Username, next); err != nil {
			t.Fatalf("the new one does not: %v", err)
		}
	})
}
