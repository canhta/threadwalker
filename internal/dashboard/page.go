package dashboard

import (
	"cmp"
	"errors"
	"fmt"
	"github.com/canhta/threadz/internal/knowledge"
	"github.com/canhta/threadz/internal/stdx"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/store"
)

const (
	visibleWakes    = 5
	maxPosts        = 42
	visiblePosts    = 6
	postStep        = 12 // how many more rows a "Show more" adds
	topPosts        = 5
	visibleIdeas    = 10
	postChars       = 220 // a post's text as its link, cut to fit a card
	ideaChars       = 160
	visibleNotes    = 10
	maxTrendVisible = 8
	maxToolRows     = 8
)

type rawWake struct {
	ID        int64
	StartedAt time.Time
	EndedAt   time.Time
	Ended     bool
	Kind      string
	Steps     int
	Cost      float64
	EndReason string
}

type rawTool struct {
	ID        int64
	WakeID    int64
	Tool      string
	Blocked   string
	Outcome   string
	BlockCode string
	Reason    string
}

type rawAction struct {
	ID       int64
	Kind     string
	PostedAt time.Time
	URL      string
	pub      store.DashboardPublication
}

type trendItem struct {
	Title  string
	Source string
	Rank   int
	Seen   map[time.Time]bool
	Last   time.Time
}

type daySpend struct {
	Start time.Time
	Wake  float64
	Study float64
	Dream float64
	Total float64
}

// buildPage turns one agent's snapshot into the public view model. It reads no
// database and no clock; a snapshot that holds an impossible number is refused.
// rangeDays are the selectable history ranges.
var rangeDays = []int{7, 14, 30, 90}

// postOrders are the accepted ?sort= values. The empty value is the default
// (newest first) and is written "recent" in a URL so the cache key is explicit.
var postOrders = []string{"recent", "views", "engaged"}

// postShows are the accepted ?show= values: the first page, then one step at a
// time up to what the panel keeps. A request renders at most one of these, so
// the row count in the response is bounded whatever the account has posted.
var postShows = func() []int {
	out := []int{visiblePosts}
	for n := visiblePosts + postStep; n < maxPosts; n += postStep {
		out = append(out, n)
	}
	return append(out, maxPosts)
}()

// postView is the order the reader asked the list for, and how much of it.
type postView struct {
	order string
	show  int
}

func (v postView) showOr() int {
	if v.show == 0 {
		return visiblePosts
	}
	return v.show
}

// next is the show value one step further on, or 0 when everything that exists
// is already on the page.
func (v postView) next(total int) int {
	for _, n := range postShows {
		if n > v.showOr() && v.showOr() < total {
			return min(n, total)
		}
	}
	return 0
}

func (v postView) orderOr() string {
	if v.order == "" {
		return "recent"
	}
	return v.order
}

// apply orders the ranked rows the way the reader asked. The ranking buildPosts
// did stays untouched, so "best", "worst" and the top performers keep meaning
// the same thing whatever order the list is in.
func (v postView) apply(rows []PostRow) []PostRow {
	out := append([]PostRow(nil), rows...)
	switch v.orderOr() {
	case "views":
		sort.SliceStable(out, func(i, j int) bool { return out[i].views > out[j].views })
	case "engaged":
		sort.SliceStable(out, func(i, j int) bool { return out[i].rate > out[j].rate })
	default:
		sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	}
	return out
}

// listURL is one dashboard URL with every read parameter spelled out, so a
// switch changes exactly one of them.
func listURL(agentID string, days int, v postView) string {
	return "/?agent=" + url.QueryEscape(agentID) + "&range=" + strconv.Itoa(days) +
		"&sort=" + v.orderOr() + "&show=" + strconv.Itoa(v.showOr())
}

// historyDays caps a panel's range at what the table behind it keeps.
func historyDays(table string, days int) int {
	age, ok := store.RetentionAge(table)
	if !ok {
		return days
	}
	return min(days, int(age/(24*time.Hour)))
}

