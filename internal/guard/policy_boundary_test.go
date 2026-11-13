package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

// These are the stable rule IDs the guard must record in tool_calls.blocked_by.
// A boundary test fails when either the effect happened or the wrong rule fired.
const (
	rulePostsPerDay    = "posts_per_day"
	ruleRepliesPerDay  = "replies_per_day"
	rulePerUser7d      = "replies_per_user_7d"
	ruleSpacing        = "min_seconds_between_actions"
	ruleForbidden      = "blocked_phrase"
	ruleTextLength     = "text_length"
	ruleReplyURL       = "reply_url"
	ruleReplyMention   = "reply_mention"
	ruleThreadPerDay   = "thread_per_day"
	ruleDNC            = "do_not_contact"
	ruleTargetHost     = "reply_target_host"
	ruleTargetMismatch = "reply_target_mismatch"
	ruleTargetUnread   = "reply_target_unread"
)

type policyClock struct{ at time.Time }

func (c *policyClock) Now() time.Time          { return c.at }
func (c *policyClock) Advance(d time.Duration) { c.at = c.at.Add(d) }

type policyHarness struct {
	t     *testing.T
	st    *store.Store
	clock *policyClock
	guard *guard.Guard
	tools *tools.Dispatcher
	calls int
	// unread skips the automatic read of a reply's target_url.
	unread bool
}

func policyTemplate() config.Template {
	return config.Template{
		ID:     "guard-agent",
		DryRun: true,
		Guard:  config.GuardConfig{BlockedPhrases: []string{"Tuy nhiên,"}},
		Limits: config.LimitsConfig{
			PostsPerDay:              100,
			RepliesPerDay:            100,
			RepliesPerUser7d:         100,
			MinSecondsBetweenActions: 1,
			MaxStepsPerWake:          20,
			MaxUSDPerDay:             1,
			MinMinutesBetweenWakes:   15,
		},
	}
}

