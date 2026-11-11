package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/dashboard"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
)

func validDashboardScenario(s string) bool {
	return s == "privacy" || s == "read_only" || s == "query_failure"
}

// These cases run the actual public projection, HTTP handler and SQLite reader;
// no model, external network, credential or user content is used as evidence.
func runDashboardCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) (r CaseResult) {
	started := time.Now()
	r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, EndReason: "completed", Reason: "dashboard_setup_failed", ToolCalls: []string{}}
	defer func() { r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6 }()
	fail := func(reason string) CaseResult { r.Reason = reason; r.Passed = false; return r }
	dir, err := os.MkdirTemp("", "threadwalker-dashboard-eval-*")
	if err != nil {
		return r
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "public.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		return r
	}
	defer st.Close()
	now := opts.Now().UTC()
	const private = "foreign_handle sk-synthetic-eval-key SYSTEM-PROMPT private-content"
	for _, id := range []string{"synthetic-dashboard", "synthetic-second", "unselected-agent"} {
		if err = st.EnsureAgent(ctx, id); err != nil {
			return r
		}
		if _, err = st.DB().ExecContext(ctx, `UPDATE agents SET self_note=?,next_wake_at=? WHERE id=?`, private, private, id); err != nil {
			return r
		}
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO wakes(agent_id,started_at,ended_at,end_reason) VALUES('synthetic-dashboard',?,?,?)`, []any{private, private, private}},
		{`INSERT INTO actions(agent_id,kind,text,target_url,target_username,hypothesis_ids_json,dry_run,posted_at,thread_url) VALUES('synthetic-dashboard','reply',?,?,?,?,0,?,?)`, []any{private, private, private, private, private, "https://threads.net/@foreign_handle/post/ABC"}},
		{`INSERT INTO playbook(agent_id,text,status,n,successes,created_at,decided_at) VALUES('synthetic-dashboard',?,'testing',4,2,?,?)`, []any{private, private, private}},
		{`INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,result_json,blocked_by,at) VALUES('synthetic-dashboard',1,?,?,?,?,?)`, []any{private, private, private, private, private}},
		{`INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,result_json,at) VALUES('synthetic-dashboard',1,'read_own_metrics',?,?,?)`, []any{`{"secret":"` + private + `"}`, `{"outcome":"success","bytes":12,"text":"` + private + `"}`, private}},
		{`INSERT INTO notes(agent_id,text,created_at) VALUES('synthetic-dashboard',?,?)`, []any{private, private}},
		{`INSERT INTO contacts(agent_id,username,reason) VALUES('synthetic-dashboard',?,?)`, []any{private, private}},
		{`INSERT INTO owner_msgs(agent_id,direction,text,at) VALUES('synthetic-dashboard',?,?,?)`, []any{private, private, private}},
		{`INSERT INTO ledger(agent_id,day,cost_usd) VALUES('synthetic-dashboard',?,0.25)`, []any{now.Format("2006-01-02")}},
	}
	for _, s := range statements {
		if _, err = st.DB().ExecContext(ctx, s.query, s.args...); err != nil {
			return r
		}
	}
	ro, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		return r
	}
	defer ro.Close()
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	h, err := dashboard.New(dashboard.Options{DB: ro.DB(), Now: opts.Now, Logger: logger, Agents: []dashboard.Agent{{ID: "synthetic-dashboard", DailyBudgetUSD: 2}, {ID: "synthetic-second", DailyBudgetUSD: 3}}})
	if err != nil {
		return r
	}
	switch c.DashboardScenario {
	case "privacy":
		page, e := h.Page(ctx)
		if e != nil || len(page.Agents) != 2 || page.Agents[0].SpentUSD != 0.25 || page.Agents[0].RemainingUSD != 1.75 {
			return fail("dashboard_projection_failed")
		}
		b, _ := json.Marshal(page)
		for _, sentinel := range append(strings.Fields(private), "unselected-agent") {
			if strings.Contains(string(b), sentinel) {
				return fail("dashboard_projection_leak")
			}
		}
		tests := []struct {
			method, path string
			code         int
		}{{"GET", "/", 200}, {"HEAD", "/", 200}, {"GET", "/dashboard.css", 200}, {"HEAD", "/dashboard.css", 200}, {"POST", "/", 405}, {"GET", "/" + url.PathEscape(private), 404}, {"GET", "/?q=" + url.QueryEscape(private), 400}}
		for _, tc := range tests {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(private))
			req.Header.Set("Authorization", private)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != tc.code {
				return fail("dashboard_http_status_failed")
			}
			for _, sentinel := range append(strings.Fields(private), "unselected-agent") {
				if strings.Contains(response.Body.String()+fmt.Sprint(response.Header()), sentinel) {
					return fail("dashboard_response_leak")
				}
			}
			if tc.method == "HEAD" && response.Body.Len() != 0 {
				return fail("dashboard_head_body")
			}
		}
	case "read_only":
		if _, err = ro.DB().ExecContext(ctx, `UPDATE agents SET status='killed'`); err == nil {
			return fail("dashboard_database_writable")
		}
		if _, err = ro.DB().ExecContext(ctx, `CREATE TABLE forbidden(id)`); err == nil {
			return fail("dashboard_migration_allowed")
		}
		for range 3 {
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
			if response.Code != 200 {
				return fail("dashboard_read_failed")
			}
		}
		var count int
		if st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE status='running'`).Scan(&count) != nil || count != 3 {
			return fail("dashboard_state_mutation")
		}
	case "query_failure":
		if _, err = st.DB().ExecContext(ctx, `DROP TABLE wakes`); err != nil {
			return r
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
		if response.Code != 503 || strings.Contains(response.Body.String(), "wakes") {
			return fail("dashboard_query_error_exposed")
		}
		r.EndReason = "persistence_error"
	default:
		return fail("dashboard_unknown_scenario")
	}
	for _, sentinel := range append(strings.Fields(private), "unselected-agent", "https://", "args_json", "result_json") {
		if strings.Contains(logs.String(), sentinel) {
			return fail("dashboard_log_leak")
		}
	}
	if !strings.Contains(logs.String(), logging.DashboardRequestFinished) {
		return fail("dashboard_logs_missing")
	}
	r.Passed, r.Reason = matches(c.Expect, r, "")
	return r
}