func buildPage(snap store.DashboardSnapshot, agents []Agent, a Agent, days int, view postView, now time.Time, content bool) (Page, error) {
	p := Page{Range: days, RangeLabel: countText(days, "day", "days"), Content: content}
	p.Agent = AgentInfo{ID: a.ID, DryRun: a.DryRun}
	for _, other := range agents {
		p.Agents = append(p.Agents, AgentLink{ID: other.ID, URL: listURL(other.ID, days, view), Current: other.ID == a.ID})
	}
	for _, d := range rangeDays {
		p.Ranges = append(p.Ranges, RangeLink{Label: countText(d, "day", "days"), Value: d, URL: listURL(a.ID, d, view), Current: d == days})
	}
	for _, o := range postOrders {
		p.Orders = append(p.Orders, FilterLink{Label: map[string]string{"recent": "Newest", "views": "Most seen", "engaged": "Most engaged"}[o],
			URL: listURL(a.ID, days, postView{order: o, show: view.show}) + "#posted-h", Current: o == view.orderOr()})
	}

	if snap.Started {
		p.Agent.Status = oneOf(snap.Status, "running", "paused", "killed")
		if !snap.NextWake.IsZero() {
			p.Agent.NextWake = formatClock(snap.NextWake, now, a.Location)
		}
	} else {
		p.Agent.Status = "not_started"
	}

	daysList := dayStarts(now, days)
	currentStart := daysList[0]
	priorStart := currentStart.AddDate(0, 0, -days)

	posted, replies, posts := publications(snap.Publications, a.Username, content)

	wakes, err := wakesFrom(snap.Wakes)
	if err != nil {
		return Page{}, err
	}
	if w := snap.LastWake; w != nil {
		if !w.StartedAt.IsZero() {
			p.Agent.LastWake = formatClock(w.StartedAt, now, a.Location)
		}
		p.Agent.LastWakeCode = "in_progress"
		if w.Ended {
			p.Agent.LastWakeCode = endReason(w.EndReason)
		}
	}
	tools, err := toolsFrom(snap.ToolCalls)
	if err != nil {
		return Page{}, err
	}
	p.Wakes = buildWakes(wakes, tools, now, a.Location, historyDays("wakes", days))
	p.ToolCalls = buildTools(tools, historyDays("tool_calls", days), posted > 0)

	trends, trendChecks, _, _ := trendsFrom(snap.Trends)
	p.Trending = buildTrending(trends, trendChecks, historyDays("trend_stories", days), a.Location)

	ledger, phases, err := spendFrom(snap)
	if err != nil {
		return Page{}, err
	}
	p.Burn = buildBurn(ledger, phases, a, daysList, now, a.Location)
	todayKey := a.spendDay(now)
	spentToday := ledger[todayKey]
	if spentToday == 0 {
		v := phases[todayKey]
		spentToday = v[0] + v[1] + v[2]
	}

	p.Learned = learnedFrom(snap, content)

	metrics, err := metricsFrom(snap.Metrics, posts, snap.Publications, a.Username, priorStart, now)
	if err != nil {
		return Page{}, err
	}
	p.Growth = buildGrowth(metrics, days, daysList, currentStart, priorStart, now, a, view)
	if next := view.next(p.Growth.Total); next > 0 {
		p.MoreURL = listURL(a.ID, days, postView{order: view.order, show: next}) + "#posted-h"
		p.MoreCount = next - p.Growth.Shown
	}
	p.Return = ReturnPanel{Empty: !p.Growth.HasMetrics}
	if !p.Growth.HasMetrics {
		p.Growth.EmptyText = "No data yet."
	}

	buildStandings(&p.Learned, snap)
	p.Followers = buildFollowers(metrics, snap.Publications, daysList, currentStart, now, a)
	p.Outcomes = buildOutcomes(snap.Publications)
	p.Blocked = buildBlocked(tools)
	buildCost(&p.Return, snap, ledger, phases, daysList, currentStart, p.Followers, p.Growth.Views)

	p.KPIs = buildKPIs(kpiInput{posts: posted - replies, replies: replies, days: days, spentToday: spentToday, budget: a.DailyBudgetUSD, growth: p.Growth, followers: p.Followers})
	return p, nil
}

// publications counts the real actions that sit under the agent's own username
// and lists the latest own posts and replies. Newest first in, newest first out.
func publications(all []store.DashboardPublication, username string, content bool) (posted, replies int, posts []rawAction) {
	for _, pub := range all {
		if !content {
			pub.Why = nil
		}
		if ownURL(pub.URL, username) == "" {
			continue
		}
		posted++
		if pub.Kind == "reply" {
			replies++
		}
		if (pub.Kind == "post" || pub.Kind == "reply") && len(posts) < maxPosts {
			posts = append(posts, rawAction{ID: pub.ID, Kind: pub.Kind, PostedAt: pub.PostedAt, URL: pub.URL, pub: pub})
		}
	}
	return posted, replies, posts
}

func endReason(reason string) string {
	if oneOfOK(reason, endReasons...) {
		return reason
	}
	return "stopped"
}

func wakesFrom(in []store.DashboardWake) ([]rawWake, error) {
	out := make([]rawWake, 0, len(in))
	for _, w := range in {
		if w.ID <= 0 || w.Steps < 0 || !stdx.FiniteNonNegative(w.CostUSD) {
			return nil, errors.New("invalid run metadata")
		}
		raw := rawWake{ID: w.ID, StartedAt: w.StartedAt, EndedAt: w.EndedAt, Ended: w.Ended, Kind: oneOf(w.Kind, "agent", "metrics", "dream", "study"), Steps: w.Steps, Cost: w.CostUSD, EndReason: "in_progress"}
		if w.Ended {
			raw.EndReason = endReason(w.EndReason)
		}
		out = append(out, raw)
	}
	return out, nil
}

