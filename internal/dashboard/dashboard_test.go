package dashboard_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/dashboard"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
)

func get(h *dashboard.Handler, target string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", target, nil))
	return rr
}

func getWithAuth(h *dashboard.Handler, target, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func mustPage(t *testing.T, h *dashboard.Handler) dashboard.Page {
	t.Helper()
	p, err := h.Page(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPageProjectsOnlySelectedSafeMetadata(t *testing.T) {
	const private = "@foreign_handle sk-synthetic-secret SYSTEM PROMPT private payload"
	snap := measured()
	// A foreign-link action and its samples must never become a public row.
	snap.Publications = append([]store.DashboardPublication{pub(2, "reply", ago(3*day), "https://www.threads.net/@foreign_handle/post/XYZ")}, snap.Publications...)
	snap.Metrics = append(snap.Metrics, sample(2, ago(2*day), 999, 99, 9, 9))
	snap.MemoryIndex = []string{private}
	snap.NoteCount = 1
	snap.Notes = []store.DashboardNote{{Text: private, CreatedAt: fixedNow()}}
	snap.Playbook = store.DashboardPlaybook{Testing: 1, Samples: 4}
	snap.Hypotheses = []store.DashboardHypothesis{{ID: 1, Status: "testing", N: 4, Text: "an idea", Decision: &store.DashboardDecision{Status: "rejected", Reason: private}}}
	snap.ToolCalls = []store.DashboardToolCall{call(1, 1, "search", private, "")}
	snap.Wakes = []store.DashboardWake{finishedWake(1, "agent", "provider_error", 3)}
	snap.Ledger = []store.DashboardLedgerDay{{Day: fixedNow().Format("2006-01-02"), CostUSD: 0.25}}
	snap.Trends = []store.DashboardTrend{{Title: "Synthetic public topic", Source: "hacker_news", Rank: 1, ObservedAt: fixedNow()}}
	snap.NextWake = fixedNow().Add(time.Hour)

	src := newSource(snap)
	h := newHandler(src)
	page := mustPage(t, h)
	b, _ := json.Marshal(page)
	for _, s := range []string{"foreign_handle", "sk-synthetic-secret", "SYSTEM PROMPT", "private payload", "XYZ"} {
		if strings.Contains(string(b), s) {
			t.Fatalf("public view leaked %q", s)
		}
	}
	if page.Agent.ID != "alpha" || len(page.Agents) != 3 || page.Agent.NextWake == "" {
		t.Fatalf("agent projection: %+v", page.Agent)
	}
	if page.KPIs[3].Value != "1" {
		t.Fatalf("published count: %+v", page.KPIs)
	}
	if page.Growth == nil || len(page.Growth.Posts) != 1 || page.Growth.Posts[0].URL != "https://www.threads.net/@own_alpha/post/ABC123" {
		t.Fatalf("post projection: %+v", page.Growth)
	}
	if page.Learned.Notes != 1 || len(page.Learned.NotesRows) != 0 || page.Learned.MemoryIndex != "" {
		t.Fatalf("content leaked without opt in: %+v", page.Learned)
	}
	if len(page.Learned.Hypotheses) != 1 || page.Learned.Hypotheses[0].Text != "an idea" || page.Learned.Hypotheses[0].Evidence != "" {
		t.Fatalf("hypothesis evidence leaked: %+v", page.Learned.Hypotheses)
	}
	if page.Content {
		t.Fatal("content mode on by default")
	}
	if page.Growth.Views != 300 || page.Growth.Likes != 30 || page.Growth.Replies != 6 || page.Growth.Reposts != 2 {
		t.Fatalf("growth totals: %+v", page.Growth)
	}
	if page.Growth.Rate != "12.7%" {
		t.Fatalf("engagement rate: %q", page.Growth.Rate)
	}
	if page.Return.CostPer1000 == "" {
		t.Fatalf("return panel missing: %+v", page.Return)
	}
	if q := src.queries[0]; q.AgentID != "alpha" || q.Content || !q.Now.Equal(fixedNow()) || !q.From.Equal(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("snapshot query: %+v", q)
	}
	if _, err := h.Page(cancelled()); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestHTTPRoutesQueriesAndProtections(t *testing.T) {
	src := newSource(measured())
	h := newHandler(src)
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/", 200},
		{"HEAD", "/", 200},
		{"GET", "/dashboard.css", 200},
		{"HEAD", "/dashboard.css", 200},
		{"GET", "/fonts/bvp-latin-400.woff2", 200},
		{"GET", "/fonts/../dashboard.css", 404},
		{"GET", "/log", 404},
		{"GET", "/growth", 404},
		{"POST", "/", 405},
		{"PUT", "/dashboard.css", 405},
		{"GET", "/?agent=alpha&range=14", 200},
		{"GET", "/?agent=beta", 200},
		{"GET", "/?agent=nope", 400},
		{"GET", "/?range=8", 400},
		{"GET", "/?range=07", 400},
		{"GET", "/?q=private", 200},
		{"GET", "/?fbclid=ZgZXh0bgNhZW0CMTEA_aem_IKtyV9WgtnjCUXEBp6Hwyw", 200},
		{"GET", "/?utm_source=threads&utm_medium=social&agent=alpha", 200},
		{"GET", "/?agent=alpha&agent=beta", 400},
		{"GET", "/?" + strings.Repeat("agent=alpha&", 40), 400},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			before := len(src.queries)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != tc.code {
				t.Fatalf("status %d want %d: %s", rr.Code, tc.code, rr.Body.String())
			}
			if tc.code == 400 && len(src.queries) != before {
				t.Fatal("rejected request read the source")
			}
			if tc.method == "HEAD" && rr.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
			if rr.Header().Get("Content-Security-Policy") == "" || !strings.Contains(rr.Header().Get("Content-Security-Policy"), "font-src 'self'") {
				t.Fatal("missing response protections")
			}
			if tc.method == "GET" && tc.code == 200 && strings.HasPrefix(tc.path, "/") && tc.path != "/dashboard.css" && !strings.HasPrefix(tc.path, "/fonts/") {
				if !strings.Contains(rr.Body.String(), "Threadz") {
					t.Fatal("page body missing")
				}
				if rr.Header().Get("Cache-Control") != "public, max-age=10" {
					t.Fatalf("cache header: %q", rr.Header().Get("Cache-Control"))
				}
			}
		})
	}
	rr := get(h, "/")
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("If-None-Match", etag)
	again := httptest.NewRecorder()
	h.ServeHTTP(again, req)
	if again.Code != 304 || again.Body.Len() != 0 {
		t.Fatalf("conditional request: %d %q", again.Code, again.Body.String())
	}
	// A cached page survives a source failure; a different cache key does not.
	src.err = errors.New("no such table: wakes")
	if rr = get(h, "/"); rr.Code != 200 {
		t.Fatalf("cached page after failure: %d %s", rr.Code, rr.Body.String())
	}
	if rr = get(h, "/?range=30"); rr.Code != 503 || strings.Contains(rr.Body.String(), "wakes") {
		t.Fatalf("unsafe source error: %d %s", rr.Code, rr.Body.String())
	}
}

func TestPublicationLinksRequireConfiguredOwnAccountAndCanonicalPath(t *testing.T) {
	tests := []struct {
		url     string
		allowed bool
	}{
		{"https://www.threads.net/@own_alpha/post/ABC123", true}, {"https://threads.com/@own_alpha/post/XYZ_12-3", true},
		{"https://threads.net/@foreign_handle/post/ABC", false}, {"https://evil.invalid/@own_alpha/post/ABC", false}, {"https://threads.net.evil.invalid/@own_alpha/post/ABC", false},
		{"https://user:secret@threads.net/@own_alpha/post/ABC", false}, {"https://threads.net/@own_alpha/post/ABC?q=secret", false}, {"https://threads.net/@own_alpha/post/ABC#foreign_handle", false},
		{"javascript:alert(1)", false}, {"//threads.net/@own_alpha/post/ABC", false}, {"https://threads.net/@own_alpha/post/ABC/foreign_handle", false}, {"https://threads.net/@own_alpha/post/ABC/", false},
		{"https://threads.net/@own_alpha/post/%41BC", false}, {"https://threads.net:443/@own_alpha/post/ABC", false}, {"https://threads.net/@own_alpha/post/ABC?", false},
	}
	for _, tc := range tests {
		snap := started()
		snap.Publications = []store.DashboardPublication{pub(1, "post", ago(2*day), tc.url)}
		snap.Metrics = []store.DashboardMetric{sample(1, ago(day), 10, 1, 0, 0)}
		p := mustPage(t, newHandler(newSource(snap)))
		rows := append([]dashboard.PostRow{}, p.Growth.Posts...)
		got := ""
		for _, row := range rows {
			got = row.URL
		}
		if (got != "") != tc.allowed {
			t.Errorf("URL %s got %q allowed %v", tc.url, got, tc.allowed)
		}
		if p.KPIs[3].Value != map[bool]string{true: "1", false: "0"}[tc.allowed] {
			t.Errorf("URL %s posted count %q", tc.url, p.KPIs[3].Value)
		}
		if !tc.allowed && strings.Contains(fmt.Sprint(p), "foreign_handle") {
			t.Errorf("foreign handle reflected for %s", tc.url)
		}
	}
}

func TestBoundsAndPlainEnumLabels(t *testing.T) {
	snap := started()
	for i := 0; i < 20; i++ {
		w := finishedWake(int64(i+1), "agent", "mystery_code", 1)
		w.StartedAt = ago(time.Duration(i) * time.Minute)
		snap.Wakes = append(snap.Wakes, w)
	}
	for i := 0; i < 60; i++ {
		snap.ToolCalls = append(snap.ToolCalls, call(int64(i+1), 1, "private-tool", `{"outcome":"sk-secret"}`, "private_code"))
	}
	for i := 0; i < 20; i++ {
		snap.Hypotheses = append(snap.Hypotheses, store.DashboardHypothesis{ID: int64(20 - i), Status: "testing", N: 1, Text: "private"})
	}
	for i := 0; i < 40; i++ {
		snap.Notes = append(snap.Notes, store.DashboardNote{Text: "private", CreatedAt: fixedNow()})
	}
	snap.NoteCount = 60
	for i := 0; i < 60; i++ {
		snap.Publications = append(snap.Publications, pub(int64(60-i), "post", ago(time.Duration(i)*time.Hour), ownLink(idCode("P", i))))
		snap.Metrics = append(snap.Metrics, sample(int64(60-i), ago(time.Duration(i)*time.Minute), int64(i), 1, 0, 0))
	}
	h := newHandler(newSource(snap), func(o *dashboard.Options) { o.Content = true; o.ContentToken = "token-123456789" })
	rr := getWithAuth(h, "/", "token-123456789")
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Older runs") || !strings.Contains(rr.Body.String(), "Older ideas") || !strings.Contains(rr.Body.String(), "Older notes") {
		t.Fatal("older bounded rows not present")
	}
	page := mustPage(t, newHandler(newSource(snap)))
	if len(page.Wakes.Rows) != 5 || len(page.Wakes.Older) != 15 {
		t.Fatalf("wake bounds: %d + %d", len(page.Wakes.Rows), len(page.Wakes.Older))
	}
	if len(page.Learned.Hypotheses) != 10 || len(page.Learned.OlderHypotheses) != 10 {
		t.Fatalf("idea bounds: %d + %d", len(page.Learned.Hypotheses), len(page.Learned.OlderHypotheses))
	}
	// The first page is one step; the panel knows the whole bounded total.
	if got, total := len(page.Growth.Posts), page.Growth.Total; got != 6 || total != 42 {
		t.Fatalf("post bound: %d shown of %d", got, total)
	}
	if page.KPIs[3].Value != "60" {
		t.Fatalf("posted count follows the list bound: %+v", page.KPIs[3])
	}
	if len(page.ToolCalls.Rows) > 8 || page.ToolCalls.Rows[0].Name != "Other" || page.ToolCalls.Rows[0].Blocked != 60 {
		t.Fatalf("tool projection: %+v", page.ToolCalls.Rows)
	}
	if page.Wakes.Rows[0].EndReason != "Stopped" || page.Wakes.Rows[0].EndCode != "stopped" {
		t.Fatalf("end reason mapping: %+v", page.Wakes.Rows[0])
	}
}

