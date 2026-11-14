package dashboard

import "strings"

// The page speaks to the owner, not to an engineer. Every internal enum is
// mapped here and nowhere else. Unknown values fall back to plain words; raw
// codes may only appear in a title attribute or inside <details>.

var toolLabels = map[string]string{
	"search":              "Search",
	"read_feed":           "Read feed",
	"read_topic":          "Read topic",
	"read_own_metrics":    "Own stats",
	"fetch_trends":        "Trends",
	"read_replies":        "Read replies",
	"read_post":           "Read post",
	"profile":             "Look up profile",
	"post":                "Post",
	"reply":               "Reply",
	"web_search":          "Web search",
	"notify_owner":        "Message owner",
	"ask_owner":           "Ask owner",
	"mark_do_not_contact": "Do not contact",
}

func toolLabel(raw string) string {
	if label, ok := toolLabels[raw]; ok {
		return label
	}
	return "Other"
}

var kindLabels = map[string]string{
	"agent":   "Run",
	"metrics": "Stats",
	"dream":   "Planning",
}

func kindLabel(raw string) string {
	if label, ok := kindLabels[raw]; ok {
		return label
	}
	return "Run"
}

var endReasons = []string{
	"completed", "interrupted", "step_limit", "budget", "provider_error",
	"invalid_response", "cancelled", "accounting_error", "persistence_error",
	"stall_stop", "tool_call_cap", "action_limit", "unknown_outcome", "logged_out", "captcha",
	"browser_error", "stopped", "too_few_episodes", "nothing_to_do", "in_progress", "",
}

var endReasonLabels = map[string]string{
	"completed":         "Done",
	"interrupted":       "Interrupted",
	"step_limit":        "Step limit",
	"budget":            "Spend limit",
	"provider_error":    "Model error",
	"browser_error":     "Browser error",
	"invalid_response":  "Bad response",
	"cancelled":         "Cancelled",
	"accounting_error":  "Cost not recorded",
	"persistence_error": "Not saved",
	"stall_stop":        "Stalled",
	"tool_call_cap":     "Tool call limit",
	"action_limit":      "Action limit",
	"unknown_outcome":   "Unknown outcome",
	"logged_out":        "Signed out",
	"captcha":           "Captcha",
	"stopped":           "Stopped",
	"too_few_episodes":  "Skipped: too few new results",
	"nothing_to_do":     "Skipped: nothing to do",
	"in_progress":       "Running",
	"":                  "Stopped",
}

func endReasonLabel(raw string) string {
	if label, ok := endReasonLabels[raw]; ok {
		return label
	}
	return "Stopped"
}

func endReasonBad(raw string) bool {
	return raw != "completed" && raw != "in_progress" && raw != "too_few_episodes" && raw != "nothing_to_do" && raw != ""
}

// blockedReasons is the fixed guard vocabulary. Anything not listed is shown
// as a generic stop reason, never as raw text from the database.
var blockedReasons = []string{
	"agent_not_running", "invalid_policy",
	"invalid_tool_arguments", "no_trend_anchor", "own_account", "phase_read_only",
	"blocked_phrase", "identity_targeting", "political_recommendation", "engagement_bait", "repeat_topic", "owner_note_open", "sensitive_arguments", "min_seconds_between_actions", "min_minutes_between_posts",
	"posts_per_day", "replies_per_day", "browser_replies_per_day", "replies_per_hour", "reply_breaker", "replies_per_user_7d", "replies_per_conversation", "reply_mention",
	"reply_target_host", "reply_target_mismatch", "reply_target_unread", "target_outside_window",
	"critic_rejected", "critic_limit", "critic_unavailable", "ungrounded_number",
	"reply_url", "shared_promotion_link", "text_length", "thread_per_day",
	"do_not_contact", "stall_stop", "tool_closed", "tool_hidden", "tool_call_cap", "last_request", "wake_not_open", "stop_request",
	"reservation_failed", "state_read_failed", "unknown_tool", "write_refused",
	"accounting_error", "budget", "captcha", "logged_out", "cancelled",
	"persistence_error", "unknown_outcome", "browser_error", "timeout",
	"malformed_page", "unknown_page", "error",
	"lesson_foreign_citation", "lesson_number_mismatch",
	"lesson_foreign_url", "lesson_secret", "lesson_has_ref", "lesson_old_episode", "stale_ref",
	"lesson_citations", "lesson_unranked", "lesson_kind_mismatch", "lesson_band", "lesson_too_long", "lessons_full", "lesson_duplicate", "lesson_forgotten", "lesson_unknown", "lesson_no_new_evidence", "lesson_enum", "invalid_lesson_ids",
}

