package gesso

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type requestIDKey struct{}

// RequestIDFrom is the request's id: the X-Request-ID gesso answered it with,
// and logged its failure under.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestID is the id to serve r under: the X-Request-ID it came with when the
// app trusts it (a proxy in front sets it), else a new one.
func (a *App) requestID(r *http.Request) string {
	if a.cfg.TrustRequestID {
		if id := r.Header.Get("X-Request-ID"); validRequestID(id) {
			return id
		}
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// validRequestID keeps what is written into logs and headers to a short run of
// safe characters.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}

// recorded is a ResponseWriter that remembers its status and size, for the
// access log.
type recorded struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *recorded) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *recorded) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the connection's own writer.
func (w *recorded) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (a *App) logRequest(r *http.Request, w *recorded, start time.Time) {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	a.cfg.Logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
		slog.String("method", r.Method), slog.String("path", r.URL.Path),
		slog.Int("status", status), slog.Int64("bytes", w.bytes),
		slog.Duration("duration", time.Since(start)),
		slog.String("request_id", RequestIDFrom(r.Context())))
}
