package gesso

import (
	"encoding/base64"
	"encoding/json"
)

// Page is one page of a list, and the cursor of the next: Next is empty on the
// last page.
//
//	type ListIn struct {
//		After string `query:"after"`
//		Take  int    `query:"take" default:"20" validate:"min=1,max=100"`
//	}
//
// A cursor names a position — the last item's key — not an offset, so a page
// is as fast deep in a table as at its start, and rows added meanwhile do not
// shift it.
type Page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
}

// ErrInvalidCursor is a cursor that does not decode.
var ErrInvalidCursor = BadRequest("invalid_cursor", "the cursor is not one this API gave")

// EncodeCursor makes the cursor of a position: key as JSON, base64url. It is
// opaque to clients — they hand it back as they got it — but not secret: put
// no more in it than the position.
func EncodeCursor[T any](key T) string {
	data, err := json.Marshal(key)
	if err != nil {
		panic("gesso: EncodeCursor: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeCursor reads a cursor EncodeCursor made. The empty cursor is the zero
// key: the first page. Anything else that does not decode is
// ErrInvalidCursor.
func DecodeCursor[T any](cursor string) (T, error) {
	var key T
	if cursor == "" {
		return key, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(data) > 1024 {
		return key, ErrInvalidCursor
	}
	if err := json.Unmarshal(data, &key); err != nil {
		return key, ErrInvalidCursor
	}
	return key, nil
}
