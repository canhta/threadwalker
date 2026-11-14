package dashboard

import (
	"fmt"
	"github.com/canhta/threadz/internal/config"
	"math"
	"sort"
	"time"
)

// metricSample is one cumulative measurement of an own publication. Views and
// engagement counters only grow, so a window is the difference between the
// latest sample at each end. When an action predates the window but has no
// sample before it, the first sample inside the window is used as the baseline
// rather than zero, which would overstate growth after a collection gap.
type metricSample struct {
	ActionID   int64
	PostedAt   time.Time
	MeasuredAt time.Time
	Views      int64
	Likes      int64
	Replies    int64
	Reposts    int64
	Quotes     int64
	Followers  *int64
}

type counts struct {
	Views, Likes, Replies, Reposts int64
}

func (c counts) engagement() int64 { return c.Likes + c.Replies + c.Reposts }

func (c counts) add(o counts) counts {
	return counts{c.Views + o.Views, c.Likes + o.Likes, c.Replies + o.Replies, c.Reposts + o.Reposts}
}

func (c counts) subClamped(o counts) counts {
	return counts{clamp0(c.Views - o.Views), clamp0(c.Likes - o.Likes), clamp0(c.Replies - o.Replies), clamp0(c.Reposts - o.Reposts)}
}

func clamp0(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

func (c counts) at(s metricSample) counts {
	return counts{s.Views, s.Likes, s.Replies, s.Reposts}
}

func latestAt(samples []metricSample, from, to time.Time) (counts, bool) {
	var (
		end   *metricSample
		base  *metricSample
		first *metricSample
	)
	for i := range samples {
		s := &samples[i]
		if !s.MeasuredAt.After(to) {
			end = s
		}
		if s.MeasuredAt.Before(from) {
			base = s
		} else if first == nil && s.MeasuredAt.Before(to) {
			first = s
		}
	}
	if end == nil {
		return counts{}, false
	}
	if base == nil && samples[0].PostedAt.Before(from) && first != nil {
		base = first
	}
	out := endCounts(*end)
	if base != nil {
		out = out.subClamped(endCounts(*base))
	}
	return out, true
}

func endCounts(s metricSample) counts { return counts{s.Views, s.Likes, s.Replies, s.Reposts} }

// earnedBetween sums the growth of each action inside [from,to].
func earnedBetween(samples []metricSample, from, to time.Time) counts {
	byAction := map[int64][]metricSample{}
	order := []int64{}
	for _, s := range samples {
		if _, ok := byAction[s.ActionID]; !ok {
			order = append(order, s.ActionID)
		}
		byAction[s.ActionID] = append(byAction[s.ActionID], s)
	}
	var total counts
	for _, id := range order {
		group := byAction[id]
		sort.Slice(group, func(i, j int) bool { return group[i].MeasuredAt.Before(group[j].MeasuredAt) })
		if c, ok := latestAt(group, from, to); ok {
			total = total.add(c)
		}
	}
	return total
}

func dayStarts(l config.LimitsConfig, now time.Time, days int) []time.Time {
	day, _, _ := l.DayBounds(now)
	out := make([]time.Time, days)
	for i := range out {
		out[i] = day.AddDate(0, 0, -(days - 1 - i))
	}
	return out
}

func engagementRate(c counts) (float64, bool) {
	if c.Views == 0 {
		return 0, false
	}
	return float64(c.engagement()) / float64(c.Views), true
}

// countDelta describes the change between two period totals in words.
func countDelta(cur, prior int64, period string) (string, bool) {
	if prior <= 0 {
		return "no earlier period to compare", false
	}
	if cur == prior {
		return "same as the previous " + period, true
	}
	change := (float64(cur) - float64(prior)) / float64(prior) * 100
	if change > 0 {
		return "up " + formatSignedPercent(change) + " on the previous " + period, true
	}
	return "down " + formatSignedPercent(-change) + " on the previous " + period, true
}

// rateDelta describes the change between two rates in percentage points.
func rateDelta(cur, prior float64, curOK, priorOK bool, period string) (string, bool) {
	if !curOK || !priorOK {
		return "no earlier period to compare", false
	}
	points := (cur - prior) * 100
	switch {
	case math.Abs(points) < 0.05:
		return "same as the previous " + period, true
	case points > 0:
		return "up " + formatPoints(points) + " points on the previous " + period, true
	default:
		return "down " + formatPoints(-points) + " points on the previous " + period, true
	}
}

// countDeltaShort is the compact form used in the four secondary stat cells;
// the full "on the previous period" phrase appears once on the views total.
func countDeltaShort(cur, prior int64) string {
	if prior <= 0 {
		return ""
	}
	if cur == prior {
		return "no change"
	}
	change := (float64(cur) - float64(prior)) / float64(prior) * 100
	if change > 0 {
		return "+" + formatSignedPercent(change)
	}
	return "-" + formatSignedPercent(-change)
}

func rateDeltaShort(cur, prior float64, curOK, priorOK bool) string {
	if !curOK || !priorOK {
		return ""
	}
	points := (cur - prior) * 100
	switch {
	case math.Abs(points) < 0.05:
		return "no change"
	case points > 0:
		return "+" + formatPoints(points) + " points"
	default:
		return "-" + formatPoints(-points) + " points"
	}
}

func formatPoints(v float64) string {
	if v >= 10 || v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

func formatSignedPercent(v float64) string {
	if v >= 10 || v == math.Trunc(v) {
		return fmt.Sprintf("%.0f%%", v)
	}
	return fmt.Sprintf("%.1f%%", v)
}

// niceMax rounds a value up to a readable 1/2/5 x 10^n axis maximum.
func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*exp {
			return m * exp
		}
	}
	return 10 * exp
}

