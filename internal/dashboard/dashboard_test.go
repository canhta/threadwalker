package dashboard_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/dashboard"
	"github.com/canhta/threadwalker/internal/store"
)

func TestPageProjectsOnlySelectedSafeMetadata(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "page.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const private = "@foreign_handle sk-synthetic-secret SYSTEM PROMPT private payload"
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"alpha", "beta", "unconfigured-private"} {
		if err = st.EnsureAgent(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB().Exec(`UPDATE agents SET self_note=?,next_wake_at=? WHERE id=?`, private, now.Add(time.Hour).Format(time.RFC3339), id); err != nil {
			t.Fatal(err)
		}
	}
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO wakes(agent_id,started_at,ended_at,end_reason,kind,steps,tokens_in,tokens_out,cost_usd) VALUES('alpha',?,?,'completed','agent',3,120,25,0.25)`, []any{now.Format(time.RFC3339), now.Format(time.RFC3339)}},
		{`INSERT INTO ledger(agent_id,day,cost_usd) VALUES('alpha','2026-11-11',0.25)`, nil},
		{`INSERT INTO actions(agent_id,kind,text,target_url,target_username,hypothesis_ids_json,dry_run,posted_at,thread_url) VALUES('alpha','post',?,?,?,?,0,?,?)`, []any{private, private, private, private, now.Format(time.RFC3339), "https://www.threads.net/@own_alpha/post/ABC123"}},
		{`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES('alpha','reply',?,0,?,?)`, []any{private, now.Format(time.RFC3339), "https://www.threads.net/@foreign_handle/post/XYZ"}},
		{`INSERT INTO playbook(agent_id,text,status,n,successes,created_at) VALUES('alpha',?,'testing',4,2,?)`, []any{private, private}},
		{`INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,result_json,blocked_by,at) VALUES('alpha',1,'schedule_next_wake',?,?,?,?)`, []any{`{"at":"2026-11-11T13:00:00Z","private":"` + private + `"}`, `{"outcome":"success","bytes":34,"private":"` + private + `"}`, private, now.Format(time.RFC3339)}},
		{`INSERT INTO notes(agent_id,text,created_at) VALUES('alpha',?,?)`, []any{private, private}},
		{`INSERT INTO contacts(agent_id,username,reason) VALUES('alpha',?,?)`, []any{private, private}},
		{`INSERT INTO owner_msgs(agent_id,direction,text,at) VALUES('alpha',?,?,?)`, []any{private, private, private}},
	}
	for _, s := range statements {
		if _, err = st.DB().Exec(s.sql, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	h, err := dashboard.New(dashboard.Options{DB: st.DB(), Now: func() time.Time { return now }, Agents: []dashboard.Agent{{ID: "alpha", Username: "own_alpha", DailyBudgetUSD: 2}, {ID: "beta", DailyBudgetUSD: 1}, {ID: "missing", DailyBudgetUSD: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.Page(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(page)
	for _, s := range []string{"foreign_handle", "sk-synthetic-secret", "SYSTEM PROMPT", "private payload", "unconfigured-private"} {
		if strings.Contains(string(b), s) {
			t.Fatalf("public DTO leaked %q", s)
		}
	}
	if len(page.Agents) != 3 || page.Agents[0].SpentUSD != 0.25 || page.Agents[0].RemainingUSD != 1.75 || page.Agents[2].Status != "not_started" {
		t.Fatalf("incorrect page: %s", b)
	}
	if len(page.Agents[0].Actions) != 2 || page.Agents[0].Actions[0].URL != "" || page.Agents[0].Actions[1].URL != "https://www.threads.net/@own_alpha/post/ABC123" {
		t.Fatalf("action binding: %s", b)
	}
	if page.Agents[0].Tools[0].Detail != "" {
		t.Fatalf("nonallowlisted JSON accepted: %s", b)
	}
	var n int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("request initialized missing agent: %d %v", n, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = h.Page(cancelled); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestHTTPResponsesNeverReflectPrivateStateOrRequests(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "privacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const private = "foreign_handle sk-synthetic-secret SYSTEM-PROMPT"
	if err = st.EnsureAgent(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`UPDATE agents SET self_note=?,next_wake_at=?`, private, private); err != nil {
		t.Fatal(err)
	}
	h, err := dashboard.New(dashboard.Options{DB: st.DB(), Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"GET", "/", 200}, {"HEAD", "/", 200}, {"GET", "/dashboard.css", 200}, {"HEAD", "/dashboard.css", 200}, {"GET", "/" + url.PathEscape(private), 404}, {"POST", "/", 405}, {"PUT", "/dashboard.css", 405}, {"GET", "/?q=" + url.QueryEscape(private), 400}, {"GET", "//" + url.PathEscape(private), 404}} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(private))
			req.Header.Set("Authorization", private)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.code {
				t.Fatalf("status %d want %d", rr.Code, tc.code)
			}
			response := rr.Body.String() + fmt.Sprint(rr.Header())
			for _, sentinel := range strings.Fields(private) {
				if strings.Contains(response, sentinel) {
					t.Fatalf("response leaked %q", sentinel)
				}
			}
			if tc.method == "HEAD" && rr.Body.Len() != 0 {
				t.Fatal("HEAD returned body")
			}
			if rr.Header().Get("Content-Security-Policy") == "" || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing response protections")
			}
		})
	}
	if _, err = st.DB().Exec(`DROP TABLE wakes`); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 503 || strings.Contains(rr.Body.String(), "wakes") {
		t.Fatalf("unsafe DB error: %d %s", rr.Code, rr.Body.String())
	}
}

func TestPublicationLinksRequireConfiguredOwnAccountAndCanonicalPath(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "links.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, err := dashboard.New(dashboard.Options{DB: st.DB(), Now: time.Now, Agents: []dashboard.Agent{{ID: "alpha", Username: "own_alpha", DailyBudgetUSD: 1}}})
	if err != nil {
		t.Fatal(err)
	}
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
		if _, err = st.DB().Exec(`DELETE FROM actions`); err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES('alpha','post','private',0,'2026-11-11T00:00:00Z',?)`, tc.url); err != nil {
			t.Fatal(err)
		}
		p, err := h.Page(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Agents[0].Actions[0].URL; (got != "") != tc.allowed {
			t.Errorf("URL %s got %q allowed %v", tc.url, got, tc.allowed)
		}
	}
}

