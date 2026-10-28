package token_test

import (
	"testing"

	"github.com/Elagoht/gesso/token"
	"github.com/Elagoht/gesso/token/tokentest"
)

func TestMemoryStore(t *testing.T) {
	tokentest.Run(t, func(*testing.T) token.Store { return token.NewMemory() })
}