const (
	chartW = 600.0
	chartH = 160.0
)

// lineGeometry converts daily values into an SVG path. A nil entry breaks the
// line so a day with no denominator is a gap, never a fake zero.
func lineGeometry(values []*float64, max float64) (path string, axisMax float64) {
	if len(values) == 0 {
		return "", 1
	}
	peak := 0.0
	for _, v := range values {
		if v != nil && *v > peak {
			peak = *v
		}
	}
	axisMax = niceMax(peak)
	step := chartW / float64(maxInt(1, len(values)-1))
	open := false
	var b []byte
	for i, v := range values {
		if v == nil {
			open = false
			continue
		}
		x := float64(i) * step
		y := chartH - (*v/axisMax)*chartH
		if y < 0 {
			y = 0
		}
		if y > chartH {
			y = chartH
		}
		if !open {
			b = append(b, fmt.Sprintf("M%.1f,%.1f", x, y)...)
			open = true
		} else {
			b = append(b, fmt.Sprintf("L%.1f,%.1f", x, y)...)
		}
	}
	return string(b), axisMax
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// barGeometry stacks daily phase values into rects. It returns the axis max so
// the template can show a readable top tick and a budget line.
func barGeometry(rows [][]float64, max float64) (rects []ChartRect, axisMax float64) {
	peak := max
	for _, row := range rows {
		total := 0.0
		for _, v := range row {
			total += v
		}
		if total > peak {
			peak = total
		}
	}
	axisMax = niceMax(peak)
	if len(rows) == 0 {
		return nil, axisMax
	}
	step := chartW / float64(len(rows))
	width := step * 0.62
	classes := []string{"wake", "study", "dream"}
	for i, row := range rows {
		base := chartH
		x := float64(i)*step + (step-width)/2
		for j, v := range row {
			if v <= 0 {
				continue
			}
			h := (v / axisMax) * chartH
			base -= h
			rects = append(rects, ChartRect{X: math.Round(x*10) / 10, Y: math.Round(base*10) / 10, W: math.Round(width*10) / 10, H: math.Round(h*10) / 10, Class: classes[j]})
		}
	}
	return rects, axisMax
}

// formatValue prints an axis tick without trailing noise.
func formatValue(v float64) string {
	if v == 0 {
		return "0"
	}
	switch {
	case v >= 1000:
		return formatCount(int64(math.Round(v)))
	case v >= 10:
		return fmt.Sprintf("%.0f", v)
	case v >= 1:
		if math.Abs(v-math.Round(v)) < 0.05 {
			return fmt.Sprintf("%.0f", v)
		}
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}
