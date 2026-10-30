package gesso

import (
	"context"
	"net/http"
	"slices"
	"strings"
)

// Everyone is the role every caller has, signed in or not: what it grants,
// anyone may do — "everyone": {"docs:list", "docs:get"} opens the docs to
// reading.
const Everyone = "everyone"

// Roles grant permissions: role → what it may do. A permission is
// "<module>:<action>" — "docs:create" — and may end in ":own", for the records
// the signed-in user owns alone; "docs:*" is every action on docs, "*" every
// permission there is.
//
//	app.Roles(gesso.Roles{
//	    "admin":  {"*"},
//	    "editor": {"docs:*"},
//	    "writer": {"docs:list", "docs:get", "docs:create", "docs:update:own", "docs:delete:own"},
//	})
type Roles map[string][]string

// Grant is how far a permission reaches.
type Grant int

const (
	// GrantNone: not at all.
	GrantNone Grant = iota
	// GrantOwn: on the records the caller owns.
	GrantOwn
	// GrantAll: on every record.
	GrantAll
)

// Roles sets what each role may do, for Can and the modules that check
// permissions; registered once, before serving.
func (a *App) Roles(r Roles) {
	a.roles = make(Roles, len(r))
	for role, perms := range r {
		a.roles[role] = slices.Clone(perms)
	}
}

type rolesKey struct{}

// grantOf is how far the roles reach for perm.
func (r Roles) grantOf(roles []string, perm string) Grant {
	module, _, _ := strings.Cut(perm, ":")
	g := GrantNone
	for _, role := range roles {
		for _, p := range r[role] {
			own := strings.HasSuffix(p, ":own")
			p = strings.TrimSuffix(p, ":own")
			if p == "*" || p == perm || p == module+":*" {
				if !own {
					return GrantAll
				}
				g = GrantOwn
			}
		}
	}
	return g
}

// Permissions are what the roles grant, as the role table writes them.
func (r Roles) permissions(roles []string) []string {
	var out []string
	for _, role := range roles {
		for _, p := range r[role] {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return out
}

func rolesFrom(ctx context.Context) Roles {
	r, _ := ctx.Value(rolesKey{}).(Roles)
	return r
}

// Allows is how far the caller's roles reach for perm, by App.Roles.
func Allows(ctx context.Context, perm string) Grant {
	id, _ := IdentityFrom(ctx)
	g := rolesFrom(ctx).grantOf(append(slices.Clone(id.Roles), Everyone), perm)
	if g == GrantOwn && id.Subject == "" {
		return GrantNone // nobody signed in owns anything
	}
	return g
}

// Permissions are what the caller's roles grant, for a client to know which
// of its buttons to show.
func Permissions(ctx context.Context) []string {
	id, _ := IdentityFrom(ctx)
	p := rolesFrom(ctx).permissions(append(slices.Clone(id.Roles), Everyone))
	if p == nil {
		p = []string{}
	}
	return p
}

// Can is a guard: the caller's roles grant perm, on some records at least —
// one granted it :own only is let through, and the handler keeps it to what
// it owns. Nobody signed in, and Everyone not granting it, is a 401.
func Can(perm string) Guard {
	return func(r *http.Request) error {
		if Allows(r.Context(), perm) != GrantNone {
			return nil
		}
		if _, ok := IdentityFrom(r.Context()); !ok {
			return ErrUnauthenticated
		}
		return ErrForbidden
	}
}
