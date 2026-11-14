package guard

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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

// Pieces of the engagement-ask patterns. A heart is the word or an emoji.
const (
	heart       = `(?:tim|tym|[❤♥\x{1F493}-\x{1F49F}\x{1F5A4}\x{1F90D}\x{1F90E}\x{1F9E1}\x{1F970}\x{FE0F}]+)`
	me          = `(?:mình|mk|mik|t|tui|tôi)`
	clauseStart = `(?:^|[.,!?;:\n])\s*`
	clauseEnd   = `\s*(?:[.,!?;:\n]|$)`
)

// safetyRule is one hard content rule. A bait rule is also compiled without
// diacritics (plain), for text typed without any.
type safetyRule struct {
	reason  string
	pattern *regexp.Regexp
	plain   *regexp.Regexp
}

func baitRule(src string) safetyRule {
	return safetyRule{reasonBait, regexp.MustCompile(src), regexp.MustCompile(unaccent(src))}
}

// safetyRules match case-folded text in this order. Identity rules target a
// person (a second-person or "đồ"/"con" address), never a topic.
var safetyRules = []safetyRule{
	{reasonIdentity, regexp.MustCompile(`(?i)` + edgeStart + `(?:` +
		`(?:you(?:'re| are)?|your|ur)\s+(?:so\s+|too\s+)?(?:fat|ugly|old|gay|retarded)` +
		`|(?:mày|bạn|ông|bà|anh|chị|con|thằng|đồ)\s+(?:quá\s+|rất\s+)?(?:béo|mập|xấu|già|lùn|đen|bê đê|đồng tính|nhà quê|bắc kỳ|nam kỳ|trung kỳ)` +
		`|(?:ngoại tình|ly hôn|chồng|vợ|người yêu)\s+(?:của\s+)?(?:mày|bạn|ông|bà|anh|chị|nó|hắn)` +
		`)` + edgeEnd), nil},
	{reasonPolitical, regexp.MustCompile(`(?i)` + edgeStart + `(?:vote\s+(?:for|against)|(?:nên|hãy|đừng)\s+(?:bầu|bỏ phiếu)|bầu\s+cho|ủng hộ\s+(?:đảng|ứng viên|ông|bà))` + edgeEnd), nil},
	// Engagement asks are first-person or imperative and aimed at the reader:
	// "follow mình", "nhớ follow nha", "thả tim cho mình", "share giúp mình". The
	// same words in an ordinary sentence ("anh ấy theo dõi em", "mình hay share
	// clip", "tui nhớ follow acc này", "Tim đi lạc một nhịp") are not asks.
	// They are written with diacritics: "hay" is not "hãy" and "tìm" is not "tim".
	baitRule(`(?i)` + edgeStart + `(?:` +
		// Comment, tag, like, save and share.
		`(?:comment|cmt)\s+(?:ngay|\S{1,12}\s+(?:để|if|below|nếu))|tag\s+(?:a\s+|một\s+)?(?:friend|bạn|người|đứa)|like\s+(?:và|&|and)\s+share|like\s+(?:nếu|if)|reply\s+\S{1,6}\s+(?:để|if|to get)|lưu(?:\s+lại)?\s+(?:và|&)\s+(?:share|chia\s+sẻ|repost)` +
		// A heart: "thả tim cho mình", "thả ❤️ nếu đúng", "thì thả tim", "để lại 1 tim", "xin 1 tym", "cho mình 1 tim".
		`|thả\s+` + heart + `\s+(?:cho\s+` + me + `|đi|nhé|nha|giúp|nếu)|(?:thì|hãy|nhớ)\s+thả\s+` + heart + clauseEnd +
		`|để\s+lại\s+(?:một\s+|1\s+)?(?:tim|tym|like|cmt|comment)|xin\s+(?:một\s+|1\s+)?(?:tim|tym)|(?:cho|tặng)\s+` + me + `\s+(?:một\s+|1\s+)?(?:tim|tym)` +
		// A reminder: "nhớ follow nha", "hãy share ngay", "bấm theo dõi"; "follow mình".
		`|(?:nhớ|hãy|đừng\s+quên|bấm|nhấn)\s+(?:follow|theo\s+dõi|share|chia\s+sẻ|repost)(?:\s+(?:me|` + me + `|nha|nhé|nhen|đi|mn|mng|mọi\s+người|ngay|giúp|dùm|hộ)|` + clauseEnd + `)` +
		`|follow\s+(?:me|` + me + `)` +
		`)` + edgeEnd),
	// A command opening a clause: "tim đi mn", "❤️❤️ đi mn", "follow page để xem phần 2",
	// "theo dõi mình nhé", "share giúp mình". After a subject ("mình follow để học") it is not an ask.
	baitRule(`(?i)` + clauseStart + `(?:` +
		heart + `\s+(?:đi|nhé|nha|cho\s+` + me + `)(?:\s+(?:mn|mng|mọi\s+người|đi|nha|nhé|nhen))*` + clauseEnd +
		`|(?:follow|theo\s+dõi)(?:\s+(?:page|kênh|acc|me|` + me + `))?\s+(?:để|nha|nhé|nhen|đi|ngay|giúp|mn|mọi\s+người)` +
		`|(?:share|chia\s+sẻ|repost)(?:\s+(?:giúp|dùm|hộ)\s+(?:` + me + `|em)|\s+ngay\s+cho|\s+(?:đi|nhé|nha))` +
		`)` + edgeEnd),
}

// unaccent drops Vietnamese tone and vowel marks and maps đ to d. It derives the
// plain bait patterns and tells a text without diacritics from one with them.
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
	diacritics := unaccent(folded) != folded
	for _, r := range safetyRules {
		re := r.pattern
		if !diacritics && r.plain != nil {
			re = r.plain
		}
		if loc := re.FindStringIndex(folded); loc != nil {
			// FoldCase maps rune to rune, so the match is cut from the original text by rune offsets.
			from := utf8.RuneCountInString(folded[:loc[0]])
			n := utf8.RuneCountInString(folded[loc[0]:loc[1]])
			return r.reason, strings.TrimSpace(string([]rune(text)[from : from+n]))
		}
	}
	return "", ""
}
