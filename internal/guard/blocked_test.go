package guard_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
)

func modelResult(t *testing.T, err error) (blocked, hint string) {
	t.Helper()
	var b *guard.BlockedError
	if !errors.As(err, &b) {
		t.Fatalf("not a BlockedError: %v", err)
	}
	var out struct{ Blocked, Hint string }
	if json.Unmarshal([]byte(b.ModelResult()), &out) != nil {
		t.Fatalf("result is not JSON: %s", b.ModelResult())
	}
	return out.Blocked, out.Hint
}

func TestBlockedResultNamesTheFixablePart(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) {
		tmpl.Limits.MinSecondsBetweenActions = 300
	})
	_, err := h.call("post", map[string]any{"text": "well TUY NHIÊN, no"})
	if b, hint := modelResult(t, err); b != ruleForbidden || hint != `remove the phrase "Tuy nhiên," and rewrite` {
		t.Fatalf("forbidden: %q %q", b, hint)
	}
	_, err = h.call("post", map[string]any{"text": strings.Repeat("あ", 501)})
	if b, hint := modelResult(t, err); b != ruleTextLength || hint != "too long: 501 of 500 characters; cut it to one short idea" {
		t.Fatalf("length: %q %q", b, hint)
	}
	h.allow("post", postArgs(1))
	h.clock.Advance(100 * time.Second)
	_, err = h.call("post", postArgs(2))
	if b, hint := modelResult(t, err); b != ruleSpacing || hint != "next action allowed at 2026-11-11T10:05:00Z" {
		t.Fatalf("spacing: %q %q", b, hint)
	}
}

func TestEveryReasonCodeHasAFixedHint(t *testing.T) {
	for _, reason := range []string{"unknown_tool", "unsupported_tool_type", "invalid_tool_call_id", "invalid_tool_arguments", "sensitive_arguments", "agent_not_running", "wake_not_open", "phase_read_only", "text_length", "blocked_phrase", "identity_targeting", "political_recommendation", "engagement_bait", "explore_budget", "own_account", "reply_target_host", "reply_target_mismatch", "reply_target_unread", "reply_url", "reply_mention", "invalid_policy", "invalid_hypothesis", "shared_promotion_link", "do_not_contact", "replies_per_user_7d", "thread_per_day", "target_outside_window", "min_seconds_between_actions", "limit_per_wake", "topic_args"} {
		b, hint := modelResult(t, &guard.BlockedError{Reason: reason, Detail: "x"})
		if b != reason || hint == "" || strings.Contains(hint, "%!") {
			t.Errorf("%s: %q", reason, hint)
		}
	}
}
