package gesso

import (
	"context"
	"slices"
	"testing"
)

func TestRoles(t *testing.T) {
	r := Roles{
		"admin":  {"*"},
		"editor": {"docs:*"},
		"writer": {"docs:list", "docs:create", "docs:update:own"},
		"mixed":  {"docs:update:own", "notes:*:own"},
	}
	for _, c := range []struct {
		roles []string
		perm  string
		want  Grant
	}{
		{[]string{"admin"}, "users:delete", GrantAll},
		{[]string{"editor"}, "docs:delete", GrantAll},
		{[]string{"editor"}, "notes:list", GrantNone},
		{[]string{"writer"}, "docs:create", GrantAll},
		{[]string{"writer"}, "docs:update", GrantOwn},
		{[]string{"writer"}, "docs:delete", GrantNone},
		{[]string{"writer", "editor"}, "docs:update", GrantAll},
		{[]string{"mixed"}, "notes:delete", GrantOwn},
		{nil, "docs:list", GrantNone},
		{[]string{"ghost"}, "docs:list", GrantNone},
	} {
		if got := r.grantOf(c.roles, c.perm); got != c.want {
			t.Errorf("%v %s: %d, want %d", c.roles, c.perm, got, c.want)
		}
	}
	ctx := context.WithValue(WithIdentity(context.Background(), Identity{Subject: "1", Roles: []string{"writer", "editor"}}), rolesKey{}, r)
	if got := Permissions(ctx); !slices.Equal(got, []string{"docs:*", "docs:create", "docs:list", "docs:update:own"}) {
		t.Fatalf("permissions: %v", got)
	}
	if Allows(context.Background(), "docs:list") != GrantNone {
		t.Fatal("anonymous")
	}
}

func TestCan(t *testing.T) {
	a := mustNew(t, Config{Authenticator: auth}, moduleFunc(func(r *Router) {
		r.Get("/x", Handle(func(ctx context.Context, _ Empty) (Empty, error) { return Empty{}, nil }, Guards(Can("docs:delete"))))
	}))
	a.Roles(Roles{"admin": {"docs:delete:own"}, Everyone: {"docs:list"}})
	if code, _, _ := withToken(a, "GET", "/x", "", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _, _ := withToken(a, "GET", "/x", "bob", ""); code != 403 {
		t.Fatalf("no role: %d", code)
	}
	if code, _, _ := withToken(a, "GET", "/x", "ada", ""); code != 204 {
		t.Fatalf("an :own grant passes the guard: %d", code)
	}
	ctx := context.WithValue(context.Background(), rolesKey{}, Roles{Everyone: {"docs:list", "docs:edit:own"}})
	if Allows(ctx, "docs:list") != GrantAll || Allows(ctx, "docs:edit") != GrantNone {
		t.Fatal("everyone, signed in or not")
	}
}
