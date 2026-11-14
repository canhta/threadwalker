// Package quality scores a drafted post or reply against a persona's rules.
// Code checks are deterministic; the style rules and rejected examples are
// judged by a model whose verdict this package builds the prompt for and parses.
package quality

import (
	"cmp"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/stdx"
)

// Kinds of draft.
const (
	Post  = "post"
	Reply = "reply"
)

// The length of a post: code owns both limits. The guard refuses a post over
// them and the scorer checks them, so the two cannot disagree. A reply's limit
// is the template's guard.reply_max_runes.
const (
	MaxPostRunes = 300
	MaxPostLines = 3
)

// Check ids. Judge checks are StyleRulePrefix plus the 1-based rule number.
const (
	MaxRunes         = "max_runes"
	MaxLines         = "max_lines"
	StyleAvoid       = "style_avoid"
	RepeatImage      = "repeat_image"
	UngroundedNumber = "ungrounded_number"
	BadExample       = "bad_example"
	StyleRulePrefix  = "style_rule_"
)

//go:embed rubric.json
var rubricJSON []byte

// Rubric is the fixed scoring configuration shared by every consumer.
type Rubric struct {
	ImageWords         []string `json:"image_words"`
	JudgeInstructions  string   `json:"judge_instructions"`
	CriticInstructions string   `json:"critic_instructions"`
	WriterInstructions string   `json:"writer_instructions"`
}

// DefaultRubric returns the embedded rubric.
func DefaultRubric() (Rubric, error) {
	var r Rubric
	if err := json.Unmarshal(rubricJSON, &r); err != nil {
		return r, fmt.Errorf("decoding quality rubric: %w", err)
	}
	return r, nil
}

// Lines is the number of non-blank lines of a text: blank-line spacing is free.
func Lines(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// Limit is the most characters a text of a kind may have, and the most lines
// (0 for no limit on lines). A reply's characters are the template's
// guard.reply_max_runes, or the platform cap when that is 0.
func Limit(kind string, replyMaxRunes int) (runes, lines int) {
	if kind == Post {
		return MaxPostRunes, MaxPostLines
	}
	return cmp.Or(replyMaxRunes, stdx.MaxThreadsPostRunes), 0
}

// RubricJSON is the raw rubric, for fingerprinting.
func RubricJSON() []byte { return rubricJSON }

// Verdict is the judge's answer.
type Verdict struct {
	ViolatedRules  []int `json:"violated_rules"`
	LikeBadExample bool  `json:"like_bad_example"`
}

// Check is one rule's outcome.
type Check struct {
	ID   string `json:"id"`
	Pass bool   `json:"pass"`
}

// ParseVerdict decodes the judge's reply, rejecting unknown fields and rule numbers.
func ParseVerdict(content string, rules int) (Verdict, error) {
	var v Verdict
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(content)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return Verdict{}, errors.New("parsing judge verdict: not the expected JSON object")
	}
	for _, n := range v.ViolatedRules {
		if n < 1 || n > rules {
			return Verdict{}, errors.New("parsing judge verdict: rule number out of range")
		}
	}
	return v, nil
}

// JudgeMessages builds the judge prompt from the persona's own style rules and
// rejected examples.
func JudgeMessages(r Rubric, p config.PersonaConfig, kind, text string) []llm.Message {
	var b strings.Builder
	b.WriteString("Style rules:\n")
	for i, s := range p.StyleRules {
		fmt.Fprintf(&b, "%d. %s\n", i+1, s)
	}
	b.WriteString("\nRejected examples:\n")
	for _, s := range p.BadExamples {
		fmt.Fprintf(&b, "- %q\n", s)
	}
	fmt.Fprintf(&b, "\nDraft %s:\n%q\n", kind, text)
	return []llm.Message{{Role: "system", Content: r.JudgeInstructions}, {Role: "user", Content: b.String()}}
}

// Score runs every check. g holds the template's limits the guard enforces:
// the reply length and the units of a self-stat. wake is the wake context's
// user message: its JSON holds the numbers a self-stat may quote. said is what
// the account already said. The result lists code checks, then one check per
// style rule, then bad_example.
func Score(r Rubric, p config.PersonaConfig, g config.GuardConfig, kind, text, wake string, said []string, v Verdict) []Check {
	var out []Check
	add := func(id string, pass bool) { out = append(out, Check{id, pass}) }
	runes, lines := Limit(kind, g.ReplyMaxRunes)
	add(MaxRunes, utf8.RuneCountInString(text) <= runes)
	add(MaxLines, lines == 0 || Lines(text) <= lines)
	add(StyleAvoid, !containsAny(text, p.StyleAvoid))
	var facts []float64
	var tree any
	// The data JSON opens the message; the scoreboard and notes follow as text.
	if json.NewDecoder(strings.NewReader(wake)).Decode(&tree) == nil {
		facts = numbers(tree, nil)
	}
	add(RepeatImage, !repeatsImage(text, said, r.ImageWords))
	add(UngroundedNumber, grounded(text, g.SelfStatUnits, facts))
	violated := map[int]bool{}
	for _, n := range v.ViolatedRules {
		violated[n] = true
	}
	for i := range p.StyleRules {
		add(StyleRulePrefix+strconv.Itoa(i+1), !violated[i+1])
	}
	add(BadExample, !v.LikeBadExample)
	return out
}

func containsAny(text string, phrases []string) bool {
	t := config.FoldCase(text)
	for _, p := range phrases {
		if p != "" && strings.Contains(t, config.FoldCase(p)) {
			return true
		}
	}
	return false
}

func repeatsImage(text string, said, words []string) bool {
	t := config.FoldCase(text)
	for _, w := range words {
		w = config.FoldCase(w)
		if !strings.Contains(t, w) {
			continue
		}
		for _, s := range said {
			if strings.Contains(config.FoldCase(s), w) {
				return true
			}
		}
	}
	return false
}

// numbers collects numeric JSON values, skipping row ids.
func numbers(v any, acc []float64) []float64 {
	switch x := v.(type) {
	case float64:
		acc = append(acc, x)
	case []any:
		for _, e := range x {
			acc = numbers(e, acc)
		}
	case map[string]any:
		for k, e := range x {
			if k != "id" {
				acc = numbers(e, acc)
			}
		}
	}
	return acc
}

var numberRe = regexp.MustCompile(`\d+(?:[.,]\d+)?`)

// grounded reports whether every number written next to a self-stat unit
// equals, at the precision it was written, a number in facts.
func grounded(text string, units []string, facts []float64) bool {
	for _, loc := range numberRe.FindAllStringIndex(text, -1) {
		if !hasUnit(text, loc[0], loc[1], units) {
			continue
		}
		lit := strings.Replace(text[loc[0]:loc[1]], ",", ".", 1)
		val, _ := strconv.ParseFloat(lit, 64)
		decimals := 0
		if i := strings.IndexByte(lit, '.'); i >= 0 {
			decimals = len(lit) - i - 1
		}
		tol := 0.5*math.Pow(10, -float64(decimals)) + 1e-9
		ok := false
		for _, f := range facts {
			if math.Abs(f-val) <= tol {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func hasUnit(text string, start, end int, units []string) bool {
	after := strings.TrimLeftFunc(text[end:], unicode.IsSpace)
	before := strings.TrimRightFunc(text[:start], unicode.IsSpace)
	for _, u := range units {
		u = config.FoldCase(u)
		if u == "$" {
			if strings.HasSuffix(before, "$") || strings.HasPrefix(after, "$") {
				return true
			}
			continue
		}
		if strings.HasPrefix(config.FoldCase(after), u) {
			return true
		}
	}
	return false
}
