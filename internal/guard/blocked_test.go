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
	if b, hint := modelResult(t, err); b != ruleTextLength || hint != "too long: 501 of 500 characters; cut it to one short idea and send it again" {
		t.Fatalf("length: %q %q", b, hint)
	}
	h.allow("post", postArgs(1))
	h.clock.Advance(100 * time.Second)
	_, err = h.call("post", postArgs(2))
	if b, hint := modelResult(t, err); b != ruleSpacing || hint != "next action allowed at 2026-11-11T10:05:00Z; wait until then, then send it again" {
		t.Fatalf("spacing: %q %q", b, hint)
	}
}

// reasons is every reason code a call can be refused with that reaches the model.
var reasons = []string{"unknown_tool", "unsupported_tool_type", "invalid_tool_call_id", "invalid_tool_arguments", "sensitive_arguments", "agent_not_running", "wake_not_open", "phase_read_only", "text_length", "too_many_parts", "blocked_phrase", "identity_targeting", "political_recommendation", "engagement_bait", "own_account", "unknown_target", "reply_target_host", "reply_url", "reply_mention", "invalid_policy", "shared_promotion_link", "do_not_contact", "replies_per_user_7d", "replies_per_hour", "reply_breaker", "thread_per_day", "replies_per_conversation", "target_outside_window", "min_seconds_between_actions", "limit_per_wake", "tool_closed", "tool_hidden", "tool_call_cap", "repeat_refused", "find_args", "min_minutes_between_posts", "post_brief", "no_trend_anchor", "repeat_topic", "owner_note_open", "note_not_open", "browser_replies_per_day", "api_quota_exhausted", "api_quota_unreadable", "critic_rejected", "critic_limit", "critic_unavailable", "ungrounded_number", "lesson_citations", "lesson_foreign_citation", "lesson_unranked", "lesson_kind_mismatch", "lesson_band", "lesson_too_long", "lessons_full", "lesson_duplicate", "lesson_forgotten", "lesson_unknown", "lesson_no_new_evidence", "lesson_enum", "invalid_lesson_ids", "lesson_number_mismatch", "lesson_foreign_url", "lesson_secret", "lesson_has_ref", "lesson_old_episode", "budget", "stale_ref"}

// Every refusal the model reads states what happened and the next valid
// action in one sentence.
func TestEveryReasonCodeHasAFixedHint(t *testing.T) {
	for _, reason := range reasons {
		b, hint := modelResult(t, &guard.BlockedError{Reason: reason, Detail: "x"})
		if b != reason || hint == "" || strings.Contains(hint, "%!") || strings.Contains(hint, "%s") {
			t.Errorf("%s: %q", reason, hint)
		}
		if strings.Contains(hint, ". ") || len(hint) > 240 {
			t.Errorf("%s: hint is not one short sentence: %q", reason, hint)
		}
	}
}

func TestPostsAreSpacedApart(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) {
		tmpl.Limits.MinSecondsBetweenActions = 1
		tmpl.Limits.MinMinutesBetweenPosts = 120
	})
	h.allow("post", postArgs(1))
	h.clock.Advance(10 * time.Minute)
	_, err := h.call("post", postArgs(2))
	if b, hint := modelResult(t, err); b != "min_minutes_between_posts" || !strings.Contains(hint, "2026-11-11T12:00:00Z") {
		t.Fatalf("post spacing: %q %q", b, hint)
	}
	h.clock.Advance(111 * time.Minute)
	h.allow("post", postArgs(3))
}
