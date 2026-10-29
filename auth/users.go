package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"slices"

	"github.com/Elagoht/gesso"
)

// User is someone who may sign in.
type User struct {
	// ID names the user in tokens and in gesso.Identity's Subject. It does not
	// change when the username does.
	ID       string
	Username string
	Roles    []string
}

// Users is where the users come from: NewSingle, NewSQL, or a source of the
// application's own — a directory, another service.
type Users interface {
	// Authenticate returns the user that username and password sign in, or
	// ErrBadCredentials. It takes as long for a username nobody has as for a
	// wrong password, so that the time it takes tells nobody which names exist.
	Authenticate(ctx context.Context, username, password string) (User, error)
	// Lookup returns the user with id, or ErrNoUser.
	Lookup(ctx context.Context, id string) (User, error)
}

// PasswordChanger is Users that let a user change their own password: the
// module then serves ChangePassword.
type PasswordChanger interface {
	// ChangePassword sets next as the password of the user with id, if current
	// is the one they have; ErrBadCredentials if it is not.
	ChangePassword(ctx context.Context, id, current, next string) error
}

var (
	// ErrBadCredentials is a wrong username or password; which of them is not
	// told.
	ErrBadCredentials = gesso.Unauthorized("bad_credentials", "the username or the password is wrong")
	// ErrNoUser is Lookup's answer for an id no user has.
	ErrNoUser = gesso.NotFound("no_user", "there is no such user")
	// ErrUsernameTaken is a username another user has.
	ErrUsernameTaken = gesso.Conflict("username_taken", "the username is taken")
	// ErrWeakPassword is a password shorter than MinPassword.
	ErrWeakPassword = gesso.Unprocessable("weak_password", "the password is too short")
)

// MinPassword is the fewest characters a password set through SQL may have:
// NIST SP 800-63B's floor. Single takes what it is given; it is the operator's.
const MinPassword = 8

// Single is one user, whose username and password are the server's settings:
// a personal site, an admin of one. Its ID is the username.
type Single struct {
	user     User
	password string
}

// NewSingle is the one user username, signed in with password, holding roles.
// The module refuses an empty username or password.
func NewSingle(username, password string, roles ...string) *Single {
	return &Single{user: User{ID: username, Username: username, Roles: slices.Clone(roles)}, password: password}
}

// Authenticate compares both, always, in time that does not depend on where
// they differ.
func (s *Single) Authenticate(_ context.Context, username, password string) (User, error) {
	userOK := same(username, s.user.Username)
	passOK := same(password, s.password)
	if !userOK || !passOK {
		return User{}, ErrBadCredentials
	}
	return s.clone(), nil
}

// Lookup returns the user when id is theirs.
func (s *Single) Lookup(_ context.Context, id string) (User, error) {
	if id != s.user.ID {
		return User{}, ErrNoUser
	}
	return s.clone(), nil
}

func (s *Single) clone() User {
	u := s.user
	u.Roles = slices.Clone(u.Roles)
	return u
}

func (s *Single) empty() bool { return s.user.Username == "" || s.password == "" }

// same compares the hashes, so that neither the length nor the content of a
// guess shows in the time it takes.
func same(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