func TestContentModeRequiresTokenAndNeverLeaksIt(t *testing.T) {
	const token = "synthetic-token-123456"
	const hostile = `<script>alert("x")</script> private lesson`
	snap := measured()
	snap.NoteCount = 1
	snap.Notes = []store.DashboardNote{{Text: hostile, CreatedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}}
	snap.Playbook = store.DashboardPlaybook{Testing: 1, Samples: 2}
	snap.Hypotheses = []store.DashboardHypothesis{{ID: 1, Status: "testing", N: 2, Text: "a private idea"}}
	snap.MemoryIndex = []string{"private self note"}
	snap.Drafts = 1
	snap.ToolCalls = []store.DashboardToolCall{call(1, 1, "post", `{"outcome":"success","text":"sk-content-secret"}`, "")}
	src := newSource(snap)
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	h := newHandler(src, func(o *dashboard.Options) {
		o.Content = true
		o.ContentToken = token
		o.Logger = logger
	})

	rr := get(h, "/")
	if rr.Code != 401 || strings.Contains(rr.Body.String(), "private lesson") {
		t.Fatalf("unauthenticated content: %d %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing challenge")
	}
	rr = getWithAuth(h, "/", token)
	if rr.Code != 200 {
		t.Fatalf("token page: %d %s", rr.Code, rr.Body.String())
	}
	if q := src.queries[len(src.queries)-1]; !q.Content {
		t.Fatalf("content page did not ask for content: %+v", q)
	}
	if rr.Header().Get("Cache-Control") != "private, no-store" || rr.Header().Get("ETag") != "" {
		t.Fatalf("private cache headers: %q %q", rr.Header().Get("Cache-Control"), rr.Header().Get("ETag"))
	}
	for _, want := range []string{"a private idea", "private self note", "&lt;script&gt;", "Testing, tried 2 times"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("content missing %q", want)
		}
	}
	if strings.Contains(rr.Body.String(), "<script>alert") {
		t.Fatal("hostile content not escaped")
	}
	for _, forbidden := range []string{"private draft text", "private args", "sk-content-secret"} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("content mode leaked %q", forbidden)
		}
	}

	form := url.Values{"token": {"wrong-token"}}
	req := httptest.NewRequest("POST", "/unlock", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, req)
	if bad.Code != 401 {
		t.Fatalf("wrong token: %d", bad.Code)
	}
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "wrong-token") {
		t.Fatal("token reached the logs")
	}

	form = url.Values{"token": {token}}
	req = httptest.NewRequest("POST", "/unlock", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, req)
	if ok.Code != 303 || !strings.Contains(ok.Header().Get("Set-Cookie"), "tw_dashboard=") {
		t.Fatalf("unlock: %d %q", ok.Code, ok.Header().Get("Set-Cookie"))
	}
	cookieReq := httptest.NewRequest("GET", "/", nil)
	for _, c := range ok.Result().Cookies() {
		cookieReq.AddCookie(c)
	}
	withCookie := httptest.NewRecorder()
	h.ServeHTTP(withCookie, cookieReq)
	if withCookie.Code != 200 || !strings.Contains(withCookie.Body.String(), "a private idea") {
		t.Fatalf("cookie access: %d", withCookie.Code)
	}

	// Content responses are not served from the public cache.
	src.err = errors.New("no such table: wakes")
	if rr = getWithAuth(h, "/", token); rr.Code != 503 || strings.Contains(rr.Body.String(), "private") {
		t.Fatalf("content cached or leaked: %d %s", rr.Code, rr.Body.String())
	}
	public := newHandler(newSource(snap))
	if rr = get(public, "/unlock"); rr.Code != 404 {
		t.Fatalf("unlock exposed without content mode: %d", rr.Code)
	}
	if rr = get(public, "/"); strings.Contains(rr.Body.String(), "a private idea") || strings.Contains(rr.Body.String(), "private self note") {
		t.Fatal("content present without the flag")
	}
}

