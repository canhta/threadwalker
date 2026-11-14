package guard

import (
	"fmt"
	"strings"
	"unicode"
)

// factAnchors are the tokens of a brief's fact the draft must keep at least one
// of: numbers, names (an uppercase first letter, after the first word) and the
// longer words of a quoted line. A fact with none (all lower-case prose) is not
// checked. ponytail: token match without a diacritic fold; add one if a writer
// ever drops marks from a name.
func factAnchors(fact string) []string {
	var anchors []string
	seen := map[string]bool{}
	add := func(t string) {
		if t = strings.ToLower(t); t != "" && !seen[t] {
			seen[t] = true
			anchors = append(anchors, t)
		}
	}
	for i, t := range splitTokens(fact) {
		first := []rune(t)[0]
		if strings.IndexFunc(t, unicode.IsDigit) >= 0 || (i > 0 && unicode.IsUpper(first)) {
			add(t)
		}
	}
	for _, q := range quotedSpans(fact) {
		for _, t := range splitTokens(q) {
			if len([]rune(t)) >= 4 {
				add(t)
			}
		}
	}
	return anchors
}

func splitTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// quotedSpans returns the text between “ and ” or between two straight quotes.
func quotedSpans(s string) []string {
	var out []string
	var cur []rune
	open := false
	for _, r := range s {
		switch {
		case !open && (r == '“' || r == '"'):
			open, cur = true, nil
		case open && (r == '”' || r == '"'):
			open = false
			out = append(out, string(cur))
		case open:
			cur = append(cur, r)
		}
	}
	return out
}

// factProblem returns a revision problem when none of the fact's anchors
// survives in the draft's texts, else "". It is deterministic and runs before
// the critic, so a writer that swaps the brief's concrete detail for a vague
// feeling goes back to the writer.
func factProblem(fact string, texts []string) string {
	anchors := factAnchors(fact)
	if len(anchors) == 0 {
		return ""
	}
	have := map[string]bool{}
	for _, t := range texts {
		for _, tok := range splitTokens(strings.ToLower(t)) {
			have[tok] = true
		}
	}
	for _, a := range anchors {
		if have[a] {
			return ""
		}
	}
	if len(anchors) > 4 {
		anchors = anchors[:4]
	}
	return fmt.Sprintf("the draft dropped the brief's concrete detail; keep at least one of %q from the fact", strings.Join(anchors, `", "`))
}