// newPolicyHarness opens a real temporary SQLite database and drives the public
// guard and dispatcher boundaries. The clock is injected so every boundary is
// exact; no test sleeps or reaches the network.
func newPolicyHarness(t *testing.T, mutate func(*config.Template)) *policyHarness {
	t.Helper()
	tmpl := policyTemplate()
	if mutate != nil {
		mutate(&tmpl)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.DB().Exec(`INSERT INTO agents(id,status) VALUES('guard-agent','running'); INSERT INTO wakes(id,agent_id,started_at) VALUES(1,'guard-agent','2026-11-11T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	clock := &policyClock{at: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	embedder, err := llm.NewOfflineEmbedder(config.LimitsConfig{MaxUSDPerDay: 1}, st, llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(tools.Options{Embedder: embedder, Scope: guard.Scope{Store: st, Template: tmpl, WakeID: 1, Now: clock.Now}, FetchTrends: func(context.Context) ([]trends.Trend, error) {
		return []trends.Trend{{Source: "synthetic", Title: "synthetic-trend", URL: "https://example.test/trend"}}, nil
	}, FetchStudy: func(context.Context) (trends.StudyFeed, error) {
		return trends.StudyFeed{Items: []trends.Trend{{Source: "synthetic", Title: "synthetic-study", URL: "https://example.test/study"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &policyHarness{t: t, st: st, clock: clock, guard: d.Guard(), tools: d}
}

func (h *policyHarness) call(tool string, args any) (string, error) {
	h.t.Helper()
	h.calls++
	raw, err := json.Marshal(args)
	if err != nil {
		h.t.Fatal(err)
	}
	if m, ok := args.(map[string]any); ok && tool == "reply" && !h.unread {
		if u, ok := m["target_url"].(string); ok {
			h.guard.NoteRead("tid", u, "")
		}
	}
	// Every synthetic call carries a unique ID and varying arguments so quota
	// tests cannot trip the separate third-identical-call stall latch.
	return h.tools.Dispatch(context.Background(), llm.ToolCall{ID: fmt.Sprintf("policy-call-%d", h.calls), Type: "function", Function: llm.CallFunction{Name: tool, Arguments: string(raw)}})
}

func (h *policyHarness) allow(tool string, args any) {
	h.t.Helper()
	if _, err := h.call(tool, args); err != nil {
		h.t.Fatalf("allowing %s: %v", tool, err)
	}
}

func (h *policyHarness) blocked(tool string, args any, rule string) {
	h.t.Helper()
	before := h.actionCount()
	_, err := h.call(tool, args)
	if err == nil {
		h.t.Fatalf("%s was allowed, want %s refusal", tool, rule)
	}
	if !errors.Is(err, guard.ErrBlocked) {
		h.t.Fatalf("%s refusal is not guard.ErrBlocked: %v", tool, err)
	}
	var refusal *guard.BlockedError
	if rule != rulePostsPerDay && rule != ruleRepliesPerDay && (!errors.As(err, &refusal) || refusal.Reason != rule || guard.Hint(rule, "x") == "") {
		h.t.Fatalf("%s refusal %v carries no hint for %s", tool, err, rule)
	}
	var blockedBy, result string
	if err := h.st.DB().QueryRow(`SELECT blocked_by,result_json FROM tool_calls ORDER BY id DESC LIMIT 1`).Scan(&blockedBy, &result); err != nil {
		h.t.Fatalf("reading refusal audit for %s: %v", tool, err)
	}
	if blockedBy != rule {
		h.t.Fatalf("%s blocked_by=%q want %q", tool, blockedBy, rule)
	}
	if result != "" {
		h.t.Fatalf("%s refusal recorded an effect result: %q", tool, result)
	}
	if after := h.actionCount(); after != before {
		h.t.Fatalf("%s refusal changed actions from %d to %d", tool, before, after)
	}
}

func (h *policyHarness) actionCount() int {
	h.t.Helper()
	var n int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func postArgs(seq int) map[string]any {
	return map[string]any{"text": fmt.Sprintf("topic%d", seq)}
}

func replyArgs(seq int, username, thread string) map[string]any {
	return map[string]any{
		"text":            fmt.Sprintf("synthetic reply %d", seq),
		"target_username": username,
		"target_id":       "tid", "target_url": thread,
	}
}

func TestPolicyPostAndReplyDailyCapsAllowNAndRefuseNPlusOne(t *testing.T) {
	t.Run("posts", func(t *testing.T) {
		h := newPolicyHarness(t, func(tmpl *config.Template) { tmpl.Limits.PostsPerDay = 2 })
		h.allow("post", postArgs(1))
		h.clock.Advance(time.Second)
		h.allow("post", postArgs(2))
		h.clock.Advance(time.Second)
		h.blocked("post", postArgs(3), rulePostsPerDay)
		if got := h.actionCount(); got != 2 {
			t.Fatalf("posts recorded %d want 2", got)
		}
	})
	t.Run("replies", func(t *testing.T) {
		h := newPolicyHarness(t, func(tmpl *config.Template) { tmpl.Limits.RepliesPerDay = 2 })
		h.allow("reply", replyArgs(1, "alice", "https://threads.com/@alice/post/1"))
		h.clock.Advance(time.Second)
		h.allow("reply", replyArgs(2, "bob", "https://threads.com/@bob/post/2"))
		h.clock.Advance(time.Second)
		h.blocked("reply", replyArgs(3, "carol", "https://threads.com/@carol/post/3"), ruleRepliesPerDay)
		if got := h.actionCount(); got != 2 {
			t.Fatalf("replies recorded %d want 2", got)
		}
	})
}

func TestPolicyDailyWindowIsUTCCalendarDay(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) { tmpl.Limits.PostsPerDay = 1 })
	h.clock.at = time.Date(2026, 11, 11, 23, 59, 57, 0, time.UTC)
	h.allow("post", postArgs(1))
	h.clock.Advance(time.Second) // 23:59:58, still the same UTC day
	h.blocked("post", postArgs(2), rulePostsPerDay)
	h.clock.Advance(2 * time.Second) // 00:00:00 the next UTC day
	h.allow("post", postArgs(3))
	h.clock.Advance(time.Second)
	h.blocked("post", postArgs(4), rulePostsPerDay)
}

func TestPolicyPerUserSevenDayWindowExpiresExactly(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) {
		tmpl.Limits.RepliesPerUser7d = 1
		tmpl.Limits.RepliesPerDay = 100
	})
	start := h.clock.Now()
	h.allow("reply", replyArgs(1, "alice", "https://threads.com/@alice/post/1"))

	h.clock.at = start.Add(7*24*time.Hour - time.Second)
	h.blocked("reply", replyArgs(2, "alice", "https://threads.com/@alice/post/2"), rulePerUser7d)

	h.clock.at = start.Add(7 * 24 * time.Hour)
	h.allow("reply", replyArgs(3, "alice", "https://threads.com/@alice/post/3"))

	h.clock.Advance(time.Second)
	h.blocked("reply", replyArgs(4, "alice", "https://threads.com/@alice/post/4"), rulePerUser7d)
}

func TestPolicySpacingEdges(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) {
		tmpl.Limits.MinSecondsBetweenActions = 300
		tmpl.Limits.PostsPerDay = 20
	})
	h.allow("post", postArgs(1))
	h.clock.Advance(299 * time.Second)
	h.blocked("post", postArgs(2), ruleSpacing)
	h.clock.Advance(time.Second) // exactly the minimum gap
	h.allow("post", postArgs(3))
	h.clock.Advance(299 * time.Second)
	h.blocked("post", postArgs(4), ruleSpacing)
}

func TestPolicyForbiddenPhraseIsCaseInsensitive(t *testing.T) {
	t.Run("post", func(t *testing.T) {
		h := newPolicyHarness(t, nil)
		h.allow("post", map[string]any{"text": "synthetic post without the phrase"})
		h.clock.Advance(time.Second)
		h.blocked("post", map[string]any{"text": "TUY NHIÊN, synthetic post"}, ruleForbidden)
	})
	t.Run("reply", func(t *testing.T) {
		h := newPolicyHarness(t, nil)
		h.blocked("reply", map[string]any{
			"text":            "tuy nhiên, synthetic reply",
			"target_username": "alice",
			"target_id":       "tid", "target_url": "https://threads.com/@alice/post/1",
		}, ruleForbidden)
	})
}

func TestPolicyTextLengthCountsRunesAt500And501(t *testing.T) {
	text500 := strings.Repeat("ế", 500)
	text501 := strings.Repeat("ế", 501)
	if utf8.RuneCountInString(text500) != 500 || len(text500) <= 500 {
		t.Fatalf("fixture is not 500 multi-byte runes: runes=%d bytes=%d", utf8.RuneCountInString(text500), len(text500))
	}
	if utf8.RuneCountInString(text501) != 501 {
		t.Fatalf("fixture is not 501 runes: %d", utf8.RuneCountInString(text501))
	}

	h := newPolicyHarness(t, nil)
	h.allow("post", map[string]any{"text": text500})
	h.clock.Advance(time.Second)
	h.blocked("post", map[string]any{"text": text501}, ruleTextLength)

	h.clock.Advance(time.Second)
	h.allow("reply", map[string]any{
		"text":            text500,
		"target_username": "alice",
		"target_id":       "tid", "target_url": "https://threads.com/@alice/post/1",
	})
	h.clock.Advance(time.Second)
	h.blocked("reply", map[string]any{
		"text":            text501,
		"target_username": "bob",
		"target_id":       "tid", "target_url": "https://threads.com/@bob/post/2",
	}, ruleTextLength)
}

func TestPolicyReplyRefusesEveryURLForm(t *testing.T) {
	for _, body := range []string{
		"see https://example.test/path",
		"see http://example.test/path",
		"see www.example.test",
		"see example.test/path",
		"see example.test",
	} {
		t.Run(body, func(t *testing.T) {
			h := newPolicyHarness(t, nil)
			h.blocked("reply", map[string]any{
				"text":            body,
				"target_username": "alice",
				"target_id":       "tid", "target_url": "https://threads.com/@alice/post/1",
			}, ruleReplyURL)
		})
	}
}

func TestPolicyReplyMentionsOnlyTheAnsweredAuthor(t *testing.T) {
	h := newPolicyHarness(t, nil)
	h.allow("reply", map[string]any{
		"text":            "thanks @alice.dev",
		"target_username": "alice.dev",
		"target_id":       "tid", "target_url": "https://threads.com/@alice.dev/post/1",
	})
	h.clock.Advance(time.Second)
	h.blocked("reply", map[string]any{
		"text":            "thanks @bob",
		"target_username": "alice",
		"target_id":       "tid", "target_url": "https://threads.com/@alice/post/2",
	}, ruleReplyMention)
}

func TestPolicyOneReplyPerThreadPerUTCDay(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) {
		tmpl.Limits.RepliesPerDay = 100
		tmpl.Limits.RepliesPerUser7d = 100
	})
	h.clock.at = time.Date(2026, 11, 11, 23, 59, 57, 0, time.UTC)
	h.allow("reply", replyArgs(1, "alice", "https://threads.com/@alice/post/42"))
	h.clock.Advance(time.Second)
	// Query, fragment and a trailing slash identify the same thread.
	h.blocked("reply", map[string]any{
		"text":            "synthetic reply 2",
		"target_username": "alice",
		"target_id":       "tid", "target_url": "https://threads.com/@alice/post/42/?utm_source=synthetic#fragment",
	}, ruleThreadPerDay)
	h.clock.Advance(2 * time.Second) // 00:00:00 the next UTC day
	h.allow("reply", map[string]any{
		"text":            "synthetic reply 3",
		"target_username": "alice",
		"target_id":       "tid", "target_url": "https://threads.com/@alice/post/42",
	})
}

func TestPolicyStopRequestAddsPermanentContact(t *testing.T) {
	t.Run("configured Vietnamese and English", func(t *testing.T) {
		h := newPolicyHarness(t, func(tmpl *config.Template) {
			tmpl.Guard = config.GuardConfig{StopPhrases: []string{"dừng lại", "please stop"}}
		})
		matched, err := h.guard.ObserveStopRequest(context.Background(), " @Synthetic.Stop ", "DỪNG LẠI synthetic", true)
		if err != nil || !matched {
			t.Fatalf("Vietnamese stop request = %v, %v", matched, err)
		}
		matched, err = h.guard.ObserveStopRequest(context.Background(), "synthetic.stop", "Dừng lại again", true)
		if err != nil || !matched {
			t.Fatalf("existing permanent entry = %v, %v", matched, err)
		}
		h.blocked("reply", replyArgs(1, "synthetic.stop", "https://threads.com/@synthetic.stop/post/1"), ruleDNC)

		matched, err = h.guard.ObserveStopRequest(context.Background(), "english.user", "please STOP now", true)
		if err != nil || !matched {
			t.Fatalf("English stop request = %v, %v", matched, err)
		}
		h.clock.Advance(time.Second)
		h.blocked("reply", replyArgs(2, "english.user", "https://threads.com/@english.user/post/2"), ruleDNC)

		var reason string
		if err := h.st.DB().QueryRow(`SELECT reason FROM contacts WHERE agent_id='guard-agent' AND username='english.user' AND do_not_contact=1`).Scan(&reason); err != nil || reason != "stop_request" {
			t.Fatalf("stop contact reason %q, %v", reason, err)
		}
	})
	t.Run("unaddressed and nonmatching text do not add", func(t *testing.T) {
		h := newPolicyHarness(t, func(tmpl *config.Template) {
			tmpl.Guard = config.GuardConfig{StopPhrases: []string{"please stop"}}
		})
		if matched, err := h.guard.ObserveStopRequest(context.Background(), "unaddressed.user", "please stop", false); err != nil || matched {
			t.Fatalf("unaddressed stop = %v, %v", matched, err)
		}
		h.allow("reply", replyArgs(1, "unaddressed.user", "https://threads.com/@unaddressed.user/post/1"))

		h.clock.Advance(time.Second)
		if matched, err := h.guard.ObserveStopRequest(context.Background(), "ordinary.user", "synthetic unrelated text", true); err != nil || matched {
			t.Fatalf("nonmatching stop = %v, %v", matched, err)
		}
		h.allow("reply", replyArgs(2, "ordinary.user", "https://threads.com/@ordinary.user/post/2"))
	})
	t.Run("omitted configuration uses code defaults", func(t *testing.T) {
		h := newPolicyHarness(t, nil)
		if matched, err := h.guard.ObserveStopRequest(context.Background(), "default.user", "stop", true); err != nil || !matched {
			t.Fatalf("default stop phrase = %v, %v", matched, err)
		}
		h.blocked("reply", replyArgs(1, "default.user", "https://threads.com/@default.user/post/1"), ruleDNC)
	})
}

func TestPolicyReplyTargetMustMatchUsername(t *testing.T) {
	h := newPolicyHarness(t, nil)
	h.blocked("reply", replyArgs(1, "bob", "https://threads.com/@alice/post/1"), ruleTargetMismatch)
	h.blocked("reply", replyArgs(2, "@Alice", "https://threads.com/@bob/post/2"), ruleTargetMismatch)
	h.allow("reply", replyArgs(3, "@Alice", "https://threads.com/@ALICE/post/3"))
}

func TestPolicyDoNotContactCannotBeBypassedByMismatchedUsername(t *testing.T) {
	h := newPolicyHarness(t, nil)
	if err := h.st.MarkDoNotContact(context.Background(), "guard-agent", "alice", "owner_request"); err != nil {
		t.Fatal(err)
	}
	h.blocked("reply", replyArgs(1, "bob", "https://threads.com/@alice/post/1"), ruleTargetMismatch)
	h.blocked("reply", replyArgs(2, "alice", "https://threads.com/@alice/post/2"), ruleDNC)
	var n int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM contacts WHERE username='bob'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("mismatched reply charged bob: n=%d err=%v", n, err)
	}
}

func TestPolicyReplyTargetHostAndShape(t *testing.T) {
	h := newPolicyHarness(t, nil)
	h.blocked("reply", replyArgs(1, "alice", "https://example.com/@alice/post/1"), ruleTargetHost)
	h.blocked("reply", replyArgs(2, "alice", "https://threads.com.evil.test/@alice/post/2"), ruleTargetHost)
	h.blocked("reply", replyArgs(3, "alice", "https://threads.com/alice/post/3"), "invalid_tool_arguments")
	h.blocked("reply", replyArgs(4, "alice", "https://threads.com/@alice/status/4"), "invalid_tool_arguments")
	h.allow("reply", replyArgs(5, "alice", "https://www.threads.net/@alice/post/5"))
}

func TestPolicyReplyTargetMustHaveBeenRead(t *testing.T) {
	h := newPolicyHarness(t, nil)
	h.unread = true
	h.blocked("reply", replyArgs(1, "alice", "https://threads.com/@alice/post/1"), ruleTargetUnread)
	h.guard.NoteRead("tid", "http://www.threads.com/@alice/post/1/?utm=x#frag", "")
	h.allow("reply", replyArgs(2, "alice", "https://threads.com/@alice/post/1"))
	h.blocked("reply", replyArgs(3, "alice", "https://threads.com/@alice/post/9"), ruleTargetUnread)
	h.guard.NoteRead("tid", "not a url", "")
	h.blocked("reply", replyArgs(4, "alice", "https://threads.com/@alice/post/9"), ruleTargetUnread)
}