func TestContentModeProjectsDecisionEvidenceAndNotes(t *testing.T) {
	snap := started()
	snap.NoteCount = 2
	snap.Notes = []store.DashboardNote{{Text: "dated", CreatedAt: time.Date(2026, 11, 12, 10, 5, 0, 0, time.UTC)}, {Text: "undated"}}
	snap.Playbook = store.DashboardPlaybook{Established: 2}
	snap.Hypotheses = []store.DashboardHypothesis{
		{ID: 2, Status: "established", DecidedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), Text: "dated change", Outcomes: 3,
			Decision: &store.DashboardDecision{Status: "established", Reason: "beat control", At: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)}},
		{ID: 1, Status: "established", Text: "undated change", Decision: &store.DashboardDecision{Status: "rejected"}},
	}
	h := newHandler(newSource(snap), func(o *dashboard.Options) { o.Content = true; o.ContentToken = "token-123456789" })
	body := getWithAuth(h, "/", "token-123456789").Body.String()
	for _, want := range []string{"Changed to Adopted on 10 Nov: beat control", "Changed to Dropped", "12 Nov 10:05", "undated change"} {
		if !strings.Contains(body, want) {
			t.Fatalf("content page missing %q", want)
		}
	}
}

func TestEmptyDryRunAndPopulatedStates(t *testing.T) {
	h := newHandler(newSource(started()))
	body := get(h, "/?agent=beta").Body.String()
	for _, want := range []string{"No data yet.", "None yet.", "No results yet."} {
		if !strings.Contains(body, want) {
			t.Fatalf("dry-run page missing %q", want)
		}
	}
	if strings.Contains(body, "n/a") || strings.Contains(body, "0.0%") {
		t.Fatal("dry-run page shows fake measured values")
	}
	if strings.Contains(body, "Views per") {
		t.Fatal("an empty hero draws no chart")
	}
	if page := mustPage(t, h); page.Agent.Status != "not_started" && page.Agent.Status != "running" {
		t.Fatalf("status %q", page.Agent.Status)
	}
	notStarted := newHandler(newSource(store.DashboardSnapshot{}))
	if page := mustPage(t, notStarted); page.Agent.Status != "not_started" {
		t.Fatalf("unknown agent status %q", page.Agent.Status)
	}

	posted := started()
	posted.Publications = []store.DashboardPublication{pub(1, "post", ago(time.Hour), ownLink("WAIT1"))}
	body = get(newHandler(newSource(posted)), "/").Body.String()
	if strings.Contains(body, "Stats at 1h") || strings.Count(body, "No stats yet") != 1 {
		t.Fatal("posted page should state the stats schedule once")
	}

	full := started()
	full.Publications = []store.DashboardPublication{pub(2, "reply", ago(4*day), ownLink("WORST1")), pub(1, "post", ago(5*day), ownLink("BEST1"))}
	full.Metrics = []store.DashboardMetric{
		sample(2, ago(3*day), 900, 2, 0, 0),
		sample(1, ago(4*day), 100, 10, 1, 1), sample(1, ago(2*day), 1200, 120, 20, 10),
	}
	today := fixedNow().Format("2006-01-02")
	full.Ledger = []store.DashboardLedgerDay{{Day: today, CostUSD: 0.20}}
	full.Phases = []store.DashboardPhaseSpend{{Day: today, Phase: "agent", CostUSD: 0.15}, {Day: today, Phase: "study", CostUSD: 0.05}}
	full.Trends = []store.DashboardTrend{{Title: "Synthetic topic A", Source: "hacker_news", Rank: 1, ObservedAt: fixedNow()}}
	full.Playbook = store.DashboardPlaybook{Testing: 1, Samples: 4}
	full.Hypotheses = []store.DashboardHypothesis{{ID: 1, Status: "testing", N: 4}}
	full.NoteCount = 1
	body = get(newHandler(newSource(full)), "/").Body.String()
	for _, want := range []string{"1,200", "best", "worst", "Synthetic topic A", "12.5%", "$0.20", "Per 1,000 views", "ideas being tested"} {
		if !strings.Contains(body, want) {
			t.Fatalf("populated page missing %q", want)
		}
	}
	if strings.Contains(body, "a note") || strings.Contains(body, ">idea<") {
		t.Fatal("content rendered without opt in")
	}
}

