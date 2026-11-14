package dashboard

import (
	"fmt"
	"sort"
	"time"

	"github.com/canhta/threadz/internal/store"
)

// followersAt is the newest follower count read at or before t.
func followersAt(ms metricSet, t time.Time) (int64, bool) {
	var best metricSample
	found := false
	for _, list := range ms.all {
		for _, s := range list {
			if s.Followers != nil && !s.MeasuredAt.After(t) && (!found || s.MeasuredAt.After(best.MeasuredAt)) {
				best, found = s, true
			}
		}
	}
	if !found {
		return 0, false
	}
	return *best.Followers, true
}

func signed(n int64) string {
	if n > 0 {
		return "+" + formatCount(n)
	}
	return formatCount(n)
}

func buildFollowers(ms metricSet, pubs []store.DashboardPublication, daysList []time.Time, currentStart, now time.Time, a Agent) FollowersPanel {
	var f FollowersPanel
	cur, ok := followersAt(ms, now)
	if !ok {
		return f
	}
	f.HasData = true
	f.Now = formatCount(cur)
	f.Chart = lineChart("followers", "Followers", daysList, func(day time.Time) ([]*float64, []string) {
		n, ok := followersAt(ms, day.AddDate(0, 0, 1).Add(-time.Nanosecond))
		if !ok || day.After(now) {
			return []*float64{nil}, []string{"—"}
		}
		v := float64(n)
		return []*float64{&v}, []string{formatCount(n)}
	}, formatValue, a.Location, 10)
	before, ok := followersAt(ms, currentStart)
	if !ok {
		for i := len(pubs) - 1; i >= 0; i-- {
			if p := pubs[i]; p.FollowersAtPost != nil && !p.PostedAt.Before(currentStart) {
				before, ok = *p.FollowersAtPost, true
				break
			}
		}
	}
	if ok {
		f.gain, f.hasGain = cur-before, true
		f.Gained = signed(f.gain) + " in " + countText(len(daysList), "day", "days")
	}
	var gained, views, posts int64
	for _, p := range pubs {
		if p.FollowersAtPost == nil || p.PostedAt.Before(currentStart) {
			continue
		}
		samples := ms.all[p.ID]
		if len(samples) == 0 {
			continue
		}
		last := samples[len(samples)-1]
		if last.Followers == nil {
			continue
		}
		gained += *last.Followers - *p.FollowersAtPost
		views += last.Views
		posts++
	}
	f.PerPost, f.Per1000 = "—", "—"
	if posts > 0 {
		f.PerPost = fmt.Sprintf("%+.1f", float64(gained)/float64(posts))
	}
	if views > 0 {
		f.Per1000 = fmt.Sprintf("%+.2f", float64(gained)/float64(views)*1000)
	}
	return f
}

// buildBlocked counts the guard's refusals of a post or reply, by reason.
func buildBlocked(tools []rawTool) BlockedPanel {
	var b BlockedPanel
	byCode := map[string]*CodeCount{}
	for _, t := range tools {
		if t.Blocked == "" || (t.Tool != "post" && t.Tool != "reply") {
			continue
		}
		b.Total++
		c := byCode[t.BlockCode]
		if c == nil {
			c = &CodeCount{Code: blockedLabel(t.BlockCode), Raw: t.BlockCode}
			byCode[t.BlockCode] = c
		}
		c.Count++
	}
	for _, c := range byCode {
		b.Rows = append(b.Rows, *c)
	}
	sort.Slice(b.Rows, func(i, j int) bool {
		if b.Rows[i].Count != b.Rows[j].Count {
			return b.Rows[i].Count > b.Rows[j].Count
		}
		return b.Rows[i].Raw < b.Rows[j].Raw
	})
	return b
}

func rangeSpend(ledger map[string]float64, phases map[string][2]float64, daysList []time.Time) float64 {
	total := 0.0
	for _, day := range daysList {
		key := day.Format("2006-01-02")
		v := phases[key]
		total += ledger[key]
		if ledger[key] == 0 {
			total += v[0] + v[1]
		}
	}
	return total
}

// buildCost adds what a post, a follower and 1,000 views cost, and the spend
// since the start, to the unit cost panel.
func buildCost(r *ReturnPanel, snap store.DashboardSnapshot, ledger map[string]float64, phases map[string][2]float64, daysList []time.Time, currentStart time.Time, f FollowersPanel, views int64) {
	spend := rangeSpend(ledger, phases, daysList)
	per := func(n float64) string {
		if spend > 0 && n > 0 {
			return formatUSD(spend / n)
		}
		return "—"
	}
	posts := 0
	for _, p := range snap.Publications {
		if !p.PostedAt.Before(currentStart) {
			posts++
		}
	}
	r.CostPerPost, r.CostPerFollow = per(float64(posts)), "—"
	if f.hasGain && f.gain > 0 {
		r.CostPerFollow = per(float64(f.gain))
	}
	r.CostPer1000 = per(float64(views) / 1000)
	r.SpentTotal = formatUSD(snap.SpendTotal)
	if r.Empty && snap.SpendTotal > 0 {
		r.Empty = false
	}
}
