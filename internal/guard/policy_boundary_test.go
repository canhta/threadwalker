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
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

// These are the stable rule IDs the guard must record in tool_calls.blocked_by.
// A boundary test fails when either the effect happened or the wrong rule fired.
const (
	rulePostsPerDay   = "posts_per_day"
	ruleRepliesPerDay = "replies_per_day"
	rulePerUser7d     = "replies_per_user_7d"
	ruleSpacing       = "min_seconds_between_actions"
	ruleForbidden     = "blocked_phrase"
	ruleTextLength    = "text_length"
	ruleReplyURL      = "reply_url"
	ruleReplyMention  = "reply_mention"
	ruleThreadPerDay  = "thread_per_day"
	ruleDNC           = "do_not_contact"
	ruleTargetHost    = "reply_target_host"
	ruleUnknownTarget = "unknown_target"
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
	opts  tools.Options
	calls int
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
	h := &policyHarness{t: t, st: st, clock: clock, opts: tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: 1, Now: clock.Now}, FetchTrends: func(context.Context) ([]trends.Trend, error) {
		return []trends.Trend{{Source: "synthetic", Title: "synthetic-trend", URL: "https://example.test/trend"}}, nil
	}}}
	h.nextWake()
	return h
}

// nextWake starts a new wake at the clock's time: a new dispatcher, so what the last one closed is open again.
func (h *policyHarness) nextWake() {
	h.t.Helper()
	d, err := tools.NewDispatcher(h.opts)
	if err != nil {
		h.t.Fatal(err)
	}
	h.guard, h.tools = d.Guard(), d
}

func (h *policyHarness) call(tool string, args any) (string, error) {
	h.t.Helper()
	h.calls++
	// A reply names its target by ref: "read" is the harness's key for the post
	// the agent read first, replaced by the ref that read issued.
	if m, ok := args.(map[string]any); ok && tool == "reply" {
		if u, ok := m["read"].(string); ok {
			m["target"] = h.guard.NotePost(context.Background(), threads.Post{ID: "tid", URL: u})
			delete(m, "read")
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		h.t.Fatal(err)
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

// replyArgs is a reply to the post at thread; the harness reads that post first (see call).
func replyArgs(seq int, thread string) map[string]any {
	return map[string]any{"text": fmt.Sprintf("synthetic reply %d", seq), "read": thread}
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
		h.allow("reply", replyArgs(1, "https://threads.com/@alice/post/1"))
		h.clock.Advance(time.Second)
		h.allow("reply", replyArgs(2, "https://threads.com/@bob/post/2"))
		h.clock.Advance(time.Second)
		h.blocked("reply", replyArgs(3, "https://threads.com/@carol/post/3"), ruleRepliesPerDay)
		if got := h.actionCount(); got != 2 {
			t.Fatalf("replies recorded %d want 2", got)
		}
	})
}

func TestPolicyDailyWindowWithoutAZoneIsUTCCalendarDay(t *testing.T) {
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

// Every cap counts on the local day in schedule.timezone: 06:30 at UTC+7 is
// 23:30 UTC the day before, and the quota and the spend ledger agree on "today".
func TestPolicyDailyWindowIsTheLocalDay(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) {
		tmpl.Limits.PostsPerDay = 1
		tmpl.Limits.Timezone = "Asia/Ho_Chi_Minh"
	})
	h.clock.at = time.Date(2026, 11, 11, 23, 30, 0, 0, time.UTC) // 06:30 on 12 Nov local
	if day := (config.LimitsConfig{Timezone: "Asia/Ho_Chi_Minh"}).Day(h.clock.at); day != "2026-11-12" {
		t.Fatalf("spend day %s want 2026-11-12", day)
	}
	h.allow("post", postArgs(1))
	h.clock.Advance(17*time.Hour + 29*time.Minute + 57*time.Second) // 23:59:57 local, same day
	h.blocked("post", postArgs(2), rulePostsPerDay)
	h.clock.Advance(3 * time.Second) // 00:00:00 local the next day
	h.allow("post", postArgs(3))
}

