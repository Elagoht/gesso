// Package text formats what a client typed, after it was checked: the
// transforms of a model's fields (trim, collapse, slugify …) are these.
package text

import (
	"strings"
	"unicode"
)

// Collapse trims s and turns every run of white space in it into one space.
func Collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// folds are the letters Slugify writes in ASCII.
var folds = map[rune]string{
	'ç': "c", 'ğ': "g", 'ı': "i", 'ö': "o", 'ş': "s", 'ü': "u", 'İ': "i",
	'à': "a", 'á': "a", 'â': "a", 'ä': "a", 'ã': "a", 'å': "a", 'æ': "ae",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ì': "i", 'í': "i", 'î': "i", 'ï': "i",
	'ñ': "n", 'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ø': "o", 'œ': "oe",
	'ù': "u", 'ú': "u", 'û': "u", 'ß': "ss", 'ý': "y", 'ÿ': "y",
}

// Slugify makes s a slug — lower-case ASCII letters and digits joined by
// single hyphens — "Çay Saati!" is "cay-saati". Letters it cannot write in
// ASCII are dropped.
func Slugify(s string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(r)
		case folds[r] != "":
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteString(folds[r])
		case unicode.IsLetter(r) || unicode.IsDigit(r):
		default:
			hyphen = true
		}
	}
	return b.String()
}
