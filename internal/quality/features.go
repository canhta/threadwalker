package quality

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/config"
)

const ngramWords = 3

// FeatureContext is what the code features of one action are measured against.
// Parent is Threads data and is only compared, never executed or followed.
type FeatureContext struct {
	TrendTitles    []string
	Parent         string
	BlockedPhrases []string
	OwnHistory     []string
}

// Features are the code-computed content figures of one action.
type Features struct {
	Runes          int  `json:"runes"`
	Lines          int  `json:"lines"`
	OpensWithTrend bool `json:"opens_with_trend"`
	QuotesParent   bool `json:"quotes_parent"`
	Question       bool `json:"question"`
	Numbers        bool `json:"numbers"`
	NearMisses     int  `json:"forbidden_near_misses"`
	RepeatedNGrams int  `json:"repeated_ngrams"`
}

// ExtractFeatures computes the features of text. It opens with the trend when
// its first line shares two topic tokens with a trend title; it quotes the
// parent when they share a run of three words; a blocked phrase is a near miss
// when all but one of its words (two or more) appear and the phrase does not;
// repetition counts the three-word runs it shares with the agent's own history.
func ExtractFeatures(text string, c FeatureContext) Features {
	text = strings.TrimSpace(text)
	f := Features{Runes: utf8.RuneCountInString(text), Lines: len(strings.Split(text, "\n")), Question: strings.ContainsAny(text, "?？"), Numbers: numberRe.MatchString(text)}
	first, _, _ := strings.Cut(text, "\n")
	opening := config.TopicTokens(first)
	for _, title := range c.TrendTitles {
		if shared(opening, config.TopicTokens(title)) >= 2 {
			f.OpensWithTrend = true
			break
		}
	}
	grams := ngrams(text)
	f.QuotesParent = overlap(grams, ngrams(c.Parent)) > 0
	own := map[string]bool{}
	for _, h := range c.OwnHistory {
		for g := range ngrams(h) {
			own[g] = true
		}
	}
	f.RepeatedNGrams = overlap(grams, own)
	folded := config.FoldCase(text)
	have := map[string]bool{}
	for _, w := range words(text) {
		have[w] = true
	}
	for _, p := range c.BlockedPhrases {
		pw := words(p)
		if len(pw) < 2 || strings.Contains(folded, config.FoldCase(p)) {
			continue
		}
		hits := 0
		for _, w := range pw {
			if have[w] {
				hits++
			}
		}
		if hits >= len(pw)-1 {
			f.NearMisses++
		}
	}
	return f
}

func words(s string) []string {
	return strings.FieldsFunc(config.FoldCase(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

func ngrams(s string) map[string]bool {
	w := words(s)
	out := map[string]bool{}
	for i := 0; i+ngramWords <= len(w); i++ {
		out[strings.Join(w[i:i+ngramWords], " ")] = true
	}
	return out
}

func overlap(a, b map[string]bool) int {
	n := 0
	for g := range a {
		if b[g] {
			n++
		}
	}
	return n
}

func shared(a, b []string) int {
	set := map[string]bool{}
	for _, t := range b {
		set[t] = true
	}
	n := 0
	for _, t := range a {
		if set[t] {
			n++
		}
	}
	return n
}
