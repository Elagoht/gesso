package gesso

import (
	"errors"
	"testing"
	"time"
)

func TestCursor(t *testing.T) {
	type key struct {
		At time.Time `json:"at"`
		ID int64     `json:"id"`
	}
	k := key{At: time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC), ID: 42}
	c := EncodeCursor(k)
	back, err := DecodeCursor[key](c)
	if err != nil || back != k {
		t.Fatalf("%v %v", back, err)
	}
	if zero, err := DecodeCursor[int64](""); err != nil || zero != 0 {
		t.Fatalf("the empty cursor is the start: %v %v", zero, err)
	}
	for _, bad := range []string{"!!!", EncodeCursor("a string"), "e30"} {
		if _, err := DecodeCursor[int64](bad); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
