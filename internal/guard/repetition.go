package guard

import (
	"regexp"
	"strings"
	"unicode"
)

// repeatWindow and repeatLimit: a draft whose closing token ends at least
// repeatLimit of the agent's last repeatWindow own actions is a tic, not a voice.
const (
	repeatWindow = 10
	repeatLimit  = 3
)

var laugh = regexp.MustCompile(`^(k{3,}|(ha){2,}|(hi){2,}|(he){2,})$`)

// closingToken is the trailing run of symbols (an emoticon like ":)))" or an
// emoji, sentence punctuation left out) of a text, or its last word when that
// is a laugh (kkk, haha). Runs of one symbol collapse, so ":)))" and ":))))"
// are the same token. A text that ends in anything else has none.
func closingToken(text string) string {
	runes := []rune(strings.TrimSpace(text))
	i := len(runes)
	for i > 0 && !unicode.IsLetter(runes[i-1]) && !unicode.IsDigit(runes[i-1]) && !unicode.IsSpace(runes[i-1]) {
		i--
	}
	if run := strings.TrimRight(string(runes[i:]), ".,!?;"); run != "" {
		var out []rune
		for _, r := range run {
			if len(out) == 0 || out[len(out)-1] != r {
				out = append(out, r)
			}
		}
		return string(out)
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
