package auth

import "time"

// ResSession is a signed-in user's bearer token: send it as
// Authorization: Bearer <token> until ExpiresAt.
type ResSession struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ResMe is the signed-in user.
type ResMe struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
}
