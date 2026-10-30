package auth

// ReqLogin is a sign-in attempt.
type ReqLogin struct {
	Username string `json:"username" validate:"required,max=255"`
	Password string `json:"password" validate:"required,max=1024"`
}

// ReqRefresh trades a refresh token for a new pair.
type ReqRefresh struct {
	RefreshToken string `json:"refreshToken" validate:"required,max=4096"`
}

// ReqChangePassword changes the signed-in user's password: Current proves it is
// them, not someone at a device they left signed in.
type ReqChangePassword struct {
	Current string `json:"currentPassword" validate:"required,max=1024"`
	New     string `json:"newPassword" validate:"required,min=8,max=1024"`
}