func toolsFrom(in []store.DashboardToolCall) ([]rawTool, error) {
	out := make([]rawTool, 0, len(in))
	for _, c := range in {
		if c.ID <= 0 || c.WakeID < 0 {
			return nil, errors.New("invalid step metadata")
		}
		t := rawTool{ID: c.ID, WakeID: c.WakeID, Tool: c.Tool, Blocked: c.BlockedBy}
		t.Outcome, t.Reason = toolSummary(c.Result, c.BlockedBy != "")
		if c.BlockedBy != "" {
			t.BlockCode = c.BlockedBy
			if !oneOfOK(c.BlockedBy, blockedReasons...) {
				t.BlockCode = "error"
			}
		} else if t.Outcome == "error" && strings.HasPrefix(t.Reason, "lesson_") {
			// The lesson gate refused the text; nothing failed.
			t.Outcome, t.Blocked, t.BlockCode = "blocked", t.Reason, t.Reason
		}
		out = append(out, t)
	}
	return out, nil
}

func buildTools(tools []rawTool, days int, hasPosts bool) ToolPanel {
	type acc struct{ ok, failed, blocked, other int }
	counts := map[string]*acc{}
	order := []string{}
	for _, t := range tools {
		if !hasPosts && t.Tool == "read_own_metrics" {
			// With no own post there is nothing to read; the failure is expected.
			continue
		}
		a, ok := counts[t.Tool]
		if !ok {
			a = &acc{}
			counts[t.Tool] = a
			order = append(order, t.Tool)
		}
		switch t.Outcome {
		case "success":
			a.ok++
		case "error":
			a.failed++
		case "blocked":
			a.blocked++
		default:
			a.other++
		}
	}
	rows := make([]ToolStat, 0, len(order))
	for _, name := range order {
		a := counts[name]
		rows = append(rows, ToolStat{Name: toolLabel(name), OK: a.ok, Failed: a.failed, Blocked: a.blocked, Other: a.other, Total: a.ok + a.failed + a.blocked + a.other})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Total != rows[j].Total {
			return rows[i].Total > rows[j].Total
		}
		return rows[i].Name < rows[j].Name
	})
	if len(rows) > maxToolRows {
		rows = rows[:maxToolRows]
	}
	return ToolPanel{Rows: rows, RangeLabel: countText(days, "day", "days")}
}

