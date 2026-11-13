package dashboard

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// formatCount groups thousands with commas. Every figure on the page uses it
// so digits line up in tabular numerals.
func formatCount(n int64) string {
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return sign + b.String()
}

// formatUSD shows money to the cent; below a cent it says so.
func formatUSD(v float64) string {
	if v > 0 && v < 0.005 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", v)
}

func formatBudget(v float64) string { return fmt.Sprintf("$%.2f", v) }

func formatPercent(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

func formatInt(n int) string { return formatCount(int64(n)) }

// formatClock renders a moment in the owner's timezone. The date appears only
// when the day differs from today.
func formatClock(t, now time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	t, now = t.In(loc), now.In(loc)
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("2 Jan 15:04")
}

func formatDay(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	return t.In(loc).Format("2 Jan")
}

func formatChartDay(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2 Jan")
}

func formatFullDay(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	return t.In(loc).Format("2 Jan 2006, 15:04")
}

// formatAge is used for posts: "3 days", "5 hours", "under an hour".
func formatAge(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Hour:
		return "under an hour"
	case d < 24*time.Hour:
		return countText(int(d/time.Hour), "hour", "hours")
	case d < 48*time.Hour:
		return countText(1, "day", "days")
	default:
		return countText(int(d/(24*time.Hour)), "day", "days")
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// pluralWord is the only singular/plural selector. Exactly one takes the
// singular form; every other count, including zero, takes the plural.
func pluralWord(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// plural adapts pluralWord for the int-valued view fields used in templates.
func plural(n int, one, many string) string {
	return pluralWord(int64(n), one, many)
}

// countText renders "0 drafts", "1 draft" and "2 drafts" through pluralWord.
func countText(n int, one, many string) string {
	return fmt.Sprintf("%s %s", formatCount(int64(n)), plural(n, one, many))
}

// deltaTone reads the direction out of a rendered delta ("+12%", "-15%") so a
// change beside a figure is coloured like the ones in the hero. Empty when the
// string carries no sign.
func deltaTone(s string) string {
	switch {
	case strings.HasPrefix(s, "+"):
		return "up"
	case strings.HasPrefix(s, "-"), strings.HasPrefix(s, "\u2212"):
		return "down"
	}
	return ""
}
