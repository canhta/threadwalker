package guard

import (
	"regexp"
	"strings"

	"github.com/canhta/threadz/internal/config"
)

// Reason codes for the built-in content categories. The category rules are code
// and cannot be changed by a template or by the agent.
const (
	reasonBlockedPhrase = "blocked_phrase"
	reasonIdentity      = "identity_targeting"
	reasonPolitical     = "political_recommendation"
	reasonBait          = "engagement_bait"
)

// Go's \b is ASCII-only, so word edges are spelled out for Vietnamese text.
const (
	edgeStart = `(?:^|[^\p{L}\p{N}])`
	edgeEnd   = `(?:[^\p{L}\p{N}]|$)`
)

// safetyRules match case-folded text in this order. Identity rules target a
// person (a second-person or "đồ"/"con" address), never a topic.
var safetyRules = []struct {
	reason  string
	pattern *regexp.Regexp
}{
	{reasonIdentity, regexp.MustCompile(`(?i)` + edgeStart + `(?:` +
		`(?:you(?:'re| are)?|your|ur)\s+(?:so\s+|too\s+)?(?:fat|ugly|old|gay|retarded)` +
		`|(?:mày|bạn|ông|bà|anh|chị|con|thằng|đồ)\s+(?:quá\s+|rất\s+)?(?:béo|mập|xấu|già|lùn|đen|bê đê|đồng tính|nhà quê|bắc kỳ|nam kỳ|trung kỳ)` +
		`|(?:ngoại tình|ly hôn|chồng|vợ|người yêu)\s+(?:của\s+)?(?:mày|bạn|ông|bà|anh|chị|nó|hắn)` +
		`)` + edgeEnd)},
	{reasonPolitical, regexp.MustCompile(`(?i)` + edgeStart + `(?:vote\s+(?:for|against)|(?:nên|hãy|đừng)\s+(?:bầu|bỏ phiếu)|bầu\s+cho|ủng hộ\s+(?:đảng|ứng viên|ông|bà))` + edgeEnd)},
	{reasonBait, regexp.MustCompile(`(?i)` + edgeStart + `(?:comment\s+(?:ngay|\S{1,12}\s+(?:để|if|below|nếu))|cmt\s+(?:ngay|\S+\s+để)|tag\s+(?:a\s+|một\s+)?(?:friend|bạn|người)|like\s+(?:và|&|and)\s+share|share\s+ngay|follow\s+(?:me|mình|tôi)|reply\s+\S{1,6}\s+(?:để|if|to get))` + edgeEnd)},
}

// contentBlock returns the reason code of the first hard rule the text breaks
// and the phrase or matched words that broke it.
func contentBlock(blocked []string, text string) (reason, detail string) {
	folded := config.FoldCase(text)
	for _, phrase := range blocked {
		if phrase != "" && strings.Contains(folded, config.FoldCase(phrase)) {
			return reasonBlockedPhrase, phrase
		}
	}
	for _, r := range safetyRules {
		if m := r.pattern.FindString(folded); m != "" {
			return r.reason, strings.TrimSpace(m)
		}
	}
	return "", ""
}
