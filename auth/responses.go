package auth

import "time"

// ResSession is a signed-in user's bearer token: send it as
// Authorization: Bearer <token> until ExpiresAt. With JWTs it is the access
// token, and RefreshToken trades for a new pair at POST /auth/refresh until
// RefreshExpiresAt.
type ResSession struct {
	Token            string    `json:"token"`
	ExpiresAt        time.Time `json:"expiresAt"`
	RefreshToken     string    `json:"refreshToken,omitempty"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt,omitzero"`
}

// ResMe is the signed-in user.
type ResMe struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
}
