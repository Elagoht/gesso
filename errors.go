package gesso

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Elagoht/gesso/validate"
)

// Error is an error that knows how it is answered: an HTTP status, a stable code a
// client can act on, and a sentence for a person. A service returns one, or wraps
// one with %w, and gesso writes it as problem+json (RFC 9457).
//
// Declare an application's errors once, next to the code that returns them:
//
//	var ErrEventFull = gesso.Conflict("event_full", "the event is full")
//
// errors.Is matches an Error by status and code, so a copy made by Wrap or
// WithDetail still matches the declaration.
type Error struct {
	Status int
	Code   string
	Detail string
	Fields []validate.FieldError
	cause  error
	ext    []byte // a JSON object of extension members, from Extend
}

// NewError declares an error answered with status.
func NewError(status int, code, detail string) *Error {
	return &Error{Status: status, Code: code, Detail: detail}
}

func (e *Error) Error() string {
	msg := e.Code
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}
	return msg
}

// Unwrap is the cause given to Wrap. It is logged, never sent.
func (e *Error) Unwrap() error { return e.cause }

// Is matches another *Error with the same status and code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Status == e.Status && t.Code == e.Code
}

// Wrap is a copy of e that carries cause, for the log.
func (e *Error) Wrap(cause error) *Error {
	c := *e
	c.cause = cause
	return &c
}

// WithDetail is a copy of e with another sentence for the client.
func (e *Error) WithDetail(detail string) *Error {
	c := *e
	c.Detail = detail
	return &c
}

// WithFields is a copy of e naming the fields at fault.
func (e *Error) WithFields(fields ...validate.FieldError) *Error {
	c := *e
	c.Fields = append(append([]validate.FieldError(nil), e.Fields...), fields...)
	return &c
}

// Extend is a copy of e whose problem carries ext's fields as extension
// members, beside type, title, status and the rest:
//
//	type retry struct{ RetryAfter int `json:"retryAfter"` }
//	return gesso.Extend(ErrRateLimited, retry{RetryAfter: 30})
//
// ext must encode as a JSON object that does not redefine a standard member;
// anything else is a programmer error, and Extend panics. Wrap, WithDetail and
// WithFields keep the extension; errors.Is still matches the declaration.
func Extend[T any](e *Error, ext T) *Error {
	data, err := json.Marshal(ext)
	if err != nil {
		panic("gesso: Extend: " + err.Error())
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil || members == nil {
		panic(fmt.Sprintf("gesso: Extend: %T does not encode as a JSON object", ext))
	}
	for _, std := range []string{"type", "title", "status", "code", "detail", "instance", "errors"} {
		if _, ok := members[std]; ok {
			panic(fmt.Sprintf("gesso: Extend: %T redefines the standard member %q", ext, std))
		}
	}
	c := *e
	c.ext = nil
	if len(members) > 0 {
		c.ext = data
	}
	return &c
}

// BadRequest declares a 400.
func BadRequest(code, detail string) *Error { return NewError(http.StatusBadRequest, code, detail) }

// Unauthorized declares a 401.
func Unauthorized(code, detail string) *Error {
	return NewError(http.StatusUnauthorized, code, detail)
}

// Forbidden declares a 403.
func Forbidden(code, detail string) *Error { return NewError(http.StatusForbidden, code, detail) }

// NotFound declares a 404.
func NotFound(code, detail string) *Error { return NewError(http.StatusNotFound, code, detail) }

// Conflict declares a 409.
func Conflict(code, detail string) *Error { return NewError(http.StatusConflict, code, detail) }

// Unprocessable declares a 422.
func Unprocessable(code, detail string) *Error {
	return NewError(http.StatusUnprocessableEntity, code, detail)
}

// The errors gesso itself answers with. An application may return them too.
var (
	ErrNotFound         = NotFound("not_found", "the resource was not found")
	ErrMethodNotAllowed = NewError(http.StatusMethodNotAllowed, "method_not_allowed", "the method is not allowed here")
	ErrInvalidJSON      = BadRequest("invalid_json", "the body is not valid JSON")
	ErrInvalidInput     = BadRequest("invalid_input", "the request failed validation")
	ErrUnsupportedMedia = NewError(http.StatusUnsupportedMediaType, "unsupported_media_type", "the body must be application/json")
	ErrBodyTooLarge     = NewError(http.StatusRequestEntityTooLarge, "body_too_large", "the body is too large")
	ErrTimeout          = NewError(http.StatusServiceUnavailable, "timeout", "the request took too long")
	ErrInternal         = NewError(http.StatusInternalServerError, "internal", "something went wrong")
)

// ErrorMapper turns an error gesso does not know into one it can answer, or
// reports false. Config.ErrorMappers holds them; db.Errors makes one for database
// constraint violations.
type ErrorMapper func(error) (*Error, bool)

// Problem is the body of every error gesso answers: RFC 9457 problem details, with
// a stable code and the fields at fault.
type Problem struct {
	Type     string                `json:"type"`
	Title    string                `json:"title"`
	Status   int                   `json:"status"`
	Code     string                `json:"code"`
	Detail   string                `json:"detail,omitempty"`
	Instance string                `json:"instance,omitempty"`
	Errors   []validate.FieldError `json:"errors,omitempty"`
}

// errorWriter answers errors the same way everywhere: handlers, the router's own
// 404 and 405, a recovered panic.
type errorWriter struct {
	logger  *slog.Logger
	mappers []ErrorMapper
}

func (ew errorWriter) resolve(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	for _, m := range ew.mappers {
		if e, ok := m(err); ok {
			return e.Wrap(err)
		}
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound.Wrap(err)
	case errors.Is(err, context.DeadlineExceeded):
		return ErrTimeout.Wrap(err)
	}
	return ErrInternal.Wrap(err)
}

func (ew errorWriter) write(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return // the client has gone; there is no one to answer
	}
	e := ew.resolve(err)
	if e.Status >= 500 {
		ew.logger.ErrorContext(r.Context(), "request failed",
			"method", r.Method, "path", r.URL.Path, "status", e.Status, "error", err)
	}
	detail := e.Detail
	if e.Status >= 500 && e.Code == ErrInternal.Code {
		detail = ErrInternal.Detail // never send the cause of an unknown failure
	}
	p := Problem{
		Type: "about:blank", Title: http.StatusText(e.Status), Status: e.Status,
		Code: e.Code, Detail: detail, Instance: r.URL.Path, Errors: e.Fields,
	}
	body, err := json.Marshal(p)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if e.ext != nil {
		// Both are JSON objects: splice the extension's members into the problem.
		body = append(append(body[:len(body)-1], ','), e.ext[1:]...)
	}
	h := w.Header()
	if e.Status == http.StatusUnauthorized && h.Get("WWW-Authenticate") == "" {
		h.Set("WWW-Authenticate", "Bearer")
	}
	h.Set("Content-Type", "application/problem+json")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(e.Status)
	_, _ = w.Write(append(body, '\n'))
}