func TestTopPostsRankByUnroundedRateAndMarkBestAndWorst(t *testing.T) {
	snap := started()
	// 1.04% and 1.00% both print as "1.0%"; only the number tells them apart.
	snap.Publications = []store.DashboardPublication{pub(3, "post", ago(3*day), ownLink("C")), pub(2, "post", ago(3*day), ownLink("B")), pub(1, "post", ago(3*day), ownLink("A"))}
	for i, text := range []string{"C", "B", "A"} {
		snap.Publications[i].Text = text
	}
	snap.Metrics = []store.DashboardMetric{
		sample(3, ago(day), 10000, 100, 0, 0),
		sample(2, ago(day), 10000, 104, 0, 0),
		sample(1, ago(day), 10000, 50, 0, 0),
	}
	page := mustPage(t, newHandler(newSource(snap)))
	var got []string
	for _, p := range page.Growth.Top {
		got = append(got, p.Link+":"+p.Rate+":"+p.Mark)
	}
	if want := "B:1.0%:best C:1.0%: A:0.5%:worst"; strings.Join(got, " ") != want {
		t.Fatalf("ranking %q want %q", strings.Join(got, " "), want)
	}
}

func TestFailClosedOnSourceErrorsAndInvalidNumbers(t *testing.T) {
	src := newSource(started())
	src.err = errors.New("reading dashboard snapshot: no such table: ledger")
	h := newHandler(src)
	p, err := h.Page(context.Background())
	if err == nil || len(p.Agents) != 0 || strings.Contains(err.Error(), "ledger") {
		t.Fatalf("partial or raw error: %+v %v", p, err)
	}
	if rr := get(h, "/"); rr.Code != 503 || strings.Contains(rr.Body.String(), "ledger") {
		t.Fatalf("unsafe error response: %d %s", rr.Code, rr.Body.String())
	}
	nan := math.NaN()
	for name, mutate := range map[string]func(*store.DashboardSnapshot){
		"negative ledger": func(s *store.DashboardSnapshot) {
			s.Ledger = []store.DashboardLedgerDay{{Day: "2026-11-12", CostUSD: -1}}
		},
		"nan ledger": func(s *store.DashboardSnapshot) {
			s.Ledger = []store.DashboardLedgerDay{{Day: "2026-11-12", CostUSD: nan}}
		},
		"negative phase": func(s *store.DashboardSnapshot) {
			s.Phases = []store.DashboardPhaseSpend{{Day: "2026-11-12", Phase: "study", CostUSD: -1}}
		},
		"negative steps":   func(s *store.DashboardSnapshot) { s.Wakes = []store.DashboardWake{{ID: 1, Steps: -1}} },
		"negative cost":    func(s *store.DashboardSnapshot) { s.Wakes = []store.DashboardWake{{ID: 1, CostUSD: -1}} },
		"missing wake id":  func(s *store.DashboardSnapshot) { s.Wakes = []store.DashboardWake{{}} },
		"bad tool wake id": func(s *store.DashboardSnapshot) { s.ToolCalls = []store.DashboardToolCall{call(1, -1, "post", "", "")} },
		"bad tool id":      func(s *store.DashboardSnapshot) { s.ToolCalls = []store.DashboardToolCall{call(0, 1, "post", "", "")} },
		"negative views": func(s *store.DashboardSnapshot) {
			s.Publications = []store.DashboardPublication{pub(1, "post", fixedNow(), ownLink("M1"))}
			s.Metrics = []store.DashboardMetric{sample(1, fixedNow(), -1, 0, 0, 0)}
		},
		"negative likes": func(s *store.DashboardSnapshot) {
			s.Publications = []store.DashboardPublication{pub(1, "post", fixedNow(), ownLink("M1"))}
			s.Metrics = []store.DashboardMetric{sample(1, fixedNow(), 0, -1, 0, 0)}
		},
		"unmeasured sample": func(s *store.DashboardSnapshot) {
			s.Publications = []store.DashboardPublication{pub(1, "post", fixedNow(), ownLink("M1"))}
			s.Metrics = []store.DashboardMetric{{ActionID: 1}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			snap := started()
			mutate(&snap)
			h := newHandler(newSource(snap))
			if rr := get(h, "/"); rr.Code != 503 || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("unsafe invalid metadata: %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestSamplesOutsideTheRangeStillFeedTheRankedList(t *testing.T) {
	snap := started()
	old := fixedNow().AddDate(0, -3, 0)
	snap.Publications = []store.DashboardPublication{
		pub(3, "post", ago(day), ownLink("NEW")),
		pub(2, "post", old, ownLink("OLD")),
		pub(1, "post", time.Time{}, ownLink("UNDATED")),
	}
	snap.Metrics = []store.DashboardMetric{
		sample(3, ago(time.Hour), 50, 5, 0, 0),
		sample(2, ago(time.Hour), 700, 7, 0, 0),
		sample(1, ago(time.Hour), 9, 0, 0, 0),
	}
	page := mustPage(t, newHandler(newSource(snap)))
	if page.Growth.Views != 50 {
		t.Fatalf("range totals counted older posts: %+v", page.Growth)
	}
	if len(page.Growth.Posts) != 3 || page.Growth.Unmeasured != 0 {
		t.Fatalf("ranked list: %+v", page.Growth)
	}
	for _, p := range page.Growth.Posts {
		if p.Link == "UNDATED" && p.Age != "" {
			t.Fatalf("undated post shows an age: %+v", p)
		}
	}
}

func TestValidatesOptionsAndEscapesAgentMetadata(t *testing.T) {
	src := newSource(started())
	now := time.Now
	for _, options := range []dashboard.Options{
		{}, {Source: src, Now: now}, {Source: src, Now: now, Agents: make([]dashboard.Agent, 101)},
		{Source: src, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}}},
		{Now: now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}}},
		{Source: src, Now: now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 0}}},
		{Source: src, Now: now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}, {ID: "alpha", DailyBudgetUSD: 1}}},
		{Source: src, Now: now, Agents: []dashboard.Agent{{ID: strings.Repeat("a", 129), DailyBudgetUSD: 1}}},
		{Source: src, Now: now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: math.Inf(1)}}},
		{Source: src, Now: now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1, Timezone: "Mars/Olympus"}}},
		{Source: src, Now: now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}}, Content: true},
	} {
		if _, err := dashboard.New(options); err == nil {
			t.Fatal("accepted invalid options")
		}
	}
	agents := []dashboard.Agent{{ID: `<script>alert(1)</script>`, DailyBudgetUSD: 1}, {ID: "other", DailyBudgetUSD: 1}}
	h, err := dashboard.New(dashboard.Options{Source: src, Now: fixedNow, Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	agents[0].ID = "mutable-private"
	body := get(h, "/").Body.String()
	if strings.Contains(body, "<script>") || strings.Contains(body, "mutable-private") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("agent selection mutation or unescaped metadata")
	}
}

