package config

import (
	"strings"
	"unicode"
)

// TrendKeywords returns the deterministic keywords used to rank fetch_trends
// results. Explicit template lists win; otherwise keywords are derived from the
// persona's topics/research focus and avoid_topics. Derivation is cheap, stable
// and needs no model call.
func (t Template) TrendKeywords() (include, avoid []string) {
	include = normalizeKeywords(t.Trends.IncludeKeywords)
	avoid = normalizeKeywords(t.Trends.AvoidKeywords)
	if len(include) == 0 {
		include = deriveKeywords(append(append([]string{}, t.Persona.Topics...), t.Persona.ResearchFocus...))
	}
	if len(avoid) == 0 {
		avoid = deriveKeywords(t.Persona.AvoidTopics)
	}
	return include, avoid
}

func normalizeKeywords(keywords []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, keyword := range keywords {
		normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(keyword))), " ")
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out
}

// trendStopwords drops function words so derived keywords favor topics. The
// list mixes English and Vietnamese; it is intentionally small and fixed so the
// ranking stays reproducible across runs and hosts.
var trendStopwords = func() map[string]bool {
	words := `a an and are as at be been but by can could did do does for from had has have he her his how i if in into is it its just me more most my no not of on one only or other our out over she should so some than that the their them then there these they this those to two up very was we were what when where which who why will with would you your
	và của là có cho với một những các cái này đó để trong khi thì mà ra vào lên xuống hay hoặc như thế rồi vẫn rất quá đang bị được từ tới về nhưng nếu vì nên hơn nhất chỉ cũng đã sẽ không gì đâu đấy ấy ta mày nó họ mình bạn tôi chúng nào sao thật luôn cả đủ khá hơi cực con còn làm vậy thôi nhé nha đây kia`
	set := map[string]bool{}
	for _, word := range strings.Fields(words) {
		set[word] = true
	}
	return set
}()

func deriveKeywords(texts []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, text := range texts {
		for _, token := range trendTokens(text) {
			if len([]rune(token)) < 2 || seen[token] || trendStopwords[token] {
				continue
			}
			seen[token] = true
			out = append(out, token)
			if len(out) == 48 {
				return out
			}
		}
	}
	return out
}

func trendTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// TopicTokens exposes deterministic, stopword-filtered tokens for trend
// analysis. It uses the same tokenizer and stopwords as keyword derivation.
func TopicTokens(text string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, token := range trendTokens(text) {
		if len([]rune(token)) < 2 || seen[token] || trendStopwords[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}
