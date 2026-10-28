package gesso

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CORS lets pages on other origins — a single-page app on app.example.com,
// calling the API on api.example.com — call the app from a browser. Without it
// a browser refuses to hand them the answers. A site that serves its pages and
// its API from one origin (collage mounting gesso) needs none.
type CORS struct {
	// Origins are the origins allowed, "https://app.example.com", exactly as a
	// browser sends them; "*" allows every origin, and cannot be combined with
	// Credentials.
	Origins []string
	// Credentials lets the browser send cookies and HTTP authentication with
	// the call. A bearer token in Authorization needs no credentials.
	Credentials bool
	// Headers the caller may send. Default: Authorization, Content-Type.
	Headers []string
	// Expose are response headers the page may read, besides the ones a
	// browser always shows. Default: Retry-After, WWW-Authenticate, X-Request-ID.
	Expose []string
	// MaxAge is how long a browser may keep a preflight's answer. Default 10m.
	MaxAge time.Duration
}

type corsPlan struct {
	any         bool
	origins     map[string]bool
	credentials bool
	headers     string
	expose      string
	maxAge      string
}

func newCORS(c *CORS) (*corsPlan, error) {
	if c == nil {
		return nil, nil
	}
	p := &corsPlan{origins: map[string]bool{}, credentials: c.Credentials}
	for _, o := range c.Origins {
		if o == "*" {
			p.any = true
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" {
			return nil, fmt.Errorf("gesso: CORS origin %q: want scheme://host[:port], as a browser sends it", o)
		}
		p.origins[strings.ToLower(o)] = true
	}
	if p.any && p.credentials {
		return nil, errors.New(`gesso: CORS: "*" with Credentials would let every site act as the signed-in user; list the origins`)
	}
	if !p.any && len(p.origins) == 0 {
		return nil, errors.New("gesso: CORS: no origins")
	}
	headers := c.Headers
	if len(headers) == 0 {
		headers = []string{"Authorization", "Content-Type"}
	}
	expose := c.Expose
	if len(expose) == 0 {
		expose = []string{"Retry-After", "WWW-Authenticate", "X-Request-ID"}
	}
	maxAge := c.MaxAge
	if maxAge <= 0 {
		maxAge = 10 * time.Minute
	}
	p.headers = strings.Join(headers, ", ")
	p.expose = strings.Join(expose, ", ")
	p.maxAge = strconv.Itoa(int(maxAge.Seconds()))
	return p, nil
}

// allow writes the CORS headers for r's origin, if it is allowed, and reports
// whether it was.
func (p *corsPlan) allow(w http.ResponseWriter, r *http.Request) bool {
	h := w.Header()
	h.Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if origin == "" || !(p.any || p.origins[strings.ToLower(origin)]) {
		return false
	}
	if p.any {
		h.Set("Access-Control-Allow-Origin", "*")
	} else {
		h.Set("Access-Control-Allow-Origin", origin)
	}
	if p.credentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	return true
}

// preflight answers a browser's OPTIONS question — may this origin send this
// method here? — for a route that exists. It reports whether it answered.
func (a *App) preflight(w http.ResponseWriter, r *http.Request) bool {
	method := r.Header.Get("Access-Control-Request-Method")
	if r.Method != http.MethodOptions || method == "" || r.Header.Get("Origin") == "" {
		return false
	}
	probe := r.Clone(r.Context())
	probe.Method = method
	if _, pattern := a.mux.Handler(probe); pattern == "" {
		return false // no such route: the 404 or 405 says so
	}
	if !a.cors.allow(w, r) {
		return false
	}
	h := w.Header()
	h.Add("Vary", "Access-Control-Request-Method")
	h.Add("Vary", "Access-Control-Request-Headers")
	h.Set("Access-Control-Allow-Methods", method)
	h.Set("Access-Control-Allow-Headers", a.cors.headers)
	h.Set("Access-Control-Max-Age", a.cors.maxAge)
	w.WriteHeader(http.StatusNoContent)
	return true
}

// actual writes the headers of a cross-origin request that is not a preflight.
func (a *App) actual(w http.ResponseWriter, r *http.Request) {
	if a.cors.allow(w, r) && a.cors.expose != "" {
		w.Header().Set("Access-Control-Expose-Headers", a.cors.expose)
	}
}
