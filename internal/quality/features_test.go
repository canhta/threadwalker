package quality

import (
	"reflect"
	"testing"
)

func TestExtractFeatures(t *testing.T) {
	cases := []struct {
		name string
		text string
		ctx  FeatureContext
		want Features
	}{
		{"plain", "tôi đúng\ncon vợ sai", FeatureContext{}, Features{Runes: 19, Lines: 2}},
		{"question and number", "gpt 5 có thật không?", FeatureContext{}, Features{Runes: 20, Lines: 1, Question: true, Numbers: true}},
		{"opens with the trend", "Nvidia GPU supply tight lol\nrest", FeatureContext{TrendTitles: []string{"Jensen Huang: Nvidia GPU supply tight"}}, Features{Runes: 32, Lines: 2, OpensWithTrend: true}},
		{"trend only on a later line", "hello\nNvidia GPU supply tight", FeatureContext{TrendTitles: []string{"Nvidia GPU supply tight"}}, Features{Runes: 29, Lines: 2}},
		{"quotes the parent", "bảo là prompt đủ rồi á", FeatureContext{Parent: "chỉ cần prompt đủ rồi mà"}, Features{Runes: 22, Lines: 1, QuotesParent: true}},
		{"two shared words are not a quote", "prompt đủ thôi", FeatureContext{Parent: "chỉ cần prompt đủ rồi"}, Features{Runes: 14, Lines: 1}},
		{"forbidden near miss", "kèo này chắc ăn thắng", FeatureContext{BlockedPhrases: []string{"kèo chắc thắng"}}, Features{Runes: 21, Lines: 1, NearMisses: 1}},
		{"forbidden phrase itself is not a near miss", "kèo chắc thắng", FeatureContext{BlockedPhrases: []string{"kèo chắc thắng"}}, Features{Runes: 14, Lines: 1}},
		{"one-word phrase is never a near miss", "kèo", FeatureContext{BlockedPhrases: []string{"kèo"}}, Features{Runes: 3, Lines: 1}},
		{"repeats own history", "tôi nhìn màn hình là code sợ", FeatureContext{OwnHistory: []string{"hôm qua tôi nhìn màn hình là xong"}}, Features{Runes: 28, Lines: 1, RepeatedNGrams: 3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExtractFeatures(c.text, c.ctx); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestParseScores(t *testing.T) {
	ok := `{"hook_strength":4,"specificity":3,"preachiness":5,"slop_free":2,"persona_fit":4,"target_fit":1}`
	if s, err := ParseScores(ok); err != nil || s.HookStrength != 4 || s.TargetFit != 1 {
		t.Fatalf("scores = %+v, err = %v", s, err)
	}
	for _, bad := range []string{`nope`, `{"hook_strength":6,"specificity":3,"preachiness":5,"slop_free":2,"persona_fit":4,"target_fit":1}`, `{"hook_strength":0}`, `{"extra":1}`} {
		if _, err := ParseScores(bad); err == nil {
			t.Errorf("ParseScores(%q) accepted", bad)
		}
	}
}
