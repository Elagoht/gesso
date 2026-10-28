package password

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h := &Argon2id{Params: Cheap}
	enc, err := h.Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("got %s", enc)
	}
	if ok, err := h.Verify("correct horse", enc); !ok || err != nil {
		t.Fatalf("right password: %v %v", ok, err)
	}
	if ok, err := h.Verify("wrong horse", enc); ok || err != nil {
		t.Fatalf("wrong password: %v %v", ok, err)
	}
	other, _ := h.Hash("correct horse")
	if other == enc {
		t.Fatal("each hash has its own salt")
	}
}

func TestParamsTravelWithTheHash(t *testing.T) {
	old := &Argon2id{Params: Cheap}
	enc, _ := old.Hash("pw")
	raised := &Argon2id{Params: Params{Memory: 128, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}}
	if ok, err := raised.Verify("pw", enc); !ok || err != nil {
		t.Fatalf("an old hash still verifies: %v %v", ok, err)
	}
	if !raised.NeedsRehash(enc) || old.NeedsRehash(enc) {
		t.Fatal("NeedsRehash compares the params")
	}
}

func TestLimitsAndMalformed(t *testing.T) {
	h := &Argon2id{Params: Cheap}
	if _, err := h.Hash(strings.Repeat("a", MaxLength+1)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("got %v", err)
	}
	for _, enc := range []string{
		"", "plaintext", "$2a$10$bcrypthash",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$!!$a2V5",
	} {
		if _, err := h.Verify("pw", enc); !errors.Is(err, ErrMalformed) {
			t.Errorf("%q: got %v", enc, err)
		}
		if !h.NeedsRehash(enc) {
			t.Errorf("%q: an unreadable hash needs rehashing", enc)
		}
	}
}
