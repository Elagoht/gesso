// Package password hashes and verifies passwords with argon2id, the algorithm
// OWASP recommends, in the PHC string format every argon2 library reads:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
//
// The parameters travel inside the string, so raising them later leaves old
// hashes verifiable; NeedsRehash says which to rehash at the user's next login.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Hasher hashes and verifies passwords. A service takes one, so a test can pass
// a cheap one.
type Hasher interface {
	Hash(password string) (string, error)
	Verify(password, encoded string) (bool, error)
	NeedsRehash(encoded string) bool
}

// MaxLength caps a password, in bytes: hashing is deliberately slow and costs
// memory, and an unbounded input would make both an attack.
const MaxLength = 1024

// ErrTooLong is returned for a password over MaxLength.
var ErrTooLong = fmt.Errorf("password: longer than %d bytes", MaxLength)

// ErrMalformed is returned for an encoded hash that is not argon2id PHC.
var ErrMalformed = errors.New("password: not an argon2id hash")

// Params are argon2id's costs.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32 // passes
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// Default is OWASP's first argon2id configuration: 64 MiB, 3 passes, 2 threads
// (about 50–100 ms on a server core).
var Default = Params{Memory: 64 * 1024, Time: 3, Threads: 2, SaltLen: 16, KeyLen: 32}

// Cheap is for tests only: fast, and worthless as protection.
var Cheap = Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

// Argon2id hashes with its params.
type Argon2id struct {
	Params Params
}

// New is an argon2id hasher with Default params.
func New() *Argon2id { return &Argon2id{Params: Default} }

// Hash returns the PHC string for password, with a fresh random salt.
func (a *Argon2id) Hash(password string) (string, error) {
	if len(password) > MaxLength {
		return "", ErrTooLong
	}
	p := a.Params
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether password matches encoded, in constant time. A wrong
// password is false with no error; an encoded string that cannot be read is an
// error.
func (a *Argon2id) Verify(password, encoded string) (bool, error) {
	if len(password) > MaxLength {
		return false, ErrTooLong
	}
	p, salt, key, err := decode(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// NeedsRehash reports whether encoded was made with other params than the
// hasher's, or cannot be read.
func (a *Argon2id) NeedsRehash(encoded string) bool {
	p, salt, key, err := decode(encoded)
	if err != nil {
		return true
	}
	want := a.Params
	return p.Memory != want.Memory || p.Time != want.Time || p.Threads != want.Threads ||
		uint32(len(salt)) != want.SaltLen || uint32(len(key)) != want.KeyLen
}

func decode(encoded string) (p Params, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrMalformed
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrMalformed
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil ||
		p.Memory == 0 || p.Time == 0 || p.Threads == 0 {
		return p, nil, nil, ErrMalformed
	}
	b64 := base64.RawStdEncoding
	if salt, err = b64.DecodeString(parts[4]); err != nil || len(salt) == 0 {
		return p, nil, nil, ErrMalformed
	}
	if key, err = b64.DecodeString(parts[5]); err != nil || len(key) == 0 {
		return p, nil, nil, ErrMalformed
	}
	return p, salt, key, nil
}
