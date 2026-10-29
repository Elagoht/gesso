package gesso

import (
	"html/template"
	"net/http"
)

// ServeSwaggerUI answers with Swagger UI for the app's OpenAPI document,
// wherever a router registers it and behind whatever guards it has. It answers
// 404 when Config.OpenAPI is unset.
func ServeSwaggerUI() http.Handler { return swaggerHandler{} }

type swaggerHandler struct{ app *App }

func (h swaggerHandler) bind(app *App, _ route) (http.Handler, error) {
	return swaggerHandler{app: app}, nil
}

func (h swaggerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.app == nil || h.app.openapi == nil {
		openAPIHandler(h).errs().write(w, r, ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// The document is JSON from encoding/json, which escapes <, > and &: safe
	// inside a script.
	_ = swaggerPage.Execute(w, swaggerData{Title: h.app.cfg.OpenAPI.Title, Spec: template.JS(h.app.openapi)})
}

type swaggerData struct {
	Title string
	Spec  template.JS
}

var swaggerPage = template.Must(template.New("").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.33.3/swagger-ui.css" integrity="sha384-Ov4/wv3j2bmct8cDc5X4ngJZohVPzEmc6uDPH8WeljUxO5vtoykvMEfbu9Vh6RaW" crossorigin="anonymous">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.33.3/swagger-ui-bundle.js" integrity="sha384-h00llMzD1afyNow1pDmnZKlHsUZgp9KE9HXLFXF2CvbwsBY0+WBO4f75SfGwvM4K" crossorigin="anonymous"></script>
<script>
SwaggerUIBundle({spec: {{.Spec}}, dom_id: "#swagger-ui", persistAuthorization: true, deepLinking: true});
</script>
</body>
</html>
`))