func buildWakes(wakes []rawWake, tools []rawTool, now time.Time, loc *time.Location, days int) WakesPanel {
	byWake := map[int64][]rawTool{}
	for _, t := range tools {
		byWake[t.WakeID] = append(byWake[t.WakeID], t)
	}
	rows := make([]WakeRow, 0, len(wakes))
	for _, w := range wakes {
		row := WakeRow{ID: w.ID, Kind: kindLabel(w.Kind), Steps: w.Steps, Cost: formatUSD(w.Cost), EndReason: endReasonLabel(w.EndReason), EndCode: w.EndReason, Bad: endReasonBad(w.EndReason), InProgress: w.EndReason == "in_progress"}
		if !w.StartedAt.IsZero() {
			row.When = formatClock(w.StartedAt, now, loc)
			row.WhenTitle = formatFullDay(w.StartedAt, loc)
			row.At = w.StartedAt
			if !w.EndedAt.IsZero() {
				row.WhenTitle += " to " + w.EndedAt.In(loc).Format("15:04")
				row.EndAt = w.EndedAt
			}
		}
		type tc struct {
			ok, failed, blocked, other int
			why                        map[string]int
		}
		perTool := map[string]*tc{}
		order := []string{}
		blocked := map[string]int{}
		for _, t := range byWake[w.ID] {
			c, ok := perTool[t.Tool]
			if !ok {
				c = &tc{why: map[string]int{}}
				perTool[t.Tool] = c
				order = append(order, t.Tool)
			}
			switch t.Outcome {
			case "success":
				c.ok++
			case "error":
				c.failed++
				c.why[t.Reason]++
			case "blocked":
				c.blocked++
			default:
				c.other++
			}
			if t.Blocked != "" {
				blocked[t.BlockCode]++
			}
		}
		sort.SliceStable(order, func(i, j int) bool {
			a, b := perTool[order[i]], perTool[order[j]]
			return a.ok+a.failed+a.blocked+a.other > b.ok+b.failed+b.blocked+b.other
		})
		for _, name := range order {
			c := perTool[name]
			if c.failed+c.blocked > 0 { // the Tools table already counts the rest
				row.ToolSummary = append(row.ToolSummary, WakeTool{Name: toolLabel(name), Failed: c.failed, Blocked: c.blocked, Why: failureText(c.why)})
			}
		}
		codes := make([]string, 0, len(blocked))
		for code := range blocked {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		for _, code := range codes {
			row.Blocked = append(row.Blocked, CodeCount{Code: blockedLabel(code), Raw: code, Count: blocked[code]})
		}
		if row.When == "" {
			row.When = "—"
		}
		rows = append(rows, row)
	}
	out := WakesPanel{RangeLabel: "last " + countText(days, "day", "days"), Count: len(rows)}
	if len(rows) > visibleWakes {
		out.Rows = rows[:visibleWakes]
		out.Older = rows[visibleWakes:]
	} else {
		out.Rows = rows
	}
	return out
}

// failureText lists a tool's failure reasons, most common first.
func failureText(why map[string]int) string {
	codes := make([]string, 0, len(why))
	for code := range why {
		codes = append(codes, code)
	}
	sort.Slice(codes, func(i, j int) bool {
		if why[codes[i]] != why[codes[j]] {
			return why[codes[i]] > why[codes[j]]
		}
		return codes[i] < codes[j]
	})
	parts := make([]string, len(codes))
	for i, code := range codes {
		parts[i] = fmt.Sprintf("%s %d", strings.ToLower(failureLabel(code)), why[code])
	}
	return strings.Join(parts, ", ")
}

func buildTrending(items []trendItem, checks int, days int, loc *time.Location) TrendingPanel {
	panel := TrendingPanel{Checks: checks, RangeLabel: "last " + countText(days, "day", "days")}
	for _, it := range items {
		seen := len(it.Seen)
		pct := 0.0
		if checks > 0 {
			pct = float64(seen) / float64(checks) * 100
		}
		panel.Rows = append(panel.Rows, TrendRow{
			Title:      it.Title,
			Source:     sourceTag(it.Source),
			SourceName: sourceLabel(it.Source),
			Seen:       fmt.Sprintf("%d of %s", seen, countText(checks, "day", "days")),
			Pct:        pct,
			Rank:       it.Rank,
		})
	}
	if len(panel.Rows) > maxTrendVisible {
		panel.Rows = panel.Rows[:maxTrendVisible]
		panel.More = true
	}
	return panel
}

func trendsFrom(in []store.DashboardTrend) (items []trendItem, checks, total, sourceCount int) {
	byTitle := map[string]*trendItem{}
	seenChecks := map[time.Time]bool{}
	sources := map[string]bool{}
	for _, row := range in {
		title := strings.TrimSpace(row.Title)
		if title == "" || row.ObservedAt.IsZero() {
			continue
		}
		at := row.ObservedAt.UTC().Truncate(24 * time.Hour) // seen counts days, not checks
		seenChecks[at] = true
		total++
		for _, part := range strings.Split(row.Source, ",") {
			if s := strings.TrimSpace(part); s != "" {
				sources[s] = true
			}
		}
		item, ok := byTitle[title]
		if !ok {
			item = &trendItem{Title: title, Source: row.Source, Rank: row.Rank, Seen: map[time.Time]bool{}, Last: at}
			byTitle[title] = item
		}
		item.Seen[at] = true
		if row.Rank > 0 && (item.Rank == 0 || row.Rank < item.Rank) {
			item.Rank = row.Rank
		}
		if at.After(item.Last) {
			item.Last = at
			item.Source = row.Source
			if row.Rank > 0 {
				item.Rank = row.Rank
			}
		}
	}
	items = make([]trendItem, 0, len(byTitle))
	for _, item := range byTitle {
		items = append(items, *item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		si, sj := len(items[i].Seen), len(items[j].Seen)
		if si != sj {
			return si > sj
		}
		ri, rj := items[i].Rank, items[j].Rank
		if ri == 0 {
			ri = 1 << 20
		}
		if rj == 0 {
			rj = 1 << 20
		}
		if ri != rj {
			return ri < rj
		}
		if !items[i].Last.Equal(items[j].Last) {
			return items[i].Last.After(items[j].Last)
		}
		return items[i].Title < items[j].Title
	})
	return items, len(seenChecks), total, len(sources)
}

func spendFrom(snap store.DashboardSnapshot) (map[string]float64, map[string][3]float64, error) {
	ledger := map[string]float64{}
	phases := map[string][3]float64{}
	for _, d := range snap.Ledger {
		if !stdx.FiniteNonNegative(d.CostUSD) {
			return nil, nil, errors.New("invalid spend")
		}
		ledger[d.Day] = d.CostUSD
	}
	for _, row := range snap.Phases {
		if !stdx.FiniteNonNegative(row.CostUSD) {
			return nil, nil, errors.New("invalid phase spend")
		}
		v := phases[row.Day]
		switch row.Phase {
		case "agent", "critic": // critic spend is part of the wake
			v[0] += row.CostUSD
		case "study":
			v[1] += row.CostUSD
		case "dream":
			v[2] += row.CostUSD
		default:
			return nil, nil, errors.New("reading phase spend: unknown stored phase")
		}
		phases[row.Day] = v
	}
	return ledger, phases, nil
}

func learnedFrom(snap store.DashboardSnapshot, content bool) LearnedPanel {
	pb := snap.Playbook
	l := LearnedPanel{Content: content, Testing: pb.Testing, Established: pb.Established, Rejected: pb.Rejected, Samples: pb.Samples, Notes: snap.NoteCount}
	for _, in := range snap.Hypotheses {
		h := HypothesisRow{ID: in.ID, Status: in.Status, N: int64(in.N), Text: truncate(in.Text, ideaChars)}
		if !in.DecidedAt.IsZero() {
			h.ChangedAt = in.DecidedAt.Format("2 Jan 2006")
		}
		if content {
			h.Outcomes = in.Outcomes
			if d := in.Decision; d != nil {
				head := "Changed to " + ideaStatus(d.Status)
				if !d.At.IsZero() {
					head += " on " + d.At.Format("2 Jan")
				}
				parts := []string{head}
				if strings.TrimSpace(d.Reason) != "" {
					parts = append(parts, d.Reason)
				}
				h.Evidence = strings.Join(parts, ": ")
			}
		}
		l.Hypotheses = append(l.Hypotheses, h)
	}
	if len(l.Hypotheses) > visibleIdeas {
		l.OlderHypotheses = l.Hypotheses[visibleIdeas:]
		l.Hypotheses = l.Hypotheses[:visibleIdeas]
	}
	if content {
		l.MemoryIndex = truncate(strings.Join(snap.MemoryIndex, "\n"), 2000)
		var lines []string
		for _, j := range snap.MemoryJournal {
			lines = append(lines, fmt.Sprintf("#%d %s %s: %s", j.ID, j.Change, j.Phase, cmp.Or(j.NewText, j.OldText)))
		}
		l.MemoryJournal = truncate(strings.Join(lines, "\n"), 2000)
		for i, s := range snap.Strategy {
			if i == store.DashboardStrategyShown {
				break
			}
			prev := ""
			if i+1 < len(snap.Strategy) {
				prev = snap.Strategy[i+1].Text
			}
			row := StrategyRow{Version: s.Version, Citations: strings.Join(s.Citations, ", "), Diff: lineDiff(prev, s.Text)}
			if !s.CreatedAt.IsZero() {
				row.When = s.CreatedAt.Format("2 Jan 15:04")
				row.At = s.CreatedAt
			}
			l.Strategy = append(l.Strategy, row)
		}
		for _, n := range snap.Notes {
			row := NoteRow{Text: n.Text}
			if !n.CreatedAt.IsZero() {
				row.When = n.CreatedAt.Format("2 Jan 15:04")
				row.At = n.CreatedAt
			}
			l.NotesRows = append(l.NotesRows, row)
		}
		if len(l.NotesRows) > visibleNotes {
			l.OlderNotes = l.NotesRows[visibleNotes:]
			l.NotesRows = l.NotesRows[:visibleNotes]
		}
	}
	if content {
		if entries, err := knowledge.All(); err == nil {
			for _, e := range entries {
				if e.PenaltyRisk != "" {
					l.Penalties = append(l.Penalties, PenaltyRow{Risk: penaltyLabel(e.PenaltyRisk), Claim: e.Claim, Source: e.SourceURL})
				}
			}
		}
		for _, c := range snap.Candidates {
			row := CandidateRow{Text: c.Technique, Source: c.SourceURL}
			if !c.CreatedAt.IsZero() {
				row.When = c.CreatedAt.Format("2 Jan")
				row.At = c.CreatedAt
			}
			l.Candidates = append(l.Candidates, row)
		}
	}
	l.HasRows = len(l.Candidates) > 0 || l.Testing+l.Established+l.Rejected > 0 || l.Notes > 0 || l.Samples > 0 || len(snap.MemoryIndex) > 0 || len(snap.Strategy) > 0
	return l
}

type metricSet struct {
	samples []metricSample
	posts   []rawAction
	all     map[int64][]metricSample
	last    time.Time
}

// metricsFrom keeps the samples of the agent's own posts and replies. A sample
// counts toward the range totals when its action was posted in [priorStart,now];
// it also feeds the ranked list when its action is among the latest own posts.
func metricsFrom(in []store.DashboardMetric, posts []rawAction, pubs []store.DashboardPublication, username string, priorStart, now time.Time) (metricSet, error) {
	ms := metricSet{all: map[int64][]metricSample{}, posts: posts}
	listed := map[int64]bool{}
	for _, p := range posts {
		listed[p.ID] = true
	}
	own := map[int64]time.Time{}
	for _, pub := range pubs {
		if ownURL(pub.URL, username) != "" {
			own[pub.ID] = pub.PostedAt
		}
	}
	for _, m := range in {
		if m.Views < 0 || m.Likes < 0 || m.Replies < 0 || m.Reposts < 0 {
			return ms, errors.New("invalid metric sample")
		}
		posted, ok := own[m.ActionID]
		if !ok {
			continue
		}
		if m.MeasuredAt.IsZero() {
			return ms, errors.New("invalid measurement time")
		}
		inRange := !posted.IsZero() && !posted.Before(priorStart) && !posted.After(now)
		if !inRange && !listed[m.ActionID] {
			continue
		}
		s := metricSample{ActionID: m.ActionID, PostedAt: posted, MeasuredAt: m.MeasuredAt, Views: m.Views, Likes: m.Likes, Replies: m.Replies, Reposts: m.Reposts, Quotes: m.Quotes, Followers: m.Followers}
		ms.all[m.ActionID] = append(ms.all[m.ActionID], s)
		if inRange {
			ms.samples = append(ms.samples, s)
		}
		if s.MeasuredAt.After(ms.last) {
			ms.last = s.MeasuredAt
		}
	}
	return ms, nil
}

func buildBurn(ledger map[string]float64, phases map[string][3]float64, a Agent, days []time.Time, now time.Time, loc *time.Location) BurnPanel {
	p := BurnPanel{RangeLabel: "last " + countText(len(days), "day", "days"), BudgetText: formatBudget(a.DailyBudgetUSD)}
	rows := make([]daySpend, 0, len(days))
	spendDays := 0
	for _, day := range days {
		key := day.Format("2006-01-02")
		total, ok := ledger[key]
		v := phases[key]
		if !ok && v[0]+v[1]+v[2] == 0 {
			continue
		}
		if !ok {
			total = v[0] + v[1] + v[2]
		}
		if v[0]+v[1]+v[2] == 0 && total > 0 {
			v[0] = total
		}
		if total > 0 || v[0]+v[1]+v[2] > 0 {
			spendDays++
		}
		rows = append(rows, daySpend{Start: day, Wake: v[0], Study: v[1], Dream: v[2], Total: total})
	}
	if len(rows) == 0 {
		return p
	}
	p.HasRows = true
	today := a.spendDay(now)
	todayTotal := ledger[today]
	todayPhase := phases[today]
	if todayPhase[0]+todayPhase[1]+todayPhase[2] == 0 && todayTotal > 0 {
		todayPhase[0] = todayTotal
	}
	if todayTotal == 0 {
		todayTotal = todayPhase[0] + todayPhase[1] + todayPhase[2]
	}
	p.PercentText = fmt.Sprintf("%.1f%% of the %s limit", todayTotal/a.DailyBudgetUSD*100, formatBudget(a.DailyBudgetUSD))
	p.BarPct = math.Min(100, todayTotal/a.DailyBudgetUSD*100)
	if todayTotal > 0 && p.BarPct < 0.6 {
		p.BarPct = 0.6
	}
	parts := []string{}
	if v := todayPhase[0]; v > 0 {
		parts = append(parts, "Runs "+formatUSD(v))
	}
	if v := todayPhase[1]; v > 0 {
		parts = append(parts, "Research "+formatUSD(v))
	}
	if v := todayPhase[2]; v > 0 {
		parts = append(parts, "Planning "+formatUSD(v))
	}
	if len(parts) > 0 {
		p.PhaseText = strings.Join(parts, ", ")
	}
	if len(rows) > 1 || spendDays > 1 || (len(rows) == 1 && rows[0].Start.Format("2006-01-02") != today) {
		p.MultiDay = true
		chartRows := make([]daySpend, len(days))
		for i, day := range days {
			key := day.Format("2006-01-02")
			v := phases[key]
			total, ok := ledger[key]
			if !ok || total == 0 {
				total = v[0] + v[1] + v[2]
			}
			if v[0]+v[1]+v[2] == 0 && total > 0 {
				v[0] = total
			}
			chartRows[i] = daySpend{Start: day, Wake: v[0], Study: v[1], Dream: v[2], Total: total}
		}
		values := make([][]float64, len(chartRows))
		for i, r := range chartRows {
			values[i] = []float64{r.Wake, r.Study, r.Dream}
		}
		rects, axisMax := barGeometry(values, a.DailyBudgetUSD)
		ch := &Chart{
			ID: "spend-days", Title: "Spending per day", Height: "bars",
			YMax: formatBudget(axisMax), YMid: formatBudget(axisMax / 2), YZero: "$0",
			Rects: rects, HasData: true,
			Head: []string{"Day", "Runs", "Research", "Planning", "Total"},
		}
		if a.DailyBudgetUSD <= axisMax {
			ch.BudgetY = chartH - (a.DailyBudgetUSD/axisMax)*chartH
			ch.Budget = "Dashed line: " + formatBudget(a.DailyBudgetUSD) + " limit a day."
		}
		for _, r := range chartRows {
			ch.Rows = append(ch.Rows, []string{formatDay(r.Start, loc), formatUSD(r.Wake), formatUSD(r.Study), formatUSD(r.Dream), formatUSD(r.Total)})
		}
		starts := make([]time.Time, len(chartRows))
		for i, r := range chartRows {
			starts[i] = r.Start
		}
		ch.XLabels = xLabelTimes(starts, loc)
		p.Chart = ch
	}
	return p
}

func xLabelTimes(days []time.Time, loc *time.Location) []ChartLabel {
	if len(days) == 0 {
		return nil
	}
	pick := func(i int) string { return formatChartDay(days[i], loc) }
	if len(days) == 1 {
		return []ChartLabel{{Text: pick(0), Class: "first"}}
	}
	return []ChartLabel{
		{Text: pick(0), Class: "first"},
		{Text: pick(len(days) / 2), Class: "mid"},
		{Text: pick(len(days) - 1), Class: "last"},
	}
}

func buildGrowth(ms metricSet, days int, daysList []time.Time, currentStart, priorStart, now time.Time, a Agent, view postView) *GrowthPanel {
	g := &GrowthPanel{RangeLabel: "last " + countText(days, "day", "days"), HasMetrics: len(ms.all) > 0}
	// Past a month a point per day is noise: chart one point per week.
	step, unit, points := 1, "day", daysList
	if days > 30 {
		step, unit, points = 7, "week", nil
		for end := len(daysList) - 1; end-step+1 >= 0; end -= step {
			points = append([]time.Time{daysList[end-step+1]}, points...)
		}
	}
	g.ViewsChart = lineChart("views", "Views per "+unit, points, func(day time.Time) ([]*float64, []string) {
		c := earnedBetween(ms.samples, day, day.AddDate(0, 0, step))
		v := float64(c.Views)
		return []*float64{&v}, []string{formatCount(c.Views)}
	}, func(v float64) string { return formatValue(v) }, a.Location, 10)

	if g.HasMetrics {
		cur := earnedBetween(ms.samples, currentStart, now)
		prior := earnedBetween(ms.samples, priorStart, currentStart)
		g.Views, g.Likes, g.Replies, g.Reposts = cur.Views, cur.Likes, cur.Replies, cur.Reposts
		if text, ok := countDelta(cur.Views, prior.Views, countText(days, "day", "days")); ok {
			g.ViewsDelta = text
		}
		g.LikesDelta = countDeltaShort(cur.Likes, prior.Likes)
		g.RepliesDelta = countDeltaShort(cur.Replies, prior.Replies)
		g.RepostsDelta = countDeltaShort(cur.Reposts, prior.Reposts)
		rate, rateOK := engagementRate(cur)
		priorRate, priorOK := engagementRate(prior)
		g.Rate = "—"
		if rateOK {
			g.Rate = formatPercent(rate)
		}
		if text, ok := rateDelta(rate, priorRate, rateOK, priorOK, countText(days, "day", "days")); ok {
			g.RateDelta = text
		}
		g.RateChart = lineChart("rate", "Engagement rate per "+unit, points, func(day time.Time) ([]*float64, []string) {
			r, ok := engagementRate(earnedBetween(ms.samples, day, day.AddDate(0, 0, step)))
			if !ok {
				return []*float64{nil}, []string{"—"}
			}
			v := r * 100
			return []*float64{&v}, []string{formatPercent(r)}
		}, func(v float64) string { return formatValue(v) + "%" }, a.Location, 4)
		g.LastMeasured = formatClock(ms.last, now, a.Location)
		g.MeasuredAt = ms.last
	}

	rows := buildPosts(ms, now, a.Location)
	for _, r := range rows {
		if !r.HasSample {
			g.Unmeasured++
		}
	}
	for _, r := range rows {
		if len(g.Top) < topPosts && r.HasSample && r.Rate != "—" && r.Rate != "0.0%" {
			g.Top = append(g.Top, r)
		}
	}
	// rows are ranked by engagement; the list is the reader's own order.
	listed := view.apply(rows)
	g.Posts = listed[:min(view.showOr(), len(listed))]
	g.Shown, g.Total = len(g.Posts), len(listed)
	return g
}

func lineChart(id, title string, days []time.Time, value func(time.Time) ([]*float64, []string), format func(float64) string, loc *time.Location, minMax float64) *Chart {
	values := make([]*float64, 0, len(days))
	rows := make([][]string, 0, len(days))
	head := []string{"Day", title}
	for _, day := range days {
		vals, cells := value(day)
		var v *float64
		if len(vals) > 0 {
			v = vals[0]
		}
		values = append(values, v)
		row := []string{formatChartDay(day, loc)}
		if len(cells) > 0 {
			row = append(row, cells[0])
		} else {
			row = append(row, "")
		}
		rows = append(rows, row)
	}
	path, axisMax := lineGeometry(values, 1)
	if minMax > 0 && axisMax < minMax {
		axisMax = minMax
		path, _ = lineGeometryWithMax(values, axisMax)
	}
	ch := &Chart{
		ID: id, Title: title, Height: "line", Path: path, HasData: true,
		YMax: format(axisMax), YMid: format(axisMax / 2), YZero: format(0),
		XLabels: xLabelTimes(days, loc), Head: head, Rows: rows,
	}
	return ch
}

func lineGeometryWithMax(values []*float64, axisMax float64) (string, float64) {
	if axisMax <= 0 {
		axisMax = 1
	}
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

func buildPosts(ms metricSet, now time.Time, loc *time.Location) []PostRow {
	type ranked struct {
		row  PostRow
		rate float64
	}
	posts := make([]ranked, 0, len(ms.posts))
	for _, p := range ms.posts {
		row := PostRow{ID: p.ID, URL: p.URL, Kind: kindWord(p.Kind), Link: cmp.Or(truncate(strings.Join(strings.Fields(p.pub.Text), " "), postChars), "Open on Threads"), Why: p.pub.Why}
		row.Label = outcomeWord(p.pub)
		if p.pub.Deleted {
			row.Label = "Deleted"
		}
		if !p.PostedAt.IsZero() {
			row.Age = formatAge(p.PostedAt, now)
			row.AgeTitle = formatFullDay(p.PostedAt, loc)
			row.PostedAt = p.PostedAt
		}
		samples := ms.all[p.ID]
		if len(samples) == 0 {
			posts = append(posts, ranked{row: row})
			continue
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i].MeasuredAt.Before(samples[j].MeasuredAt) })
		latest := samples[len(samples)-1]
		row.HasSample = true
		row.Views = formatCount(latest.Views)
		row.Likes = formatCount(latest.Likes)
		row.Replies = formatCount(latest.Replies)
		row.Reposts = formatCount(latest.Reposts)
		rate, ok := engagementRate(endCounts(latest))
		row.Rate = "—"
		if ok {
			row.Rate = formatPercent(rate)
		}
		row.views, row.rate = latest.Views, rate
		posts = append(posts, ranked{row: row, rate: rate})
	}
	sort.SliceStable(posts, func(i, j int) bool {
		if posts[i].rate != posts[j].rate {
			return posts[i].rate > posts[j].rate
		}
		return posts[i].row.ID > posts[j].row.ID
	})
	rows := make([]PostRow, len(posts))
	rated := []int{}
	for i, p := range posts {
		rows[i] = p.row
		if p.row.HasSample && p.rate > 0 {
			rated = append(rated, i)
		}
	}
	if len(rated) >= 2 {
		rows[rated[0]].Mark = "best"
		rows[rated[len(rated)-1]].Mark = "worst"
	}
	return rows
}