func TestRateLimitAndCacheSize(t *testing.T) {
	h := newHandler(newSource(started()), func(o *dashboard.Options) {
		o.RatePerSec = 0.0001
		o.RateBurst = 1
	})
	if rr := get(h, "/"); rr.Code != 200 {
		t.Fatalf("first request: %d", rr.Code)
	}
	rr := get(h, "/?range=14")
	if rr.Code != 429 || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d %q", rr.Code, rr.Header().Get("Retry-After"))
	}
}

func TestStatsStepRowOnlyAfterAPost(t *testing.T) {
	snap := started()
	snap.Wakes = []store.DashboardWake{finishedWake(1, "agent", "completed", 4)}
	for i := 0; i < 2; i++ {
		snap.ToolCalls = append(snap.ToolCalls, call(int64(2*i+1), 1, "search", `{"outcome":"error"}`, ""), call(int64(2*i+2), 1, "read_own_metrics", `{"outcome":"error"}`, ""))
	}
	src := newSource(snap)
	h := newHandler(src)
	page := mustPage(t, h)
	if row := toolRow(page.ToolCalls.Rows, "Own stats"); row != nil {
		t.Fatalf("stats step row with nothing posted: %+v", row)
	}

	snap.Publications = []store.DashboardPublication{pub(1, "post", ago(30*time.Minute), ownLink("STATS1"))}
	src.snaps["alpha"] = snap
	page = mustPage(t, h)
	row := toolRow(page.ToolCalls.Rows, "Own stats")
	if row == nil || row.Failed != 2 {
		t.Fatalf("posted stats step row: %+v", row)
	}
}

