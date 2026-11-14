package guard

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

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
// person (a second-person or "đồ"/"con" address), never a topic. A rule with
// plain set matches the text without diacritics (see unaccent), so its pattern
// is spelled without them and also catches "tha tim cho minh".
var safetyRules = []struct {
	reason  string
	plain   bool
	pattern *regexp.Regexp
}{
	{reasonIdentity, false, regexp.MustCompile(`(?i)` + edgeStart + `(?:` +
		`(?:you(?:'re| are)?|your|ur)\s+(?:so\s+|too\s+)?(?:fat|ugly|old|gay|retarded)` +
		`|(?:mày|bạn|ông|bà|anh|chị|con|thằng|đồ)\s+(?:quá\s+|rất\s+)?(?:béo|mập|xấu|già|lùn|đen|bê đê|đồng tính|nhà quê|bắc kỳ|nam kỳ|trung kỳ)` +
		`|(?:ngoại tình|ly hôn|chồng|vợ|người yêu)\s+(?:của\s+)?(?:mày|bạn|ông|bà|anh|chị|nó|hắn)` +
		`)` + edgeEnd)},
	{reasonPolitical, false, regexp.MustCompile(`(?i)` + edgeStart + `(?:vote\s+(?:for|against)|(?:nên|hãy|đừng)\s+(?:bầu|bỏ phiếu)|bầu\s+cho|ủng hộ\s+(?:đảng|ứng viên|ông|bà))` + edgeEnd)},
	// Engagement asks are first-person or imperative and aimed at the reader:
	// "follow mình", "nhớ follow", "thả tim cho mình", "share giúp mình". The
	// same words in an ordinary sentence ("anh ấy theo dõi em", "chia sẻ cho
	// fan", "đau tim nha mn") are not asks.
	{reasonBait, true, regexp.MustCompile(`(?i)` + edgeStart + `(?:` +
		// Comment, tag, like and share.
		`comment\s+(?:ngay|\S{1,12}\s+(?:de|if|below|neu))|cmt\s+(?:ngay|\S+\s+de)|tag\s+(?:a\s+|mot\s+)?(?:friend|ban|nguoi)|like\s+(?:va|&|and)\s+share|reply\s+\S{1,6}\s+(?:de|if|to get)` +
		// A heart: "thả tim cho mình", "để lại 1 tim", "xin 1 tym", "cho mình 1 tim".
		`|tha\s+(?:tim|tym)\s+(?:cho\s+(?:minh|mk|mik|t|tui|toi)|di|nhe|nha|giup)|de\s+lai\s+(?:mot\s+|1\s+)?(?:tim|tym|like|cmt|comment)|xin\s+(?:mot\s+|1\s+)?(?:tim|tym)|(?:cho|tang)\s+(?:minh|mk|mik|t|tui|toi)\s+(?:mot\s+|1\s+)?(?:tim|tym)` +
		// Follow: "nhớ follow", "follow mình/mk/t", "follow để xem phần 2", "theo dõi mình để xem tiếp".
		`|(?:nho|hay|dung\s+quen|bam|nhan)\s+(?:follow|theo\s+doi)|follow\s+(?:me|minh|mk|mik|t|tui|toi|de|nha|nhe|di|ngay|giup|mn|moi\s+nguoi)|theo\s+doi\s+(?:minh|mk|mik|tui|toi)\s+(?:de|nha|nhe|di|ngay|giup)` +
		// Share and repost: "share/chia sẻ/repost giúp mình", "share ngay cho bạn bè", "nhớ share".
		`|(?:share|chia\s+se|repost)\s+(?:giup|dum|ho)\s+(?:minh|mk|mik|t|tui|toi|em)|share\s+(?:ngay\s+cho|di|nhe|nha|cho\s+(?:ban|bn|mn|moi\s+nguoi))|(?:nho|hay)\s+(?:share|chia\s+se|repost)` +
		`)` + edgeEnd)},
	// A heart opening a clause is a command: "tim đi mn", "❤️ đi mn".
	{reasonBait, true, regexp.MustCompile(`(?i)(?:^|[.,!?;:\n])\s*(?:tim|tym|[❤♥\x{1F493}-\x{1F49F}\x{1F5A4}\x{1F90D}\x{1F90E}\x{1F9E1}])\s+(?:di|nhe|nha|cho\s+(?:minh|mk|mik|t|tui|toi))` + edgeEnd)},
}

// unaccent drops Vietnamese tone and vowel marks and maps đ to d, so a text
// typed without diacritics matches the same rule.
func unaccent(text string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(text) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case r == 'đ' || r == 'Đ':
			b.WriteByte('d')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
	plain := unaccent(folded)
	for _, r := range safetyRules {
		text := folded
		if r.plain {
			text = plain
		}
		if m := r.pattern.FindString(text); m != "" {
			return r.reason, strings.TrimSpace(m)
		}
	}
	return "", ""
}
