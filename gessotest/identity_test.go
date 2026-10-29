package gessotest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/gessotest"
)

type whoModule struct{}

var who = gesso.Endpoint[gesso.Empty, string]{Method: "GET", Pattern: "/who"}

func (whoModule) Routes(r *gesso.Router) {
	who.Handle(r, func(ctx context.Context, _ gesso.Empty) (string, error) {
		id, _ := gesso.IdentityFrom(ctx)
		return id.Subject, nil
	}, gesso.Guards(gesso.RequireIdentity))
}

func TestIdentities(t *testing.T) {
	app := gessotest.New(t, gesso.Config{Authenticator: gessotest.Identities{"ada": {Subject: "1"}}}, whoModule{})
	rm, ctx := gesso.NewLocal(app), context.Background()
	if got, err := who.Call(ctx, rm, gesso.Empty{}, gesso.Bearer("ada")); err != nil || got != "1" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := who.Call(ctx, rm, gesso.Empty{}); !errors.Is(err, gesso.ErrUnauthenticated) {
		t.Fatalf("no token: %v", err)
	}
	if _, err := who.Call(ctx, rm, gesso.Empty{}, gesso.Bearer("eve")); !errors.Is(err, gesso.ErrInvalidToken) {
		t.Fatalf("an unknown token: %v", err)
	}
}
