package quality

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

const wake = `{"remaining_usd":1.5,"counters":{"posts_today":2,"window_hours":24},"recent":[{"id":9}]}

The scoreboard follows the JSON as text.`

var said = []string{"tôi nhìn màn hình là code sợ"}

func persona() config.PersonaConfig {
	return config.PersonaConfig{
		StyleRules:  []string{"rule one", "rule two"},
		BadExamples: []string{"bad one"},
		StyleAvoid:  []string{"Tuy nhiên,"},
	}
}

// guardLimits are the template limits the scorer reads: a 120 character reply
// and the units after which a number is a claim about the account.
func guardLimits() config.GuardConfig {
	return config.GuardConfig{ReplyMaxRunes: 120, SelfStatUnits: []string{"$", "cent", "đô", "view", "follower", "reply", "token"}}
}

func failed(checks []Check) string {
	var ids []string
	for _, c := range checks {
		if !c.Pass {
			ids = append(ids, c.ID)
		}
	}
	return strings.Join(ids, ",")
}

func TestScore(t *testing.T) {
	r, err := DefaultRubric()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, kind, text string
		verdict          Verdict
		want             string
	}{
		{"clean post", Post, "gớm, build xong trong 4 phút.", Verdict{}, ""},
		{"reply over limit", Reply, strings.Repeat("a", 121), Verdict{}, MaxRunes},
		{"reply at limit", Reply, strings.Repeat("a", 120), Verdict{}, ""},
		{"post over limit", Post, strings.Repeat("a", MaxPostRunes+1), Verdict{}, MaxRunes},
		{"post at limit", Post, strings.Repeat("a", MaxPostRunes), Verdict{}, ""},
		{"post too many lines", Post, "a\nb\nc\nd", Verdict{}, MaxLines},
		{"reply lines unchecked", Reply, "a\nb\nc\nd", Verdict{}, ""},
		{"style avoid folded", Post, "TUY NHIÊN, không", Verdict{}, StyleAvoid},
		{"repeated image", Post, "tôi hay nhìn màn hình lắm", Verdict{}, RepeatImage},
		{"new image", Post, "tôi nhìn cái quạt", Verdict{}, ""},
		{"stat matches fact", Post, "còn 1.5 đô", Verdict{}, ""},
		{"stat with dollar sign", Post, "còn $1.5", Verdict{}, ""},
		{"stat rounds to written precision", Post, "còn 2 đô", Verdict{}, ""},
		{"comma decimal", Post, "còn 1,5 đô", Verdict{}, ""},
		{"stat invented", Post, "xài 8 cent", Verdict{}, UngroundedNumber},
		{"decimal too precise", Post, "còn 1.6 đô", Verdict{}, UngroundedNumber},
		{"id is not a fact", Post, "9 view", Verdict{}, UngroundedNumber},
		{"joke number without unit", Post, "ship trong 6 tháng", Verdict{}, ""},
		{"judge rule", Post, "ok", Verdict{ViolatedRules: []int{2}}, StyleRulePrefix + "2"},
		{"judge bad example", Post, "ok", Verdict{LikeBadExample: true}, BadExample},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failed(Score(r, persona(), guardLimits(), tt.kind, tt.text, wake, said, tt.verdict)); got != tt.want {
				t.Fatalf("failed checks = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScoreToleratesMalformedWake(t *testing.T) {
	r, _ := DefaultRubric()
	if got := failed(Score(r, persona(), guardLimits(), Post, "9 view", "not json", nil, Verdict{})); got != UngroundedNumber {
		t.Fatalf("failed = %q", got)
	}
}

func TestParseVerdict(t *testing.T) {
	v, err := ParseVerdict(` {"violated_rules":[1,2],"like_bad_example":true} `, 2)
	if err != nil || len(v.ViolatedRules) != 2 || !v.LikeBadExample {
		t.Fatalf("v=%+v err=%v", v, err)
	}
	for _, bad := range []string{"", "yes", `{"violated_rules":[3]}`, `{"violated_rules":[0]}`, `{"extra":1}`} {
		if _, err := ParseVerdict(bad, 2); err == nil {
			t.Errorf("ParseVerdict(%q) accepted", bad)
		}
	}
}

func TestJudgeMessagesCarryRulesAndExamples(t *testing.T) {
	r, _ := DefaultRubric()
	m := JudgeMessages(r, persona(), Reply, "the draft")
	if m[0].Role != "system" || m[0].Content != r.JudgeInstructions {
		t.Fatalf("system=%+v", m[0])
	}
	for _, want := range []string{"1. rule one", "2. rule two", `"bad one"`, `"the draft"`, "reply"} {
		if !strings.Contains(m[1].Content, want) {
			t.Errorf("judge prompt lacks %q", want)
		}
	}
}
