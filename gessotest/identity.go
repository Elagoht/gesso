package gessotest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/auth"
)

// Identities is an Authenticator for tests: the bearer token is a key of the
// map, and the request is that identity. No token is anonymous; a token not in
// the map is gesso.ErrInvalidToken.
//
//	app := gessotest.New(t, gesso.Config{Authenticator: gessotest.Identities{
//		"ada": {Subject: "1", Roles: []string{"admin"}},
//	}}, docs.New(…))
//	docs.CreateDoc.Call(ctx, gesso.NewLocal(app), in, gesso.Bearer("ada"))
type Identities map[string]gesso.Identity

func (ids Identities) Authenticate(r *http.Request) (gesso.Identity, bool, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return gesso.Identity{}, false, nil
	}
	id, ok := ids[raw]
	if !ok {
		return gesso.Identity{}, false, gesso.ErrInvalidToken
	}
	return id, true, nil
}

// SignIn signs username in through the auth module's Login on rm, failing the
// test if it cannot, and is the bearer to call as them with.
func SignIn(t testing.TB, rm *gesso.Remote, username, password string) gesso.CallOption {
	t.Helper()
	s, err := auth.Login.Call(context.Background(), rm, auth.ReqLogin{Username: username, Password: password})
	if err != nil {
		t.Fatalf("gessotest: sign in as %s: %v", username, err)
	}
	return gesso.Bearer(s.Token)
}
