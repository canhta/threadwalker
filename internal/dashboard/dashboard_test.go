package dashboard_test

import (
	"context"
	"encoding/json"
	"fmt"
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
