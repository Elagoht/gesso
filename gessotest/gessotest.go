// Package gessotest tests a gesso application: build it, send it requests in
// process, read its answers and problems, and run each test inside a database
// transaction that is rolled back when it ends.
//
//	func TestCreateEvent(t *testing.T) {
//	    tx := gessotest.Tx(t, testDB)
//	    app := gessotest.New(t, gesso.Config{}, events.New(events.Deps{DB: tx}))
//	    c := gessotest.NewClient(t, app)
//
//	    res := c.Post("/events", gessotest.JSON(events.CreateIn{Title: "Jam"}))
//	    res.ExpectStatus(http.StatusCreated)
//	    got := gessotest.Decode[events.Event](res)
//
//	    c.Post("/events", gessotest.JSON(events.CreateIn{})).ExpectProblem(400, "invalid_input")
//	}
package gessotest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/db"
)

// New builds an app, failing the test on a route problem, and starts it; the
// test's cleanup shuts it down.
func New(t testing.TB, cfg gesso.Config, modules ...gesso.Module) *gesso.App {
	t.Helper()
	app, err := gesso.New(cfg, modules...)
	if err != nil {
		t.Fatalf("gessotest: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("gessotest: start: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("gessotest: shutdown: %v", err)
		}
	})
	return app
}

// Tx begins a transaction on db and rolls it back when the test ends, so nothing
// the test writes outlives it. Hand it to the repositories under test as their
// db.DB; a db.InTx inside it nests as a savepoint.
//
// A transaction is one connection: requests in one test run one after another,
// not in parallel. And it holds only for an app served in process, as Client
// does; a server in another process has connections of its own.
func Tx(t testing.TB, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("gessotest: begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// FakeDB is a db.DB for unit tests that touch no database: a service under test
// with fake repositories still calls db.InTx, which FakeDB answers by running the
// unit, with itself as the transaction. A query run on it fails with
// ErrFakeDB, so a repository that was meant to be faked and was not is caught.
type FakeDB struct{}

// ErrFakeDB is what a query on a FakeDB returns.
var ErrFakeDB = errors.New("gessotest: FakeDB has no database; fake the repository")

// ExecContext fails with ErrFakeDB.
func (FakeDB) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, ErrFakeDB
}

// QueryContext fails with ErrFakeDB.
func (FakeDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, ErrFakeDB
}

// QueryRowContext panics: *sql.Row cannot carry an error made outside
// database/sql, and a nil one would fail later and further from the cause.
func (FakeDB) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic(ErrFakeDB.Error())
}

// InTx runs fn with the FakeDB as its transaction.
func (f FakeDB) InTx(_ context.Context, _ *sql.TxOptions, fn func(tx db.DB) error) error {
	return fn(f)
}

// Body is a request body and its content type.
type Body struct {
	ContentType string
	Data        []byte
}

// JSON encodes v as a JSON body.
func JSON[T any](v T) Body {
	data, err := json.Marshal(v)
	if err != nil {
		panic("gessotest: JSON: " + err.Error())
	}
	return Body{ContentType: "application/json", Data: data}
}

// Raw is a body sent as it is.
func Raw(contentType, data string) Body {
	return Body{ContentType: contentType, Data: []byte(data)}
}

// Client sends requests to a handler in process.
type Client struct {
	t       testing.TB
	h       http.Handler
	headers http.Header
}

// NewClient sends to h, an app or any handler.
func NewClient(t testing.TB, h http.Handler) *Client {
	return &Client{t: t, h: h, headers: http.Header{}}
}

// WithHeader is a copy of c that sends a header on every request: a token, a
// client marker.
func (c *Client) WithHeader(key, value string) *Client {
	h := c.headers.Clone()
	h.Set(key, value)
	return &Client{t: c.t, h: c.h, headers: h}
}

// Get sends a GET.
func (c *Client) Get(target string) *Response { return c.Do(http.MethodGet, target, Body{}) }

// Post sends a POST.
func (c *Client) Post(target string, body Body) *Response {
	return c.Do(http.MethodPost, target, body)
}

// Put sends a PUT.
func (c *Client) Put(target string, body Body) *Response { return c.Do(http.MethodPut, target, body) }

// Patch sends a PATCH.
func (c *Client) Patch(target string, body Body) *Response {
	return c.Do(http.MethodPatch, target, body)
}

// Delete sends a DELETE.
func (c *Client) Delete(target string) *Response { return c.Do(http.MethodDelete, target, Body{}) }

// Do sends any request.
func (c *Client) Do(method, target string, body Body) *Response {
	c.t.Helper()
	var rd io.Reader
	if body.Data != nil {
		rd = bytes.NewReader(body.Data)
	}
	req := httptest.NewRequest(method, target, rd)
	for k, v := range c.headers {
		req.Header[k] = v
	}
	if body.ContentType != "" {
		req.Header.Set("Content-Type", body.ContentType)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return &Response{t: c.t, Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
}

// Response is what the handler answered.
type Response struct {
	t      testing.TB
	Status int
	Header http.Header
	Body   []byte
}

// ExpectStatus fails the test unless the status is want.
func (r *Response) ExpectStatus(want int) *Response {
	r.t.Helper()
	if r.Status != want {
		r.t.Fatalf("gessotest: status %d, want %d; body: %s", r.Status, want, r.Body)
	}
	return r
}

// Problem decodes the answer as a problem, failing the test if it is not one.
func (r *Response) Problem() gesso.Problem {
	r.t.Helper()
	if ct := r.Header.Get("Content-Type"); ct != "application/problem+json" {
		r.t.Fatalf("gessotest: not a problem (content type %q): %s", ct, r.Body)
	}
	var p gesso.Problem
	if err := json.Unmarshal(r.Body, &p); err != nil {
		r.t.Fatalf("gessotest: problem: %v", err)
	}
	return p
}

// ExpectProblem fails the test unless the answer is a problem with status and
// code, and returns it for a closer look at its fields.
func (r *Response) ExpectProblem(status int, code string) gesso.Problem {
	r.t.Helper()
	p := r.Problem()
	if p.Status != status || p.Code != code {
		r.t.Fatalf("gessotest: problem %d %q, want %d %q: %s", p.Status, p.Code, status, code, r.Body)
	}
	return p
}

// Decode decodes the answer's JSON body as T, failing the test if it cannot.
func Decode[T any](r *Response) T {
	r.t.Helper()
	var v T
	if err := json.Unmarshal(r.Body, &v); err != nil {
		r.t.Fatalf("gessotest: decode %T: %v; body: %s", v, err, r.Body)
	}
	return v
}