func TestGuardRefusalIsCountedAsBlockedNotFailed(t *testing.T) {
	snap := started()
	snap.Wakes = []store.DashboardWake{finishedWake(1, "study", "completed", 1)}
	// A research run tried to message the owner while the phase is read-only.
	// The guard refused it and wrote no result.
	snap.ToolCalls = []store.DashboardToolCall{call(1, 1, "notify_owner", "", "phase_read_only")}
	src := newSource(snap)
	h := newHandler(src)
	page := mustPage(t, h)
	row := toolRow(page.ToolCalls.Rows, "Message owner")
	if row == nil || row.Blocked != 1 || row.Failed != 0 {
		t.Fatalf("blocked tool row: %+v", row)
	}
	if len(page.Wakes.Rows) != 1 {
		t.Fatalf("wake rows: %d", len(page.Wakes.Rows))
	}
	wt := wakeTool(page.Wakes.Rows[0].ToolSummary, "Message owner")
	if wt == nil || wt.Blocked != 1 || wt.Failed != 0 {
		t.Fatalf("run step summary: %+v", page.Wakes.Rows[0].ToolSummary)
	}
	body := get(h, "/").Body.String()
	visible := pageText(body)
	if !strings.Contains(visible, "Reason: Write in read-only phase 1") {
		t.Fatalf("page does not label the refusal as blocked: %q", visible)
	}
	if strings.Contains(visible, "phase_read_only") || strings.Contains(visible, "failed") {
		t.Fatalf("page exposed the raw code or called the refusal a failure: %q", visible)
	}
	if !strings.Contains(body, `title="phase_read_only"`) {
		t.Fatal("raw code is not kept inside the details title attribute")
	}

	// A real error stays separate from the refusal.
	snap.ToolCalls = append(snap.ToolCalls, call(2, 1, "search", `{"outcome":"error"}`, ""))
	src.snaps["alpha"] = snap
	page = mustPage(t, h)
	if row = toolRow(page.ToolCalls.Rows, "Message owner"); row == nil || row.Blocked != 1 || row.Failed != 0 {
		t.Fatalf("blocked row changed after a real failure: %+v", row)
	}
}