func TestPolicyPerUserSevenDayWindowExpiresExactly(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) {
		tmpl.Limits.RepliesPerUser7d = 1
		tmpl.Limits.RepliesPerDay = 100
	})
	start := h.clock.Now()
	h.allow("reply", replyArgs(1, "https://threads.com/@alice/post/1"))

	h.clock.at = start.Add(7*24*time.Hour - time.Second)
	h.blocked("reply", replyArgs(2, "https://threads.com/@alice/post/2"), rulePerUser7d)

	h.clock.at = start.Add(7 * 24 * time.Hour)
	h.allow("reply", replyArgs(3, "https://threads.com/@alice/post/3"))

	h.clock.Advance(time.Second)
	h.blocked("reply", replyArgs(4, "https://threads.com/@alice/post/4"), rulePerUser7d)
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
			"text": "tuy nhiên, synthetic reply",
			"read": "https://threads.com/@alice/post/1",
		}, ruleForbidden)
	})
}

// A reply is capped by the platform at 500 characters and a post by the
// quality limit at 300 (and 3 lines); both count runes, not bytes.
func TestPolicyTextLengthCountsRunesAtTheLimits(t *testing.T) {
	text := func(n int) string { return strings.Repeat("ế", n) }
	if utf8.RuneCountInString(text(500)) != 500 || len(text(500)) <= 500 {
		t.Fatal("fixture is not 500 multi-byte runes")
	}
	h := newPolicyHarness(t, nil)
	h.allow("post", map[string]any{"text": text(quality.MaxPostRunes)})
	h.clock.Advance(time.Second)
	h.blocked("post", map[string]any{"text": text(quality.MaxPostRunes + 1)}, ruleTextLength)
	h.clock.Advance(time.Second)
	h.blocked("post", map[string]any{"text": "một\nhai\nba\nbốn"}, ruleTextLength)

	h.clock.Advance(time.Second)
	h.allow("reply", map[string]any{
		"text": text(500),
		"read": "https://threads.com/@alice/post/1",
	})
	h.clock.Advance(time.Second)
	h.blocked("reply", map[string]any{
		"text": text(501),
		"read": "https://threads.com/@bob/post/2",
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
				"text": body,
				"read": "https://threads.com/@alice/post/1",
			}, ruleReplyURL)
		})
	}
}

func TestPolicyReplyMentionsOnlyTheAnsweredAuthor(t *testing.T) {
	h := newPolicyHarness(t, nil)
	h.allow("reply", map[string]any{
		"text": "thanks @alice.dev",
		"read": "https://threads.com/@alice.dev/post/1",
	})
	h.clock.Advance(time.Second)
	h.blocked("reply", map[string]any{
		"text": "thanks @bob",
		"read": "https://threads.com/@alice/post/2",
	}, ruleReplyMention)
}

