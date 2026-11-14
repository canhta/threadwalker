package guard

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// repeatWindow and repeatLimit: a draft whose closing token ends at least
// repeatLimit of the agent's last repeatWindow own actions is a tic, not a voice.
const (
	repeatWindow = 10
	repeatLimit  = 3
)

var laugh = regexp.MustCompile(`^(k{3,}|(ha){2,}|(hi){2,}|(he){2,})$`)

// emoticon is a text face without emoji: ":)", ";-(", "=))", "^_^", "-_-".
var emoticon = regexp.MustCompile(`^(?:[:;=][-^']?[()\[\]/\\|*]+|\^[_.-]?\^|-_-)$`)

// closingToken is the tic a text ends on: the emoji or emoticon it closes with
// (sentence punctuation and plain marks such as ")", a quote, "%" or a dash are
// no token), or its last word when that is a laugh (kkk, haha). Runs of one
// emoji or symbol collapse, so ":)))" and ":))))" are the same token. A text
// that ends in anything else has none.
func closingToken(text string) string {
	runes := []rune(norm.NFC.String(strings.TrimSpace(text)))
	i := len(runes)
	for i > 0 && !unicode.IsLetter(runes[i-1]) && !unicode.IsDigit(runes[i-1]) && !unicode.IsSpace(runes[i-1]) {
		i--
	}
	for i < len(runes) && unicode.IsMark(runes[i]) && !isEmojiExtender(runes[i]) {
		i++ // an accent left on the last letter
	}
	units := graphemes(runes[i:])
	for len(units) > 0 && len(units[len(units)-1]) == 1 && strings.ContainsRune(".,!?;", units[len(units)-1][0]) {
		units = units[:len(units)-1]
	}
	if first := slices.IndexFunc(units, isEmoji); first >= 0 {
		last := len(units) - 1
		for !isEmoji(units[last]) {
			last--
		}
		return collapse(units[first : last+1])
	}
	if token := collapse(units); emoticon.MatchString(token) {
		return token
	}
	fields := strings.Fields(strings.ToLower(string(runes[:i])))
	if len(fields) == 0 {
		return ""
	}
	switch w := fields[len(fields)-1]; {
	case !laugh.MatchString(w):
		return ""
	case w[0] == 'k':
		return "kkk"
	default:
		return strings.Repeat(w[:2], 2)
	}
}

// collapse joins the units, a run of one unit counting once.
func collapse(units [][]rune) string {
	var out []rune
	for i, u := range units {
		if i == 0 || string(u) != string(units[i-1]) {
			out = append(out, u...)
		}
	}
	return string(out)
}

const zwj rune = 0x200d

// isEmojiExtender is a rune that joins the emoji before it: a variation
// selector, skin tone, keycap, tag or the zero-width joiner.
func isEmojiExtender(r rune) bool {
	return r >= 0xFE00 && r <= 0xFE0F || r >= 0x1F3FB && r <= 0x1F3FF || r >= 0xE0020 && r <= 0xE007F || r == 0x20E3 || r == zwj
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// graphemes splits runes into user-perceived symbols: a base rune with the
// extenders after it, a joiner with the rune after it, and a flag's two
// regional indicators.
// Limit: an approximation of UAX #29 for emoji, not a full segmenter; upgrade to a segmenter library if other scripts matter.
func graphemes(runes []rune) [][]rune {
	var units [][]rune
	for _, r := range runes {
		if n := len(units); n > 0 {
			last := units[n-1]
			prev := last[len(last)-1]
			if isEmojiExtender(r) || prev == zwj || isRegionalIndicator(r) && len(last) == 1 && isRegionalIndicator(prev) {
				units[n-1] = append(last, r)
				continue
			}
		}
		units = append(units, []rune{r})
	}
	return units
}

// isEmoji reports whether the unit starts with an emoji or pictograph.
func isEmoji(unit []rune) bool {
	r := unit[0]
	return r >= 0x1F000 || r >= 0x2600 && r <= 0x27BF || r >= 0x2B00 && r <= 0x2BFF || r >= 0x2300 && r <= 0x23FF
}

// overusedEnding returns the draft's closing token when it ends repeatLimit or
// more of the newest repeatWindow texts in recent (newest first), else "".
func overusedEnding(draft string, recent []string) string {
	token := closingToken(draft)
	if token == "" {
		return ""
	}
	n := 0
	for _, r := range recent[:min(len(recent), repeatWindow)] {
		if closingToken(r) == token {
			n++
		}
	}
	if n < repeatLimit {
		return ""
	}
	return token
}

// recentEndings is the distinct closing tokens of the newest repeatWindow texts in recent.
func recentEndings(recent []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, r := range recent[:min(len(recent), repeatWindow)] {
		if t := closingToken(r); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
