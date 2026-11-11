package guard_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/guard"
)

func TestPolicyReservesQuotaBeforeEffect(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Limits.PostsPerDay = 1
	opts.Template.Limits.RepliesPerDay = 3
	opts.Template.Limits.RepliesPerUser7d = 2
	opts.Template.Limits.MinSecondsBetweenActions = 1
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	effect := func() (string, error) {
		calls++
		counts, err := st.CountActions(context.Background(), "agent", opts.Now().Add(-time.Second), opts.Now().Add(time.Second))
		if err != nil || counts.Posts != 1 {
			t.Fatalf("intent missing before effect %+v %v", counts, err)
		}
		return "", errors.New("synthetic unknown effect")
	}
	if _, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"First synthetic action"}`}, effect); err == nil {
		t.Fatal("effect failure lost")
	}
	if _, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"Second synthetic action"}`}, effect); !errors.Is(err, guard.ErrBlocked) || calls != 1 {
		t.Fatalf("quota bypass %d %v", calls, err)
	}
}

func TestConcurrentGuardsCannotSpendLastSlotTwice(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Limits.PostsPerDay = 1
	opts.Template.Limits.RepliesPerDay = 20
	opts.Template.Limits.RepliesPerUser7d = 2
	opts.Template.Limits.MinSecondsBetweenActions = 1
	var effects atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := guard.New(opts)
			if err != nil {
				t.Error(err)
				return
			}
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"synthetic concurrent intent"}`}, func() (string, error) { effects.Add(1); return "ok", nil })
			if err != nil && !errors.Is(err, guard.ErrBlocked) && !errors.Is(err, guard.ErrAudit) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	counts, err := st.CountActions(context.Background(), "agent", opts.Now().Add(-time.Hour), opts.Now().Add(time.Hour))
	if err != nil || counts.Posts != 1 || effects.Load() != 1 {
		t.Fatalf("double spend %+v effects %d err %v", counts, effects.Load(), err)
	}
}

func TestStopRequestsDefaultAndPersistenceFailure(t *testing.T) {
	st, opts := setup(t)
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"STOP", "ĐỪNG LÀM PHIỀN"} {
		matched, err := g.ObserveStopRequest(context.Background(), "synthetic_user", text, true)
		if err != nil || !matched {
			t.Fatalf("stop %v %v", matched, err)
		}
	}
	st.DB().Exec(`CREATE TRIGGER fail_dnc BEFORE INSERT ON contacts BEGIN SELECT RAISE(FAIL,'private-contact-payload'); END`)
	matched, err := g.ObserveStopRequest(context.Background(), "other_user", "stop", true)
	if matched || !errors.Is(err, guard.ErrAudit) || strings.Contains(err.Error(), "private") {
		t.Fatalf("fail closed %v %v", matched, err)
	}
	opts.Template.Guard.StopPhrases = []string{" "}
	if _, err = guard.New(opts); err == nil {
		t.Fatal("empty phrase accepted")
	}
}

func TestReservationPersistenceFailuresNeverInvokeEffect(t *testing.T) {
	for _, mode := range []string{"contact-insert", "contact-refresh", "invalid-stamp", "invalid-contact", "invalid-thread", "decision-update"} {
		t.Run(mode, func(t *testing.T) {
			st, opts := setup(t)
			opts.Template.Limits.PostsPerDay = 1
			opts.Template.Limits.RepliesPerDay = 3
			opts.Template.Limits.RepliesPerUser7d = 2
			opts.Template.Limits.MinSecondsBetweenActions = 1
			args := `{"text":"synthetic outgoing reply","target_username":"fixture_author","target_url":"https://threads.test/@fixture_author/post/1"}`
			switch mode {
			case "contact-insert":
				st.DB().Exec(`CREATE TRIGGER fail_contact BEFORE INSERT ON contacts BEGIN SELECT RAISE(FAIL,'private-contact-error'); END`)
			case "contact-refresh":
				st.DB().Exec(`INSERT INTO contacts(agent_id,username) VALUES('agent','fixture_author'); CREATE TRIGGER fail_contact BEFORE UPDATE ON contacts BEGIN SELECT RAISE(FAIL,'private-contact-error'); END`)
			case "invalid-stamp":
				st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at) VALUES('agent','post','','invalid-private-timestamp')`)
			case "invalid-contact":
				st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,target_username,target_url) VALUES('agent','reply','','2026-11-10T23:59:00Z','invalid name','https://threads.test/x')`)
			case "invalid-thread":
				st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at,target_username,target_url) VALUES('agent','reply','','2026-11-10T23:59:00Z','fixture_author','invalid-url')`)
			case "decision-update":
				st.DB().Exec(`INSERT INTO contacts(agent_id,username,do_not_contact) VALUES('agent','fixture_author',1); CREATE TRIGGER fail_audit BEFORE UPDATE ON tool_calls BEGIN SELECT RAISE(FAIL,'private-audit-error'); END`)
			}
			g, err := guard.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			var before int
			st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&before)
			called := false
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: args}, func() (string, error) { called = true; return "", nil })
			if called || !errors.Is(err, guard.ErrAudit) || strings.Contains(err.Error(), "private") {
				t.Fatalf("effect %v error %v", called, err)
			}
			var after, audits int
			st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&after)
			st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE result_json=''`).Scan(&audits)
			if after != before || audits != 1 {
				t.Fatalf("rollback action count %d -> %d audits %d", before, after, audits)
			}
		})
	}
}

func TestGuardRejectsMalformedPolicyInputsBeforeReservation(t *testing.T) {
	for _, tc := range []struct {
		tool, args, reason string
		invalidLimits      bool
	}{
		{"post", `{`, "invalid_tool_arguments", false},
		{"post", `{"text":" "}`, "invalid_tool_arguments", false},
		{"reply", `{"text":"synthetic reply","target_username":"bad name","target_url":"https://threads.test/x"}`, "invalid_tool_arguments", false},
		{"reply", `{"text":"synthetic reply","target_username":"fixture_author","target_url":"javascript:bad"}`, "invalid_tool_arguments", false},
		{"post", `{"text":"synthetic post"}`, "invalid_policy", true},
	} {
		t.Run(tc.reason+tc.args, func(t *testing.T) {
			st, opts := setup(t)
			if !tc.invalidLimits {
				opts.Template.Limits.PostsPerDay = 3
				opts.Template.Limits.RepliesPerDay = 3
				opts.Template.Limits.RepliesPerUser7d = 2
				opts.Template.Limits.MinSecondsBetweenActions = 1
			}
			g, _ := guard.New(opts)
			called := false
			_, err := g.Execute(context.Background(), guard.Attempt{Tool: tc.tool, Arguments: tc.args}, func() (string, error) { called = true; return "", nil })
			if !errors.Is(err, guard.ErrBlocked) || called {
				t.Fatalf("invalid input ran %v %v", called, err)
			}
			var reason string
			if err := st.DB().QueryRow(`SELECT blocked_by FROM tool_calls`).Scan(&reason); err != nil || reason != tc.reason {
				t.Fatalf("reason %q %v", reason, err)
			}
		})
	}
}
