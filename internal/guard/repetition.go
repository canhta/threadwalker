package guard

import (
	"fmt"
	"strings"
	"unicode"
)

// repeatWindow and repeatLimit: a draft whose closing token ends at least
// repeatLimit of the agent's last repeatWindow own actions is a tic, not a voice.
const (
	repeatWindow = 10
	repeatLimit  = 3
)

// closingToken is the trailing run of symbols and punctuation of a text (an
// emoticon like ":)))" or an emoji), or its last two words when it ends in
// letters or in plain sentence punctuation. Case is folded.
func closingToken(text string) string {
	runes := []rune(strings.TrimSpace(text))
	end := len(runes)
	i := end
	for i > 0 && !unicode.IsLetter(runes[i-1]) && !unicode.IsDigit(runes[i-1]) && !unicode.IsSpace(runes[i-1]) {
		i--
	}
	if run := string(runes[i:end]); strings.Trim(run, ".,!?;") != "" {
		return run
	}
	words := strings.Fields(strings.ToLower(strings.TrimRight(string(runes[:i]), " \t\n")))
	if len(words) > 2 {
		words = words[len(words)-2:]
	}
	return strings.Join(words, " ")
}

// repetitionProblem returns a revision problem when the draft's closing token
// ends repeatLimit or more of the newest repeatWindow texts in recent (newest
// first), else "". It is deterministic and runs before the critic.
func repetitionProblem(draft string, recent []string) string {
	token := closingToken(draft)
	if token == "" {
		return ""
	}
	if len(recent) > repeatWindow {
		recent = recent[:repeatWindow]
	}
	n := 0
	for _, r := range recent {
		if closingToken(r) == token {
			n++
		}
	}
	if n < repeatLimit {
		return ""
	}
	return fmt.Sprintf("the ending %q closes %d of your last %d texts; end this one a different way", token, n, len(recent))
}
