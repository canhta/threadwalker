package guard

import "testing"

func TestClosingToken(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"ellipsis", "để xem sao…", ""},
		{"paren", "(theo mình thấy)", ""},
		{"quote", `anh ấy bảo "được"`, ""},
		{"percent", "tăng 50%", ""},
		{"dash", "giá vé –", ""},
		{"sentence punctuation", "hay quá!!!", ""},
		{"emoticon run collapses", "hay quá :)))", ":)"},
		{"emoticon lengths agree", "hay quá :))))", ":)"},
		{"emoji", "thương quá 😭", "😭"},
		{"emoji then punctuation", "thương quá 😭.", "😭"},
		{"emoji in parens", "thương quá (😭)", "😭"},
		{"emoji run collapses", "thương quá 😭😭😭", "😭"},
		{"variation selector run", "yêu ❤️❤️", "❤️"},
		{"skin tone run", "ok 👍🏽👍🏽", "👍🏽"},
		{"zwj sequence run", "nhà mình 👨‍👩‍👧👨‍👩‍👧", "👨‍👩‍👧"},
		{"flag run", "việt nam 🇻🇳🇻🇳", "🇻🇳"},
		{"different skin tones differ", "ok 👍🏽👍🏿", "👍🏽👍🏿"},
		{"laugh", "hay quá kkkk", "kkk"},
		{"decomposed accent is a letter", "hay quá", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := closingToken(tc.text); got != tc.want {
				t.Fatalf("closingToken(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
