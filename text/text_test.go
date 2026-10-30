package text

import "testing"

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Çay Saati!":          "cay-saati",
		"  İstanbul'da 2026 ": "istanbul-da-2026",
		"--a--b--":            "a-b",
		"Ünlü Şarkı Ğ":        "unlu-sarki-g",
		"日本 go":               "go",
		"":                    "",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Collapse("  a \t b\n\nc  "); got != "a b c" {
		t.Errorf("Collapse: %q", got)
	}
}