func TestEmptyOrInvalidResultJSONNeverBreaksThePage(t *testing.T) {
	snap := started()
	snap.Wakes = []store.DashboardWake{finishedWake(1, "agent", "completed", 3)}
	snap.ToolCalls = []store.DashboardToolCall{
		call(1, 1, "search", "", ""),
		call(2, 1, "fetch_trends", "not json at all", ""),
		call(3, 1, "notify_owner", "{broken", "phase_read_only"),
	}
	h := newHandler(newSource(snap))
	page := mustPage(t, h)
	if rr := get(h, "/"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "Tools") {
		t.Fatalf("page broke on an unusable result: %d %s", rr.Code, rr.Body.String())
	}
	// Unusable results are unfinished, never failed; the refusal keeps the
	// blocked label even though its result is not JSON.
	if row := toolRow(page.ToolCalls.Rows, "Search"); row == nil || row.Other != 1 || row.Failed != 0 {
		t.Fatalf("empty result row: %+v", row)
	}
	if row := toolRow(page.ToolCalls.Rows, "Trends"); row == nil || row.Other != 1 || row.Failed != 0 {
		t.Fatalf("invalid result row: %+v", row)
	}
	if row := toolRow(page.ToolCalls.Rows, "Message owner"); row == nil || row.Blocked != 1 || row.Failed != 0 {
		t.Fatalf("refusal with unusable result: %+v", row)
	}
}

