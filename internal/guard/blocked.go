package guard

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/canhta/threadz/internal/store"
)

// BlockedError is the guard's refusal of one tool call. Detail is the one
// variable fact its hint names; it is never model or Threads text, except the
// critic's problems, which come from the separate editor model.
type BlockedError struct {
	Reason, Detail string
	// Next is one more sentence for a refusal the model keeps provoking: the
	// next valid action, or that the tool is closed.
	Next string
}

func (e *BlockedError) Error() string {
	return "executing guarded tool: " + e.Reason + ": " + ErrBlocked.Error()
}
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// ModelResult is the tool result the model reads after a refusal.
func (e *BlockedError) ModelResult() string {
	hint := Hint(e.Reason, e.Detail)
	if e.Next != "" {
		hint += ". " + e.Next
	}
	out, _ := json.Marshal(struct {
		Blocked string `json:"blocked"`
		Hint    string `json:"hint"`
	}{e.Reason, hint})
	return string(out)
}

// reasonTargetOutsideWindow refuses a reply to a post this wake's read showed
// outside the template's targets window.
const reasonTargetOutsideWindow = "target_outside_window"

// hints holds the fixed model-facing text per reason code; %s is Detail.
var hints = map[string]string{
	reasonTargetOutsideWindow:     "this post is outside the reply window (%s); pick a young, rising post with room in its replies",
	"tool_closed":                 "this tool is closed for the rest of the wake; use another tool or end the wake",
	"tool_hidden":                 "this tool or source is not offered in this wake; use another one or end the wake",
	"tool_call_cap":               "this wake used all its tool calls; end the wake now",
	"repeat_refused":              "this exact call already failed 3 times and was not run; change the inputs, use another tool, or end the wake",
	"unknown_tool":                "no such tool; call one from the tool list",
	"unsupported_tool_type":       "only function tool calls are supported; call a tool from the tool list",
	"invalid_tool_call_id":        "the tool call needs a non-empty id; send it again with an id",
	"invalid_tool_arguments":      "argument %s did not match the tool schema; fix it and send the call again",
	"post_not_readable":           "%s was read in the browser, so its replies cannot be read; reply to it or read another post",
	"sensitive_arguments":         "arguments contain a secret; remove it and retry",
	"agent_not_running":           "the agent is not running; end the wake",
	"wake_not_open":               "this wake has ended; stop calling tools",
	"phase_read_only":             "this tool is not allowed in the current phase; use a read tool",
	"text_length":                 "too long: %s; cut it to one short idea and send it again",
	"blocked_phrase":              "remove the phrase \"%s\" and rewrite",
	"identity_targeting":          "do not target a person's identity (appearance, gender, region, age, private life); rewrite about the idea instead",
	"political_recommendation":    "do not recommend a political choice; rewrite without it",
	"engagement_bait":             "do not ask for comments, tags, shares or follows; rewrite without the request",
	"own_account":                 "do not interact with your own account; pick another target",
	"reply_target_host":           "that post has no usable Threads permalink; pick another post from find_posts",
	"no_trend_anchor":             "trend must be the ref (t3 or p12) of a trend or post you read; call fetch_trends or find_posts and use a ref it returned",
	"reply_url":                   "remove links from the reply and rewrite",
	"reply_mention":               "mention only the author you reply to, or remove the mention",
	"invalid_policy":              "the agent limits are misconfigured; end the wake",
	"shared_promotion_link":       "remove the link and rewrite",
	"do_not_contact":              "this user asked not to be contacted; pick another target",
	"replies_per_hour":            "you already replied the maximum times in the last hour; the next reply is allowed at %s; post something instead, or wait",
	"reply_breaker":               "replies are paused until %s because your recent replies earned nothing; post something better, or wait",
	"replies_per_user_7d":         "you have replied to this user the maximum times this week; pick another target",
	"replies_per_conversation":    "you already replied %s times in this conversation; pick a post from another conversation",
	"thread_per_day":              "you already replied in this thread today; pick another thread",
	"critic_rejected":             "an editor rejected this draft: %s; do not rewrite the same news: pick a different topic or reply target, then try once more",
	"critic_limit":                "the editor rejected %s drafts this wake; do not write more this wake",
	"critic_unavailable":          "the editor could not review the draft; do not write more this wake",
	"repeat_ending":               "your recent texts already end with %s too often; end this one differently, with no emoticon or emoji tail",
	"ungrounded_number":           "%s is not a number on your facts sheet; quote a value from facts or drop the number, then send it again",
	"min_seconds_between_actions": "next action allowed at %s; wait until then, then send it again",
	"min_minutes_between_posts":   "your last post is too recent; the next post is allowed at %s; reply to someone instead, or post in a later wake",
	"invalid_lesson_ids":          "lesson_ids takes at most 3 different ids of active lessons that fit this kind of action; use ids from the lesson list or leave it out",
	"lesson_citations":            "cite 2 to 5 different action_id values of your own actions",
	"lesson_foreign_citation":     "action_id %s is not one of your published, measured actions; cite your own",
	"lesson_unranked":             "action_id %s is not ranked at 6h or 24h yet; cite actions that have a rank at one of those windows",
	"lesson_kind_mismatch":        "every cited action must be a %s, the kind of the lesson; set kind to any to mix posts and replies",
	"lesson_band":                 "action_id %s is not in the band the polarity needs: do cites only top actions, avoid cites only bottom ones",
	"lesson_too_long":             "the lesson is %s characters; cut it to 240 or fewer",
	"lessons_full":                "%s lessons are active; retire one with lesson_retire, then add this one",
	"lesson_duplicate":            "lesson %s already says this; renew it with lesson_confirm instead",
	"lesson_forgotten":            "the owner removed a lesson that says this; do not add it again",
	"lesson_unknown":              "there is no active lesson with this id; use an id from the lesson list",
	"lesson_no_new_evidence":      "every cited action is already evidence for this lesson; cite at least one new one",
	"lesson_enum":                 "kind is post, reply or any; polarity is do or avoid",
	"lesson_number_mismatch":      "a number in the lesson is not a counter measured on the cited action; quote the measured value or drop it",
	"lesson_foreign_url":          "a url in the lesson is not one of your action urls; remove it",
	"lesson_has_ref":              "the lesson names a date or an action id; say what to do in words, without dates or ids",
	"lesson_old_episode":          "action_id %s was posted before this lesson was last confirmed; confirm only with newer actions",
	"lesson_secret":               "the lesson looks like it contains a secret; remove it",
	"stale_ref":                   "the text names a p or t ref, which is an id and not content; name the post or story in words instead",
	"budget":                      "today's budget is spent; stop calling paid tools this wake",
	"limit_per_wake":              "%s is used up for this wake; do not call it again, work with what you already read",
	"too_many_parts":              "a series has at most " + strconv.Itoa(store.MaxSeriesParts-1) + " later parts; send fewer parts",
	"unknown_target":              "target must be a p ref (such as p12) that this wake showed you; pick one from the open reply candidates or the inbox, or read posts with find_posts first and use a ref it returns",
	"note_not_open":               "answers_note names an owner note that is not open; use the id of an open note from the data message or leave it out",
	"post_brief":                  "a post needs hook, trend and a one-line rationale; add them and send it again",
	"repeat_topic":                "you already posted on this story in the last 24 hours; anchor a different story or reply to someone instead",
	"owner_note_open":             "an owner note is still open; send this post with answers_note set to its id, or reply to the note first",
	"browser_replies_per_day":     "the daily limit of replies through the browser is used and stays used for the rest of the wake; reply only to an inbox item or an open reply candidate (those have an api id), or post instead",
	"api_quota_exhausted":         "Threads allows no more publishing right now; end the wake",
	"api_quota_unreadable":        "the Threads publishing quota could not be read; end the wake and try next wake",
	"find_args":                   "source feed takes no other argument, topic takes topic (a t ref from feed) or query, search takes query, profile takes username; send exactly those",
}

// Hint returns the fixed text for reason with detail filled in.
func Hint(reason, detail string) string {
	return strings.ReplaceAll(hints[reason], "%s", detail)
}

// nextActions is the sentence a second refusal for the same reason adds.
var nextActions = map[string]string{
	"critic_rejected": "Do not redraft this idea: choose another topic or a reply target, or end the wake",
	"no_trend_anchor": "Call fetch_trends or find_posts once and use a ref it returns",
	"text_length":     "Send a text under the limit in one short sentence",
}

// NextAction names what to do instead after a repeated refusal.
func NextAction(reason string) string {
	if s, ok := nextActions[reason]; ok {
		return s
	}
	return "Do not repeat this; take a different action or end the wake"
}
