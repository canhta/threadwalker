package guard

import (
	"encoding/json"
	"strings"
)

// BlockedError is the guard's refusal of one tool call. Detail is the one
// variable fact its hint names; it is never model or Threads text, except the
// critic's problems, which come from the separate editor model.
type BlockedError struct{ Reason, Detail string }

func (e *BlockedError) Error() string {
	return "executing guarded tool: " + e.Reason + ": " + ErrBlocked.Error()
}
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// ModelResult is the tool result the model reads after a refusal.
func (e *BlockedError) ModelResult() string {
	out, _ := json.Marshal(struct {
		Blocked string `json:"blocked"`
		Hint    string `json:"hint"`
	}{e.Reason, Hint(e.Reason, e.Detail)})
	return string(out)
}

// reasonTargetOutsideWindow refuses a reply to a post this wake's read showed
// outside the template's targets window.
const reasonTargetOutsideWindow = "target_outside_window"

// hints holds the fixed model-facing text per reason code; %s is Detail.
var hints = map[string]string{
	reasonTargetOutsideWindow:     "this post is outside the reply window (%s); pick a young, rising post with room in its replies",
	"repeat_refused":              "this exact call already failed 3 times the same way and was not run; use different inputs, try another approach, or say what blocks you",
	"unknown_tool":                "no such tool; call one from the tool list",
	"unsupported_tool_type":       "only function tool calls are supported",
	"invalid_tool_call_id":        "the tool call needs a non-empty id",
	"invalid_tool_arguments":      "arguments did not match the tool schema; fix them and retry",
	"sensitive_arguments":         "arguments contain a secret; remove it and retry",
	"agent_not_running":           "the agent is not running; no action is possible",
	"wake_not_open":               "this wake has ended; no action is possible",
	"phase_read_only":             "this tool is not allowed in the current phase; use a read or note tool",
	"text_length":                 "too long: %s characters; cut it to one short idea",
	"blocked_phrase":              "remove the phrase \"%s\" and rewrite",
	"identity_targeting":          "do not target a person's identity (appearance, gender, region, age, private life); rewrite about the idea instead",
	"political_recommendation":    "do not recommend a political choice; rewrite without it",
	"engagement_bait":             "do not ask for comments, tags, shares or follows; rewrite without the request",
	"explore_budget":              "the explore budget is used; post or reply without explore",
	"own_account":                 "do not interact with your own account; pick another target",
	"reply_target_host":           "the target must be a Threads post URL",
	"reply_target_mismatch":       "target_username must be the author of target_url",
	"reply_target_unread":         "read the target thread before replying to it; pass target_url as the short ref (e.g. \"r3\") of a post read_feed, read_topic or search returned this wake",
	"no_trend_anchor":             "a post needs trend_ref: the short ref (e.g. \"r3\", the ref field) of an item fetch_trends, read_feed, read_topic, search or web_search returned this wake; pick a story people are discussing today",
	"reply_url":                   "remove links from the reply and rewrite",
	"reply_mention":               "mention only the author you reply to, or remove the mention",
	"invalid_policy":              "the agent limits are misconfigured; no action is possible",
	"invalid_hypothesis":          "hypothesis_ids must be at most 5 distinct hypotheses you are testing",
	"shared_promotion_link":       "remove the link and rewrite",
	"do_not_contact":              "this user asked not to be contacted; pick another target",
	"replies_per_user_7d":         "you have replied to this user the maximum times this week; pick another target",
	"thread_per_day":              "you already replied in this thread today; pick another thread",
	"critic_rejected":             "an editor rejected this draft: %s; do not rewrite the same news: pick a different topic people actually live (from read_feed or fetch_trends) or a reply target, then try once more",
	"critic_limit":                "the editor rejected %s drafts this wake; do not write more this wake",
	"critic_unavailable":          "the editor could not review the draft; do not write more this wake",
	"ungrounded_number":           "%s is not a number on your facts sheet; quote a value from facts or drop the number",
	"min_seconds_between_actions": "next action allowed at %s",
	"min_minutes_between_posts":   "your last post is too recent; the next post is allowed at %s; reply to someone instead, or post in a later wake",
	"limit_per_wake":              "%s is used up for this wake; do not call it again, work with what you already read",
	"topic_args":                  "pass exactly one of url or query; url is only the short ref (such as \"r3\") of a Threads trend that read_feed returned this wake; for a news story or anything else, pass a few words as query",
}

// Hint returns the fixed text for reason with detail filled in.
func Hint(reason, detail string) string {
	return strings.ReplaceAll(hints[reason], "%s", detail)
}
