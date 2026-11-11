package guard_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
)

const (
	isolationSelf = "synthetic_one"
	isolationPeer = "synthetic_two"
)

// isolationGuardFixture creates two running agents sharing one store. The guard
// surface is public: identities are operator metadata, effects are closures,
// and no test depends on unexported policy internals.
func isolationGuardFixture(t *testing.T) (*store.Store, config.Template, time.Time) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "isolation-guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, err = st.DB().Exec(`
		INSERT INTO agents(id,status) VALUES ('agent-one','running'),('agent-two','running');
		INSERT INTO wakes(id,agent_id,started_at) VALUES
			(1,'agent-one','2026-11-11T00:00:00Z'),
			(2,'agent-two','2026-11-11T00:00:00Z');`)
	if err != nil {
		t.Fatalf("seeding isolation guard agents: %v", err)
	}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	tmpl := config.Template{
		ID:     "agent-one",
		DryRun: true,
		Threads: config.ThreadsConfig{
			Username: isolationSelf,
		},
		Limits: config.LimitsConfig{
			PostsPerDay:              5,
			RepliesPerDay:            10,
			RepliesPerUser7d:         2,
			MinSecondsBetweenActions: 1,
		},
	}
	return st, tmpl, now
}

func TestIsolationAcceptancePeerAccountReplyAndMentionRefused(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		args  string
		allow bool
	}{
		{
			name: "target username is the peer after at-sign and case normalization",
			tool: "reply",
			args: `{"text":"synthetic reply","target_username":"@SYNTHETIC_TWO","target_url":"https://threads.net/@ordinary_author/post/1"}`,
		},
		{
			name: "peer URL alias with host case www scheme and query",
			tool: "reply",
			args: `{"text":"synthetic reply","target_username":"ordinary_author","target_url":"https://WWW.THREADS.COM/@Synthetic_Two/post/1?x=1#frag"}`,
		},
		{
			name: "peer profile path without at-sign",
			tool: "reply",
			args: `{"text":"synthetic reply","target_username":"ordinary_author","target_url":"https://threads.net/synthetic_two/post/1"}`,
		},
		{
			name: "outbound peer mention from a post",
			tool: "post",
			args: `{"text":"synthetic take for @Synthetic_Two"}`,
		},
		{
			name: "direct peer profile link from a post",
			tool: "post",
			args: `{"text":"synthetic take https://www.threads.net/@synthetic_two/"}`,
		},
		{
			name: "bare peer link in a reply mention",
			tool: "reply",
			args: `{"text":"synthetic reply to @ordinary_author about threads.net/@synthetic_two/post/1","target_username":"ordinary_author","target_url":"https://threads.net/@ordinary_author/post/1"}`,
		},
		{
			name: "peer mention inside a reply whose target username lies",
			tool: "reply",
			args: `{"text":"synthetic reply @synthetic_two","target_username":"ordinary_author","target_url":"https://threads.net/@ordinary_author/post/1"}`,
		},
		{
			name:  "ordinary stranger reply remains allowed",
			tool:  "reply",
			args:  `{"text":"synthetic reply","target_username":"ordinary_author","target_url":"https://threads.net/@ordinary_author/post/1"}`,
			allow: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, tmpl, now := isolationGuardFixture(t)
			g, err := guard.New(guard.Options{
				Store:        st,
				Template:     tmpl,
				OwnUsernames: []string{isolationSelf, isolationPeer},
				WakeID:       1,
				Now:          func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: tc.tool, Arguments: tc.args}, func() (string, error) {
				called = true
				return "synthetic result", nil
			})
			if tc.allow {
				if err != nil || !called {
					t.Fatalf("ordinary interaction refused: called=%v err=%v", called, err)
				}
				return
			}
			if !errors.Is(err, guard.ErrBlocked) || called || !strings.Contains(err.Error(), "own_account") {
				t.Fatalf("peer account escaped: called=%v err=%v", called, err)
			}
			var reason string
			if err := st.DB().QueryRow(`SELECT blocked_by FROM tool_calls`).Scan(&reason); err != nil || reason != "own_account" {
				t.Fatalf("audit reason=%q err=%v", reason, err)
			}
		})
	}
}

func TestIsolationAcceptanceDirectGuardKnowsItsOwnTemplateIdentity(t *testing.T) {
	st, tmpl, now := isolationGuardFixture(t)
	// The harness-owned list is empty, but the running template still knows its
	// own configured account. A direct caller must not have to duplicate it.
	g, err := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: 1, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = g.Execute(context.Background(), guard.Attempt{
		Tool:      "reply",
		Arguments: `{"text":"synthetic reply","target_username":"@Synthetic_One","target_url":"https://threads.net/@synthetic_one/post/1"}`,
	}, func() (string, error) { called = true; return "", nil })
	if !errors.Is(err, guard.ErrBlocked) || called || !strings.Contains(err.Error(), "own_account") {
		t.Fatalf("direct guard missed template identity: called=%v err=%v", called, err)
	}
}

func TestIsolationAcceptanceCrossAgentDuplicatePromotionRefused(t *testing.T) {
	st, tmpl, now := isolationGuardFixture(t)
	first, err := guard.New(guard.Options{
		Store:        st,
		Template:     tmpl,
		OwnUsernames: []string{isolationSelf, isolationPeer},
		WakeID:       1,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	// An uncertain effect still commits and counts the dry-run reservation.
	_, err = first.Execute(context.Background(), guard.Attempt{
		Tool:      "post",
		Arguments: `{"text":"synthetic promotion https://Example.org/a/../article/?b=2&a=1#frag"}`,
	}, func() (string, error) { return "", errors.New("synthetic uncertain effect") })
	if err == nil {
		t.Fatal("uncertain effect was not reported")
	}

	secondTemplate := tmpl
	secondTemplate.ID = "agent-two"
	secondTemplate.Threads.Username = isolationPeer
	second, err := guard.New(guard.Options{
		Store:        st,
		Template:     secondTemplate,
		OwnUsernames: []string{isolationSelf, isolationPeer},
		WakeID:       2,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = second.Execute(context.Background(), guard.Attempt{
		Tool:      "post",
		Arguments: `{"text":"another synthetic promotion http://www.example.org:80/article?a=1&b=2"}`,
	}, func() (string, error) { called = true; return "synthetic result", nil })
	if !errors.Is(err, guard.ErrBlocked) || called || !strings.Contains(err.Error(), "shared_promotion_link") {
		t.Fatalf("cross-agent duplicate escaped: called=%v err=%v", called, err)
	}

	// A genuinely different query is a different local link and remains allowed.
	called = false
	if _, err = second.Execute(context.Background(), guard.Attempt{
		Tool:      "post",
		Arguments: `{"text":"independent synthetic promotion https://example.org/article?a=2&b=2"}`,
	}, func() (string, error) { called = true; return "synthetic result", nil }); err != nil || !called {
		t.Fatalf("independent promotion refused: called=%v err=%v", called, err)
	}
}

func TestIsolationAcceptanceRefusalLogsAndAuditOmitIdentityAndURL(t *testing.T) {
	st, tmpl, now := isolationGuardFixture(t)
	var logs bytes.Buffer
	logger, err := logging.New(&logs, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	g, err := guard.New(guard.Options{
		Store:        st,
		Template:     tmpl,
		OwnUsernames: []string{isolationSelf, isolationPeer},
		WakeID:       1,
		Now:          func() time.Time { return now },
		Logger:       logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	const rawURL = "https://www.threads.net/@Synthetic_Two/post/1?private=marker"
	_, err = g.Execute(context.Background(), guard.Attempt{
		Tool:      "reply",
		Arguments: `{"text":"synthetic reply","target_username":"ordinary_author","target_url":"` + rawURL + `"}`,
	}, func() (string, error) { return "", nil })
	if !errors.Is(err, guard.ErrBlocked) {
		t.Fatalf("expected refusal, got %v", err)
	}
	for _, forbidden := range []string{isolationPeer, "Synthetic_Two", rawURL, "https://", "private=marker"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("refusal error leaked %q: %v", forbidden, err)
		}
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("operational log leaked %q: %s", forbidden, logs.String())
		}
	}
	if !strings.Contains(logs.String(), `"event":"tool.blocked"`) || !strings.Contains(logs.String(), `"reason":"own_account"`) {
		t.Fatalf("safe blocked event missing: %s", logs.String())
	}
	var args, reason string
	if err := st.DB().QueryRow(`SELECT args_json,blocked_by FROM tool_calls`).Scan(&args, &reason); err != nil {
		t.Fatal(err)
	}
	if reason != "own_account" || strings.Contains(args, rawURL) || strings.Contains(args, isolationPeer) {
		t.Fatalf("audit leaked payload: args=%q reason=%q", args, reason)
	}
	var actions int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions); err != nil || actions != 0 {
		t.Fatalf("refused reply reserved an action: %d %v", actions, err)
	}
}
