package auth

import "github.com/Elagoht/gesso"

// The module's endpoints: the server registers them, a collage page or another
// process calls them with the same types.
var (
	Login          = gesso.Endpoint[ReqLogin, ResSession]{Method: "POST", Pattern: "/auth/login"}
	Logout         = gesso.Endpoint[gesso.Empty, gesso.Empty]{Method: "POST", Pattern: "/auth/logout"}
	Me             = gesso.Endpoint[gesso.Empty, ResMe]{Method: "GET", Pattern: "/auth/me"}
	ChangePassword = gesso.Endpoint[ReqChangePassword, ResSession]{Method: "PUT", Pattern: "/auth/password"}
)
