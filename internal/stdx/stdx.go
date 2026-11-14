// Package stdx holds the small helpers the standard library lacks and several packages share.
package stdx

import (
	"context"
	"math"
	"time"
	"unicode/utf8"
)

// Sleep waits d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// FiniteNonNegative reports whether n is a number that is not negative, NaN or infinite.
func FiniteNonNegative(n float64) bool { return n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n) }

// TruncateUTF8 cuts s to at most limit bytes without splitting a rune and reports whether it cut.
func TruncateUTF8(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}

// CapRunes cuts s to at most n runes.
func CapRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
