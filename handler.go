package gesso

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/Elagoht/gesso/validate"
)

// Empty is the input of a handler that reads nothing, and the output of one that
// answers nothing: an Empty output is a 204 No Content.
type Empty struct{}

// HandlerFunc is a typed handler: it receives the request's context and its
// decoded, validated input, and returns what to answer or why not.
type HandlerFunc[In, Out any] func(ctx context.Context, in In) (Out, error)

// HandleOption changes how a typed handler answers.
type HandleOption func(*handleConfig)

type handleConfig struct {
	status int
}

// Status answers a success with code instead of 200 (or 204 for Empty):
// Status(http.StatusCreated) for a handler that creates.
func Status(code int) HandleOption {
	return func(c *handleConfig) { c.status = code }
}

// Handle makes a typed handler into an http.Handler. In is decoded from the
// request (see the input rules on decoding) and validated with its validate tags
// before fn runs, so fn never sees invalid input; Out is written as JSON.
//
// Registered on a Router, its input is checked when the route is: a path field
// the pattern does not have, or a tag gesso cannot read, is an error from New.
func Handle[In, Out any](fn HandlerFunc[In, Out], opts ...HandleOption) http.Handler {
	h := &typed[In, Out]{fn: fn}
	for _, opt := range opts {
		if opt != nil {
			opt(&h.cfg)
		}
	}
	if h.cfg.status == 0 {
		h.cfg.status = http.StatusOK
		if reflect.TypeFor[Out]() == emptyType {
			h.cfg.status = http.StatusNoContent
		}
	}
	return h
}

type typed[In, Out any] struct {
	fn   HandlerFunc[In, Out]
	cfg  handleConfig
	plan *inputPlan
	errs errorWriter
	max  int64

	bound bool      // registered on a Router, which planned it
	once  sync.Once // plans it on first use when mounted without one
	err   error     // from that late planning
}

func (h *typed[In, Out]) bind(app *App, pattern string) error {
	if err := h.prepare(); err != nil {
		return err
	}
	for _, pp := range h.plan.params {
		if pp.src == fromPath && !strings.Contains(pattern, "{"+pp.key+"}") && !strings.Contains(pattern, "{"+pp.key+"...}") {
			return fmt.Errorf("input field %q reads path parameter {%s}, which the pattern does not have", pp.name, pp.key)
		}
	}
	h.errs = app.errs
	h.max = app.cfg.MaxBodyBytes
	h.bound = true
	return nil
}

func (h *typed[In, Out]) prepare() error {
	if h.plan != nil {
		return nil
	}
	plan, err := planInput(reflect.TypeFor[In]())
	if err != nil {
		return err
	}
	if reflect.TypeFor[In]() != emptyType {
		if err := validate.Compile[In](); err != nil {
			return err
		}
	}
	h.plan = plan
	return nil
}

func (h *typed[In, Out]) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.bound {
		// Mounted without a Router: plan on first use, with default settings.
		h.once.Do(func() {
			h.err = h.prepare()
			h.errs = errorWriter{logger: slog.Default()}
			h.max = defaultMaxBody
		})
	}
	if h.err != nil {
		h.errs.write(w, r, h.err)
		return
	}
	ctx := context.WithValue(r.Context(), exchangeKey{}, &exchange{r: r, w: w})
	var in In
	if err := h.plan.decode(r, reflect.ValueOf(&in).Elem(), h.max); err != nil {
		h.errs.write(w, r, err)
		return
	}
	if reflect.TypeFor[In]() != emptyType {
		if fields := validate.Struct(ctx, in); fields != nil {
			h.errs.write(w, r, ErrInvalidInput.WithFields(fields...))
			return
		}
	}
	out, err := h.fn(ctx, in)
	if err != nil {
		h.errs.write(w, r, err)
		return
	}
	if h.cfg.status == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(out); err != nil {
		h.errs.write(w, r, fmt.Errorf("gesso: encode response: %w", err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(h.cfg.status)
	_, _ = w.Write(buf.Bytes())
}

type exchangeKey struct{}

type exchange struct {
	r *http.Request
	w http.ResponseWriter
}

// Request is the HTTP request a typed handler is answering, for what the input
// does not carry: the client's address, a cookie. It is nil outside one.
func Request(ctx context.Context) *http.Request {
	if x, ok := ctx.Value(exchangeKey{}).(*exchange); ok {
		return x.r
	}
	return nil
}

// ResponseHeader is the header of the response a typed handler will write: set a
// Location, a cookie, a cache policy. It is nil outside one.
func ResponseHeader(ctx context.Context) http.Header {
	if x, ok := ctx.Value(exchangeKey{}).(*exchange); ok {
		return x.w.Header()
	}
	return nil
}
