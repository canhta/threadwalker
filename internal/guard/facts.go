package guard

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

// Fact is one measured value of the agent's own account.
type Fact struct {
	ID    string  `json:"id"`
	Value float64 `json:"value"`
}

const factMetricSamples = 20

// FactSheet is the one list of numbers an agent may quote about itself: the
// wake context shows it and the guard checks post and reply text against it.
func FactSheet(ctx context.Context, st *store.Store, t config.Template, now time.Time) ([]Fact, error) {
	u := now.UTC()
	dayStart := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	spent, err := st.LLMUsageForDayPhases(ctx, t.ID, dayStart.Format("2006-01-02"), "agent", "study")
	if err != nil {
		return nil, fmt.Errorf("building fact sheet: %w", err)
	}
	today, err := st.CountActions(ctx, t.ID, t.DryRun, dayStart, dayStart.Add(24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("building fact sheet: %w", err)
	}
	samples, err := st.RecentMetricSamples(ctx, t.ID, factMetricSamples)
	if err != nil {
		return nil, fmt.Errorf("building fact sheet: %w", err)
	}
	facts := []Fact{
		{"usd_spent_today", spent.CostUSD},
		{"usd_left_today", max(t.Limits.MaxUSDPerDay-spent.CostUSD, 0)},
		{"tokens_today", float64(spent.TokensIn + spent.TokensOut)},
		{"posts_today", float64(today.Posts)},
		{"replies_today", float64(today.Replies)},
	}
	if followers, ok, err := st.LatestFollowers(ctx, t.ID); err != nil {
		return nil, fmt.Errorf("building fact sheet: %w", err)
	} else if ok {
		facts = append(facts, Fact{"followers", float64(followers)})
	}
	seen := map[int64]bool{}
	for _, s := range samples {
		if !seen[s.ActionID] {
			seen[s.ActionID] = true
			id := "action_" + strconv.FormatInt(s.ActionID, 10)
			facts = append(facts, Fact{id + "_views", float64(s.Views)}, Fact{id + "_replies", float64(s.Replies)})
		}
	}
	return facts, nil
}

// statPattern matches a number next to one of the template's self-stat units:
// "8 cent", "3 view", "$0.08".
func statPattern(units []string) *regexp.Regexp {
	var suffix, prefix []string
	for _, u := range units {
		q := regexp.QuoteMeta(strings.TrimSpace(u))
		suffix = append(suffix, q)
		if !strings.ContainsFunc(u, func(r rune) bool { return r > 127 || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) {
			prefix = append(prefix, q)
		}
	}
	const num = `(\d+(?:[.,]\d+)?)`
	p := `(?i)` + num + `\s*(?:` + strings.Join(suffix, "|") + `)(?:[^\p{L}\p{N}]|$)`
	if len(prefix) > 0 {
		p += `|(?:` + strings.Join(prefix, "|") + `)\s*` + num
	}
	return regexp.MustCompile(p)
}

// ungroundedNumber returns the first stat-unit number in text that matches no
// fact within the rounding of the number as written, or "".
func ungroundedNumber(pattern *regexp.Regexp, text string, facts []Fact, quoted string) string {
	for _, m := range pattern.FindAllStringSubmatch(text, -1) {
		raw := strings.Join(m[1:], "")
		if quoted != "" && strings.Contains(quoted, raw) {
			continue
		}
		written := strings.Replace(raw, ",", ".", 1)
		n, err := strconv.ParseFloat(written, 64)
		if err != nil {
			continue
		}
		places := 0
		if i := strings.IndexByte(written, '.'); i >= 0 {
			places = len(written) - i - 1
		}
		if !matchesFact(n, places, facts) {
			return raw
		}
	}
	return ""
}

// matchesFact accepts n when a fact, or the fact in cents, rounds to n at the
// places n was written with.
func matchesFact(n float64, places int, facts []Fact) bool {
	scale := math.Pow10(places)
	for _, f := range facts {
		for _, v := range []float64{f.Value, f.Value * 100} {
			if math.Round(v*scale) == math.Round(n*scale) {
				return true
			}
		}
	}
	return false
}
