package token_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/token"
	"github.com/Elagoht/gesso/token/tokentest"
)

func TestMemoryStore(t *testing.T) {
	tokentest.Run(t, func(*testing.T) token.Store { return token.NewMemory() })
}

// A Bearer header that is there but holds no single token is a bad credential,
// not none.
func TestBlankBearerIsInvalid(t *testing.T) {
	iss := token.NewIssuer(token.NewMemory(), nil, time.Hour)
	for _, h := range []string{"Bearer", "Bearer ", "bearer\t", "Bearer a b"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", h)
		if _, _, err := iss.Authenticate(r); !errors.Is(err, gesso.ErrInvalidToken) {
			t.Errorf("%q: %v", h, err)
		}
	}
	raw, _, _ := iss.Issue(context.Background(), "ada")
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer\t"+raw)
	if id, ok, err := iss.Authenticate(r); err != nil || !ok || id.Subject != "ada" {
		t.Fatalf("a tab after the scheme: %v %v %v", id, ok, err)
	}
	for _, h := range []string{"", "Basic dXNlcjpwYXNz"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", h)
		if _, ok, err := iss.Authenticate(r); ok || err != nil {
			t.Errorf("%q is another scheme's or none: %v %v", h, ok, err)
		}
	}
}