func TestPolicyOneReplyPerThreadPerDayWithoutAZoneIsUTC(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) {
		tmpl.Limits.RepliesPerDay = 100
		tmpl.Limits.RepliesPerUser7d = 100
	})
	h.clock.at = time.Date(2026, 11, 11, 23, 59, 57, 0, time.UTC)
	h.allow("reply", replyArgs(1, "https://threads.com/@alice/post/42"))
	h.clock.Advance(time.Second)
	// Query, fragment and a trailing slash identify the same thread.
	h.blocked("reply", map[string]any{
		"text": "synthetic reply 2",
		"read": "https://threads.com/@alice/post/42/?utm_source=synthetic#fragment",
	}, ruleThreadPerDay)
	h.clock.Advance(2 * time.Second) // 00:00:00 the next UTC day
	h.allow("reply", map[string]any{
		"text": "synthetic reply 3",
		"read": "https://threads.com/@alice/post/42",
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
		h.blocked("reply", replyArgs(1, "https://threads.com/@synthetic.stop/post/1"), ruleDNC)

		matched, err = h.guard.ObserveStopRequest(context.Background(), "english.user", "please STOP now", true)
		if err != nil || !matched {
			t.Fatalf("English stop request = %v, %v", matched, err)
		}
		h.clock.Advance(time.Second)
		h.blocked("reply", replyArgs(2, "https://threads.com/@english.user/post/2"), ruleDNC)

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
		h.allow("reply", replyArgs(1, "https://threads.com/@unaddressed.user/post/1"))

		h.clock.Advance(time.Second)
		if matched, err := h.guard.ObserveStopRequest(context.Background(), "ordinary.user", "synthetic unrelated text", true); err != nil || matched {
			t.Fatalf("nonmatching stop = %v, %v", matched, err)
		}
		h.allow("reply", replyArgs(2, "https://threads.com/@ordinary.user/post/2"))
	})
	t.Run("omitted configuration uses code defaults", func(t *testing.T) {
		h := newPolicyHarness(t, nil)
		if matched, err := h.guard.ObserveStopRequest(context.Background(), "default.user", "stop", true); err != nil || !matched {
			t.Fatalf("default stop phrase = %v, %v", matched, err)
		}
		h.blocked("reply", replyArgs(1, "https://threads.com/@default.user/post/1"), ruleDNC)
	})
}

// The author a reply answers is read from the stored post's permalink, never
// from an argument, so no name can disguise whose thread it is.
func TestPolicyReplyAuthorComesFromTheStoredPost(t *testing.T) {
	h := newPolicyHarness(t, nil)
	h.allow("reply", replyArgs(1, "https://threads.com/@ALICE/post/1"))
	var user string
	if err := h.st.DB().QueryRow(`SELECT target_username FROM actions WHERE kind='reply'`).Scan(&user); err != nil || user != "alice" {
		t.Fatalf("stored author %q: %v", user, err)
	}
}

func TestPolicyDoNotContactAppliesToTheStoredPostsAuthor(t *testing.T) {
	h := newPolicyHarness(t, nil)
	if err := h.st.MarkDoNotContact(context.Background(), "guard-agent", "alice", "owner_request"); err != nil {
		t.Fatal(err)
	}
	h.blocked("reply", replyArgs(1, "https://threads.com/@Alice/post/1"), ruleDNC)
	h.allow("reply", replyArgs(2, "https://threads.com/@bob/post/2"))
}

func TestPolicyReplyTargetHostAndShape(t *testing.T) {
	h := newPolicyHarness(t, nil)
	h.blocked("reply", replyArgs(1, "https://example.com/@alice/post/1"), ruleTargetHost)
	h.blocked("reply", replyArgs(2, "https://threads.com.evil.test/@alice/post/2"), ruleTargetHost)
	h.blocked("reply", replyArgs(3, "https://threads.com/alice/post/3"), ruleTargetHost)
	h.blocked("reply", replyArgs(4, "https://threads.com/@alice/status/4"), ruleTargetHost)
	h.allow("reply", replyArgs(5, "https://www.threads.net/@alice/post/5"))
}

func TestPolicyReplyTargetMustBeAStoredPost(t *testing.T) {
	h := newPolicyHarness(t, nil)
	h.blocked("reply", map[string]any{"text": "synthetic reply 1", "target": "p1"}, ruleUnknownTarget)
	h.allow("reply", replyArgs(2, "https://threads.com/@alice/post/1")) // the read stores it as p1
	h.blocked("reply", map[string]any{"text": "synthetic reply 3", "target": "p9"}, ruleUnknownTarget)
	h.blocked("reply", map[string]any{"text": "synthetic reply 4", "target": "https://threads.com/@alice/post/9"}, ruleUnknownTarget)
}
