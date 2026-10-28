package gesso

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// A typed handler's 200 answer to GET carries an ETag — the hash of the JSON
// it sends, or one the handler set with ResponseHeader — and a GET whose
// If-None-Match names it is answered 304 with no body: the client's copy is
// current.
//
// For an update that must not overwrite a change it has not seen — optimistic
// concurrency — the client sends back, in If-Match, the ETag of what it read.
// The handler loads the resource, and asks IfMatch whether the client saw this
// version:
//
//	note, err := m.svc.Get(ctx, in.ID)
//	…
//	if err := gesso.IfMatch(ctx, gesso.ETagOf(note)); err != nil {
//		return Note{}, err // 412: someone changed it since
//	}
//
// ETagOf hashes v as a GET of it answers, so the two agree.

// ErrPreconditionFailed is an If-Match the resource no longer matches.
var ErrPreconditionFailed = NewError(http.StatusPreconditionFailed, "precondition_failed",
	"the resource changed since it was read: read it again")

// ETagOf is the ETag a GET answering v carries.
func ETagOf[T any](v T) string {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		return ""
	}
	return etag(buf.Bytes())
}

func etag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// IfMatch checks the request's If-Match against current, the resource's ETag
// now: nil when the header is absent, is "*", or names current; else
// ErrPreconditionFailed.
func IfMatch(ctx context.Context, current string) error {
	r := Request(ctx)
	if r == nil {
		return nil
	}
	h := r.Header.Get("If-Match")
	if h == "" || strings.TrimSpace(h) == "*" {
		return nil
	}
	for tag := range strings.SplitSeq(h, ",") {
		tag = strings.TrimSpace(tag)
		if !strings.HasPrefix(tag, "W/") && tag == current { // If-Match compares strongly
			return nil
		}
	}
	return ErrPreconditionFailed
}

// notModified reports whether r's If-None-Match names tag: weakly, as RFC 9110
// has it for GET.
func notModified(r *http.Request, tag string) bool {
	h := r.Header.Get("If-None-Match")
	if h == "" {
		return false
	}
	if strings.TrimSpace(h) == "*" {
		return true
	}
	opaque := strings.TrimPrefix(tag, "W/")
	for t := range strings.SplitSeq(h, ",") {
		if strings.TrimPrefix(strings.TrimSpace(t), "W/") == opaque {
			return true
		}
	}
	return false
}
