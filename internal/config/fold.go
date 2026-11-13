package config

import (
	"strings"
	"unicode"
)

// FoldCase picks one representative per Unicode simple-fold orbit. Lowercase
// alone misses equivalent forms such as Greek final sigma and Latin long s.
func FoldCase(text string) string {
	return strings.Map(func(r rune) rune {
		canonical := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < canonical {
				canonical = next
			}
		}
		return canonical
	}, text)
}