var blockedLabels = map[string]string{
	"agent_not_running":           "Not running",
	"invalid_policy":              "Limit not set",
	"invalid_tool_arguments":      "Bad input",
	"own_account":                 "Own account",
	"phase_read_only":             "Write in read-only phase",
	"blocked_phrase":              "Blocked phrase",
	"identity_targeting":          "Targets identity",
	"political_recommendation":    "Political pick",
	"engagement_bait":             "Engagement bait",
	"repeat_topic":                "Topic repeated",
	"owner_note_open":             "Owner note unanswered",
	"sensitive_arguments":         "Private data",
	"min_seconds_between_actions": "Too soon",
	"min_minutes_between_posts":   "Post too soon",
	"posts_per_day":               "Daily post limit",
	"replies_per_day":             "Daily reply limit",
	"browser_replies_per_day":     "Daily feed reply limit",
	"replies_per_hour":            "Hourly reply limit",
	"reply_breaker":               "Replies paused",
	"replies_per_user_7d":         "Reply limit per person",
	"replies_per_conversation":    "Reply limit per conversation",
	"reply_mention":               "Wrong mention",
	"reply_target_host":           "Not a Threads link",
	"reply_target_mismatch":       "Author mismatch",
	"reply_target_unread":         "Post not read",
	"target_outside_window":       "Post not taking off",
	"reply_url":                   "Link in reply",
	"shared_promotion_link":       "Promotion link",
	"text_length":                 "Too long",
	"thread_per_day":              "Daily thread limit",
	"do_not_contact":              "Do not contact",
	"no_trend_anchor":             "Not tied to a trend",
	"critic_rejected":             "Editor said no",
	"critic_limit":                "Editor limit for the run",
	"critic_unavailable":          "Editor unavailable",
	"ungrounded_number":           "Number with no source",
	"stall_stop":                  "It stopped making progress",
	"tool_closed":                 "Tool closed after repeated refusals",
	"tool_hidden":                 "Tool not offered",
	"tool_call_cap":               "Tool call limit",
	"last_request":                "Write on the last request",
	"wake_not_open":               "Run ended",
	"stop_request":                "Owner stop",
	"reservation_failed":          "Not reserved",
	"state_read_failed":           "State unreadable",
	"unknown_tool":                "Unknown tool",
	"write_refused":               "Write refused",
	"accounting_error":            "A cost could not be recorded",
	"budget":                      "It reached its spending limit",
	"captcha":                     "A captcha blocked it",
	"logged_out":                  "The account was signed out",
	"cancelled":                   "It was stopped",
	"persistence_error":           "It could not save its work",
	"unknown_outcome":             "The result was unknown",
	"browser_error":               "The browser failed",
	"timeout":                     "Timeout",
	"malformed_page":              "Unreadable page",
	"unknown_page":                "Unknown page",
	"error":                       "Failed",
	"lesson_citations":            "Lesson cites the wrong number of posts",
	"lesson_unranked":             "Lesson cites a post with no rank yet",
	"lesson_kind_mismatch":        "Lesson cites the wrong kind of post",
	"lesson_band":                 "Lesson cites a post in the wrong band",
	"lesson_too_long":             "Lesson too long",
	"lessons_full":                "Too many lessons",
	"lesson_duplicate":            "Lesson already kept",
	"lesson_forgotten":            "Lesson was removed by the owner",
	"lesson_unknown":              "No such lesson",
	"lesson_no_new_evidence":      "Lesson has no new evidence",
	"lesson_enum":                 "Lesson kind or polarity not valid",
	"invalid_lesson_ids":          "Lesson ids not valid",
	"lesson_foreign_citation":     "Lesson cites another account",
	"lesson_number_mismatch":      "Lesson number not measured",
	"lesson_foreign_url":          "Lesson link not its own",
	"lesson_secret":               "Lesson looks like a secret",
	"lesson_has_ref":              "Lesson names a date or post id",
	"lesson_old_episode":          "Lesson confirmed with an old post",
	"stale_ref":                   "Text names a per-wake ref",
}

// failureReasons is the fixed vocabulary of a failed step's reason code.
var failureReasons = []string{
	"operation_failed", "unknown_outcome", "logged_out", "captcha", "unknown_page",
	"malformed_page", "metrics_unavailable", "media_not_found", "write_refused", "quota_exhausted",
	"api_error", "close_failed", "cancelled", "timeout",
	"lesson_foreign_citation", "lesson_number_mismatch",
	"lesson_foreign_url", "lesson_secret", "lesson_has_ref", "lesson_old_episode",
	"lesson_citations", "lesson_unranked", "lesson_kind_mismatch", "lesson_band", "lesson_too_long", "lessons_full", "lesson_duplicate", "lesson_forgotten", "lesson_unknown", "lesson_no_new_evidence", "lesson_enum",
}

var failureLabels = map[string]string{
	"operation_failed":    "Failed",
	"metrics_unavailable": "Stats unavailable",
	"media_not_found":     "Post not found",
	"quota_exhausted":     "Quota used",
	"api_error":           "API error",
	"close_failed":        "Close failed",
}

// failureLabel falls back to the blocked-reason label, then to Failed.
func failureLabel(raw string) string {
	if label, ok := failureLabels[raw]; ok {
		return label
	}
	if oneOfOK(raw, blockedReasons...) {
		return blockedLabel(raw)
	}
	return "Failed"
}

func blockedLabel(raw string) string {
	if label, ok := blockedLabels[raw]; ok {
		return label
	}
	return "Blocked"
}

var sourceTags = map[string]string{
	"hacker_news":        "HN",
	"product_hunt":       "PH",
	"google_trends":      "GT",
	"google_news_vn":     "GN",
	"google_news_global": "GN",
	"google_news_topics": "GN",
	"vnexpress":          "VNE",
	"genk":               "GK",
	"tinhte":             "TT",
	"vietnamnet":         "VNN",
	"the_verge":          "VG",
	"techcrunch":         "TC",
	"arstechnica":        "AT",
	"reddit_localllama":  "RD",
	"reddit_openai":      "RD",
	"reddit_singularity": "RD",
	"github_trending":    "GH",
	"youtube":            "YT",
	"threads":            "TH",
}

// sourceTag picks the first recognised source in a comma-separated list.
func sourceTag(raw string) string {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if tag, ok := sourceTags[part]; ok {
			return tag
		}
	}
	return "WEB"
}

func sourceLabel(raw string) string {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		return strings.ReplaceAll(part, "_", " ")
	}
	return "the web"
}

func statusText(status string) string {
	switch status {
	case "running":
		return "Running"
	case "paused":
		return "Paused"
	case "killed":
		return "Stopped"
	case "not_started":
		return "Not started"
	default:
		return "Unknown"
	}
}
