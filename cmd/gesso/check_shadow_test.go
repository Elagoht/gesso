package main

import "testing"

func TestShadows(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"/docs/order", "/docs/{slug}", true},
		{"/docs/{slug}/cover", "/docs/{slug}", false},
		{"/docs", "/docs/{slug}", false},
		{"/docs/{slug}", "/docs/order", false},
		{"/auth/me", "/auth/{x}", true},
		{"/docs/{a}/x", "/docs/{b}/{c}", true},
		{"/docs/a", "/notes/{b}", false},
	} {
		if got := shadows(c.a, c.b); got != c.want {
			t.Errorf("shadows(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