func wakeTool(rows []dashboard.WakeTool, name string) *dashboard.WakeTool {
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

func toolRow(rows []dashboard.ToolStat, name string) *dashboard.ToolStat {
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

func TestRenderedCountsUseSingularForms(t *testing.T) {
	snap := started()
	snap.Wakes = []store.DashboardWake{finishedWake(1, "agent", "browser_error", 1)}
	snap.ToolCalls = []store.DashboardToolCall{call(1, 1, "search", `{"outcome":"success"}`, "")}
	snap.Publications = []store.DashboardPublication{pub(1, "post", ago(time.Hour), ownLink("ONE1"))}
	snap.Metrics = []store.DashboardMetric{sample(1, ago(30*time.Minute), 1, 0, 0, 0)}
	snap.Trends = []store.DashboardTrend{{Title: "A synthetic topic", Source: "hacker_news", Rank: 1, ObservedAt: fixedNow()}}
	snap.NoteCount = 1
	body := pageText(get(newHandler(newSource(snap)), "/").Body.String())
	for _, want := range []string{"1 hour"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing singular %q", want)
		}
	}
	for _, bad := range []string{"1 runs", "1 hours"} {
		if strings.Contains(body, bad) {
			t.Fatalf("page kept wrong plural %q", bad)
		}
	}
}

func TestRunsShowTimesAndLastRunState(t *testing.T) {
	snap := started()
	snap.NextWake = fixedNow().Add(2 * time.Hour)
	running := store.DashboardWake{ID: 2, StartedAt: ago(10 * time.Minute), Kind: "agent"}
	undated := store.DashboardWake{ID: 1, Ended: true, EndReason: "completed"}
	snap.Wakes = []store.DashboardWake{running, undated, finishedWake(3, "metrics", "completed", 2)}
	snap.LastWake = &running
	page := mustPage(t, newHandler(newSource(snap)))
	if page.Agent.LastWakeCode != "in_progress" || page.Agent.LastWake == "" || page.Agent.NextWake == "" {
		t.Fatalf("running agent: %+v", page.Agent)
	}
	if !page.Wakes.Rows[0].InProgress || page.Wakes.Rows[1].When != "—" || !strings.Contains(page.Wakes.Rows[2].WhenTitle, " to ") {
		t.Fatalf("wake rows: %+v", page.Wakes.Rows)
	}
	snap.LastWake = &store.DashboardWake{ID: 3, StartedAt: ago(time.Hour), Ended: true, EndReason: "mystery"}
	if page = mustPage(t, newHandler(newSource(snap))); page.Agent.LastWakeCode != "stopped" {
		t.Fatalf("unknown end reason: %+v", page.Agent)
	}
}

var pageTagRE = regexp.MustCompile(`<[^>]+>`)

func pageText(s string) string {
	return strings.Join(strings.Fields(pageTagRE.ReplaceAllString(s, " ")), " ")
}

func TestContentModeShowsCandidatesAndPenaltyKnowledge(t *testing.T) {
	snap := started()
	snap.Candidates = []store.KnowledgeCandidate{{Technique: "friday recap threads", SourceURL: "https://x.test/recap", CreatedAt: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}}
	h := newHandler(newSource(snap), func(o *dashboard.Options) { o.Content = true; o.ContentToken = "token-123456789" })
	body := getWithAuth(h, "/", "token-123456789").Body.String()
	for _, want := range []string{"friday recap threads", "https://x.test/recap", "Asking for comments or shares", "Could cost reach"} {
		if !strings.Contains(body, want) {
			t.Fatalf("content page missing %q", want)
		}
	}
	h = newHandler(newSource(snap))
	if body = get(h, "/").Body.String(); strings.Contains(body, "friday recap threads") {
		t.Fatal("candidate text leaked without content mode")
	}
}

func TestContentModeShowsEachStrategyRevisionAsADiff(t *testing.T) {
	snap := started()
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	snap.Strategy = []store.DashboardStrategy{
		{Version: 2, Text: "keep short roasts\nnew series <b>Q&A</b>", Citations: []string{"action:7"}, CreatedAt: at},
		{Version: 1, Text: "keep short roasts\ndrop hashtags", Citations: []string{"note:1"}, CreatedAt: at.Add(-24 * time.Hour)},
	}
	h := newHandler(newSource(snap), func(o *dashboard.Options) { o.Content = true; o.ContentToken = "token-123456789" })
	body := strings.ReplaceAll(getWithAuth(h, "/", "token-123456789").Body.String(), "&#43;", "+")
	for _, want := range []string{"v2", "action:7", ">  keep short roasts<", "+ new series &lt;b&gt;Q&amp;A&lt;/b&gt;", "- drop hashtags", "+ drop hashtags"} {
		if !strings.Contains(body, want) {
			t.Fatalf("strategy diff missing %q in %s", want, body)
		}
	}
	if public := get(newHandler(newSource(snap)), "/").Body.String(); strings.Contains(public, "keep short roasts") {
		t.Fatal("strategy text on the public page")
	}
}

func TestFailedStepsShowReasonCodes(t *testing.T) {
	snap := started()
	snap.Wakes = []store.DashboardWake{finishedWake(1, "agent", "completed", 4)}
	snap.ToolCalls = []store.DashboardToolCall{
		call(1, 1, "search", `{"outcome":"error","reason":"timeout"}`, ""),
		call(2, 1, "search", `{"outcome":"error","reason":"timeout"}`, ""),
		call(3, 1, "search", `{"outcome":"error","reason":"private text"}`, ""),
		call(4, 1, "memory_add", `{"outcome":"error","reason":"lesson_number_mismatch"}`, ""),
	}
	page := mustPage(t, newHandler(newSource(snap)))
	if wt := wakeTool(page.Wakes.Rows[0].ToolSummary, "Search"); wt == nil || wt.Failed != 3 || wt.Why != "timeout 2, failed 1" {
		t.Fatalf("failed search reasons: %+v", page.Wakes.Rows[0].ToolSummary)
	}
	if row := toolRow(page.ToolCalls.Rows, "Add memory"); row == nil || row.Failed != 0 || row.Blocked != 1 {
		t.Fatalf("a lesson refusal is a block, not a failure: %+v", row)
	}
}

func TestFaviconRoutesServeBrandIcons(t *testing.T) {
	h := newHandler(newSource(measured()))
	page := get(h, "/").Body.String()
	for path, contentType := range map[string]string{
		"/favicon.svg":          "image/svg+xml",
		"/favicon.ico":          "image/x-icon",
		"/apple-touch-icon.png": "image/png",
	} {
		rr := get(h, path)
		if rr.Code != 200 || rr.Body.Len() == 0 {
			t.Fatalf("%s: status %d, %d bytes", path, rr.Code, rr.Body.Len())
		}
		if got := rr.Header().Get("Content-Type"); got != contentType {
			t.Fatalf("%s: content type %q want %q", path, got, contentType)
		}
		if !strings.Contains(page, `href="`+path+`?v=`) {
			t.Fatalf("page does not link %s", path)
		}
	}
}