func kindWord(kind string) string {
	if kind == "reply" {
		return "Reply"
	}
	return "Post"
}

func oneOfOK(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}

type kpiInput struct {
	posts, replies int
	days           int
	spentToday     float64
	budget         float64
	growth         *GrowthPanel
	followers      FollowersPanel
}

// buildKPIs lists the figures in reading order: the first three are the hero,
// then output counts, then today's spend.
func buildKPIs(in kpiInput) []KPI {
	views, rate, followers, viewsNote, rateNote := "—", "—", "—", "", ""
	if in.growth.HasMetrics {
		views, viewsNote = formatCount(in.growth.Views), in.growth.ViewsDelta
		rate, rateNote = in.growth.Rate, in.growth.RateDelta
	}
	var change string
	if in.followers.HasData {
		followers, change = in.followers.Now, in.followers.Gained
	}
	kpis := []KPI{
		{Label: fmt.Sprintf("Views in %d days", in.days), Value: views, Note: viewsNote},
		{Label: "Followers", Value: followers, Note: change},
		{Label: "Engagement", Value: rate, Note: rateNote},
		{Label: "Posts", Value: formatInt(in.posts)},
		{Label: "Replies", Value: formatInt(in.replies)},
		{Label: "Spend today", Value: formatUSD(in.spentToday) + " / " + formatBudget(in.budget)},
	}
	for i := range kpis {
		kpis[i].Tone = noteTone(kpis[i].Note)
	}
	return kpis
}

func noteTone(note string) string {
	switch {
	case strings.HasPrefix(note, "up ") || strings.HasPrefix(note, "+"):
		return "up"
	case strings.HasPrefix(note, "down ") || strings.HasPrefix(note, "-"):
		return "down"
	}
	return ""
}

// lineDiff is a longest-common-subsequence line diff of old against new.
func lineDiff(old, cur string) []DiffLine {
	a, b := splitLines(old), splitLines(cur)
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out = append(out, DiffLine{" ", a[i]})
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, DiffLine{"+", b[j]})
			j++
		default:
			out = append(out, DiffLine{"-", a[i]})
			i++
		}
	}
	return out
}

func splitLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}
