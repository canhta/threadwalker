package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/canhta/threadz/internal/dashboard"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
)

func validDashboardScenario(s string) bool {
	return s == "privacy" || s == "read_only" || s == "query_failure"
}

// These cases run the actual public projection, HTTP handler and SQLite reader;
// no model, external network, credential or user content is used as evidence.
func runDashboardCase(ctx context.Context, c Case, p Profile, repeat int, opts Options) CaseResult {
	return runDashboardCaseCaptured(ctx, c, p, repeat, opts, nil)
}

func runDashboardCaseCaptured(ctx context.Context, c Case, p Profile, repeat int, opts Options, capture io.Writer) (r CaseResult) {
	run, err := beginCase(&r, c, p, repeat, "dashboard", "completed", "dashboard_setup_failed", capture)
	if err != nil {
		return r
	}
	defer run.end()
	fail := run.fail
	if err = run.Open(ctx, "public.db"); err != nil {
		return r
	}
	st, logs, logger := run.Store, &run.Logs, run.Logger
	path := run.Path("public.db")
	raw, err := openRaw(path)
	if err != nil {
		return r
	}
	defer raw.Close()
	now := opts.Now().UTC()
	const private = "foreign_handle sk-synthetic-eval-key SYSTEM-PROMPT private-content"
	for _, id := range []string{"synthetic-dashboard", "synthetic-second", "unselected-agent"} {
		if err = st.EnsureAgent(ctx, id); err != nil {
			return r
		}
		if _, err = raw.ExecContext(ctx, `UPDATE agents SET next_wake_at=? WHERE id=?`, private, id); err != nil {
			return r
		}
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO wakes(agent_id,started_at,ended_at,end_reason) VALUES('synthetic-dashboard',?,?,?)`, []any{private, private, private}},
		{`INSERT INTO actions(agent_id,kind,text,target_url,target_username,dry_run,posted_at,thread_url) VALUES('synthetic-dashboard','reply',?,?,?,0,?,?)`, []any{private, private, private, private, "https://threads.net/@foreign_handle/post/ABC"}},
		{`INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,result_json,blocked_by,at) VALUES('synthetic-dashboard',1,?,?,?,?,?)`, []any{private, private, private, private, private}},
		{`INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,result_json,at) VALUES('synthetic-dashboard',1,'read_own_metrics',?,?,?)`, []any{`{"secret":"` + private + `"}`, `{"outcome":"success","bytes":12,"text":"` + private + `"}`, private}},
		{`INSERT INTO contacts(agent_id,username,reason) VALUES('synthetic-dashboard',?,?)`, []any{private, private}},
		{`INSERT INTO owner_msgs(agent_id,direction,text,at) VALUES('synthetic-dashboard',?,?,?)`, []any{private, private, private}},
		{`INSERT INTO ledger_phases(agent_id,day,phase,cost_usd) VALUES('synthetic-dashboard',?,'agent',0.25)`, []any{now.Format("2006-01-02")}},
	}
	for _, s := range statements {
		if _, err = raw.ExecContext(ctx, s.query, s.args...); err != nil {
			return r
		}
	}
	ro, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		return r
	}
	defer ro.Close()
	h, err := dashboard.New(dashboard.Options{Source: ro, Now: opts.Now, Logger: logger, Agents: []dashboard.Agent{{ID: "synthetic-dashboard", DailyBudgetUSD: 2}, {ID: "synthetic-second", DailyBudgetUSD: 3}}})
	if err != nil {
		return r
	}
	switch c.Scenario {
	case "privacy":
		page, e := h.Page(ctx)
		if e != nil || len(page.Agents) != 2 || page.Agent.ID != "synthetic-dashboard" || len(page.KPIs) != 6 || !strings.HasPrefix(page.KPIs[5].Value, "$0.25 / ") {
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
		}{{"GET", "/", 200}, {"HEAD", "/", 200}, {"GET", "/dashboard.css", 200}, {"HEAD", "/dashboard.css", 200}, {"POST", "/", 405}, {"GET", "/" + url.PathEscape(private), 404}, {"GET", "/?q=" + url.QueryEscape(private), 200}, {"GET", "/?range=8", 400}}
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
		updateRejected, schemaRejected := ro.ReadOnlyProbe(ctx)
		if !updateRejected {
			return fail("dashboard_database_writable")
		}
		if !schemaRejected {
			return fail("dashboard_schema_change_allowed")
		}
		for range 3 {
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
			if response.Code != 200 {
				return fail("dashboard_read_failed")
			}
		}
		for _, id := range []string{"synthetic-dashboard", "synthetic-second", "unselected-agent"} {
			if status, err := st.AgentStatus(ctx, id); err != nil || status != "running" {
				return fail("dashboard_state_mutation")
			}
		}
	case "query_failure":
		if _, err = raw.ExecContext(ctx, `DROP TABLE wakes`); err != nil {
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

// openRaw returns a handle that bypasses the store. The dashboard cases plant
// sentinel values in columns the store never writes that way and break the
// schema underneath the reader.
func openRaw(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
}