func TestToolMetadataAllowlistAndBoundedHistory(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, err := dashboard.New(dashboard.Options{DB: st.DB(), Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, result, blocked, outcome, detail string }{
		{"read_own_metrics", `{"outcome":"success","bytes":42}`, "", "success", "Result: 42 bytes"},
		{"schedule_next_wake", `{"outcome":"error","bytes":0}`, "", "error", "Result: 0 bytes"},
		{"search", `{"outcome":"success","bytes":42}`, "", "success", ""},
		{"private-tool", `{"outcome":"success","bytes":42}`, "", "success", ""},
		{"read_own_metrics", `{"outcome":"sk-secret","bytes":42}`, "", "unknown", ""},
		{"read_own_metrics", `{"outcome":"success","bytes":-1}`, "", "success", ""},
		{"read_own_metrics", `{"outcome":"success","bytes":1099511627776}`, "", "success", ""},
		{"read_own_metrics", `{"outcome":"success","bytes":"sk-secret"}`, "", "unknown", ""},
		{"read_own_metrics", `{"outcome":"success","bytes":42,"text":"foreign_handle"}`, "", "unknown", ""},
		{"read_own_metrics", `{"outcome":"success","bytes":42} {}`, "", "unknown", ""},
		{"read_own_metrics", `not-json`, "", "unknown", ""},
		{"read_own_metrics", ``, "", "pending", ""},
		{"read_own_metrics", `{"outcome":"success","bytes":42}`, "foreign_handle", "blocked", ""},
		{"post", `{"outcome":"unknown_assumed_sent","retry":false}`, "", "unknown_assumed_sent", ""},
		{"read_own_metrics", `{"outcome":"interrupted_read","retry":true}`, "", "interrupted_read", ""},
	} {
		if _, err = st.DB().Exec(`DELETE FROM tool_calls`); err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB().Exec(`INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,result_json,blocked_by,at) VALUES('alpha',1,?,'private',?,?,?)`, tc.name, tc.result, tc.blocked, "2026-11-11T01:00:00+01:00"); err != nil {
			t.Fatal(err)
		}
		p, err := h.Page(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		tool := p.Agents[0].Tools[0]
		if tool.Outcome != tc.outcome || tool.Detail != tc.detail || tool.At != "2026-11-11T00:00:00Z" {
			t.Errorf("%+v got %+v", tc, tool)
		}
	}
	if _, err = st.DB().Exec(`DELETE FROM tool_calls`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 110; i++ {
		if _, err = st.DB().Exec(`INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,at) VALUES('alpha',1,'post','private','invalid')`); err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB().Exec(`INSERT INTO playbook(agent_id,text,status,n,successes,created_at) VALUES('alpha','private','testing',1,1,'invalid')`); err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB().Exec(`INSERT INTO wakes(agent_id,started_at,ended_at,end_reason) VALUES('alpha','invalid','invalid','foreign_handle')`); err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES('alpha','post','private',0,'invalid','unsafe')`); err != nil {
			t.Fatal(err)
		}
	}
	p, err := h.Page(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := p.Agents[0]
	if len(a.Tools) != 20 || len(a.Wakes) != 20 || len(a.Actions) != 20 || len(a.Playbook) != 100 || a.Actions[0].ID <= a.Actions[19].ID || a.Wakes[0].EndReason != "unknown" {
		t.Fatalf("history bounds or sanitization: %+v", a)
	}
}

func TestDashboardFailsClosedOnInvalidNumericMetadataAndQueryErrors(t *testing.T) {
	for _, table := range []string{"agents", "ledger", "wakes", "actions", "playbook", "tool_calls"} {
		t.Run(table, func(t *testing.T) {
			st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "query.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			h, err := dashboard.New(dashboard.Options{DB: st.DB(), Now: time.Now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.DB().Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			p, err := h.Page(context.Background())
			if err == nil || len(p.Agents) != 0 || strings.Contains(err.Error(), table) {
				t.Fatalf("partial or raw query error: %+v %v", p, err)
			}
		})
	}
	for _, statement := range []string{
		`INSERT INTO ledger(agent_id,day,cost_usd) VALUES('alpha','2026-11-11',-1)`,
		`INSERT INTO wakes(agent_id,started_at,steps) VALUES('alpha','invalid',-1)`,
		`INSERT INTO playbook(agent_id,text,status,n,successes,created_at) VALUES('alpha','private','testing',1,2,'invalid')`,
		`INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,at) VALUES('alpha',-1,'post','private','invalid')`,
	} {
		t.Run(statement[:18], func(t *testing.T) {
			st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "invalid.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if _, err = st.DB().Exec(statement); err != nil {
				t.Fatal(err)
			}
			h, err := dashboard.New(dashboard.Options{DB: st.DB(), Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
			if rr.Code != 503 || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("unsafe invalid metadata %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestDashboardValidatesPublicOptionsAndOwnsAgentSelection(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "options.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, options := range []dashboard.Options{
		{}, {DB: st.DB(), Now: time.Now}, {DB: st.DB(), Now: time.Now, Agents: make([]dashboard.Agent, 101)},
		{DB: st.DB(), Now: time.Now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 0}}},
		{DB: st.DB(), Now: time.Now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: 1}, {ID: "alpha", DailyBudgetUSD: 1}}},
		{DB: st.DB(), Now: time.Now, Agents: []dashboard.Agent{{ID: strings.Repeat("a", 129), DailyBudgetUSD: 1}}},
		{DB: st.DB(), Now: time.Now, Agents: []dashboard.Agent{{ID: "alpha", DailyBudgetUSD: math.Inf(1)}}},
	} {
		if _, err = dashboard.New(options); err == nil {
			t.Fatal("accepted invalid public options")
		}
	}
	agents := []dashboard.Agent{{ID: `<script>alert(1)</script>`, DailyBudgetUSD: 1}}
	h, err := dashboard.New(dashboard.Options{DB: st.DB(), Now: time.Now, Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	agents[0].ID = "mutable-private"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "<script>") || strings.Contains(rr.Body.String(), "mutable-private") || !strings.Contains(rr.Body.String(), "&lt;script&gt;") {
		t.Fatal("agent selection mutation or unescaped template metadata")
	}
}
