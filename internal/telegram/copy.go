package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/store"
)

// Owner-facing copy is English plain text: the client sends no parse_mode, so
// nothing needs escaping. Every value comes from code-owned facts; Threads text
// appears only in /restore previews and in the agent's own owner messages.

// ownerCommand is one entry of the command surface; the same table feeds
// setMyCommands and /help so the two cannot drift. Telegram setMyCommands
// limits names to 1-32 lowercase letters, digits and underscores and
// descriptions to 1-256 characters.
type ownerCommand struct {
	name, args, description string
	perAgent                bool
}

var ownerCommandList = []ownerCommand{
	{"status", "", "Current state, next run and today's counts", true},
	{"report", "", "Today's report", true},
	{"pause", "", "Pause the agent", true},
	{"resume", "", "Resume a paused agent", true},
	{"kill", "", "Stop the agent permanently", true},
	{"dnc", "<username>", "Never contact a Threads user", true},
	{"restore", "[m123 | s4]", "List or restore archived memory and strategy", true},
	{"cookie", "<cookies>", "Sign in to Threads again with browser cookies", true},
	{"update", "", "Install the latest release", false},
	{"help", "", "List commands", false},
	{"start", "", "Show what this bot does", false},
}

// ownerCommands is the menu the bot advertises. With several agents the
// per-agent commands say so.
func ownerCommands(multiAgent bool) []BotCommand {
	out := make([]BotCommand, len(ownerCommandList))
	for i, c := range ownerCommandList {
		d := c.description
		if multiAgent && c.perAgent {
			d += " (agent ID first)"
		}
		out[i] = BotCommand{Command: c.name, Description: d}
	}
	return out
}

func helpText(multiAgent bool) string {
	var b strings.Builder
	b.WriteString("Commands")
	for _, c := range ownerCommandList {
		if c.name == "start" {
			continue
		}
		b.WriteString("\n/" + c.name)
		if multiAgent && c.perAgent {
			b.WriteString(" <agent_id>")
		}
		if c.args != "" {
			b.WriteString(" " + c.args)
		}
		b.WriteString(" - " + c.description)
	}
	b.WriteString("\n\n/kill cannot be undone. Reply to a question from the agent to answer it; any other text is saved as a note for its next run.")
	return b.String()
}

func startText(name string, reportHour int, loc *time.Location, now time.Time, multiAgent bool) string {
	who := "the agent"
	if name != "" && !multiAgent {
		who = name
	}
	return fmt.Sprintf("Threadz owner console\nThis chat receives alerts and questions from %s, plus a daily report at %02d:00 (%s).\n\n%s", who, reportHour, zoneLabel(now.In(loc)), helpText(multiAgent))
}

func receiptText(command, username string) string {
	switch command {
	case "pause":
		return "Agent paused\nSend /resume to continue."
	case "resume":
		return "Agent resumed"
	case "kill":
		return "Agent stopped permanently\n/resume cannot restart it."
	case "dnc":
		return "Do-not-contact list updated\n@" + username + " will not be contacted."
	}
	return ""
}

const (
	noteAckText       = "Note saved\nThe agent reads it on its next run."
	credentialRefused = "Note not saved\nIt looked like a credential."
	questionHint      = "Reply to this message to answer."
)

// messageTitle is the first line Flush gives a stored message of this kind.
// Command replies and reports carry their own title.
func messageTitle(kind string) string {
	switch kind {
	case "notice":
		return "Notice"
	case "question":
		return "Question from the agent"
	case "notification":
		return "Message from the agent"
	}
	return ""
}

// CookieHowTo tells the owner how to sign the agent in again.
func CookieHowTo() string {
	return "To sign in again, send /cookie followed by the cookies of a browser signed in to threads.com: export them as JSON with Cookie-Editor and paste the whole array, or copy the Cookie header of a threads.com request from the devtools Network tab. The message is deleted immediately. From the server: threadz ctl threads cookies --agent ID < file"
}

// CookieResultText is the one-line answer to a cookie import.
func CookieResultText(status string) string {
	switch status {
	case "ok":
		return "Signed in. The agent has resumed."
	case "missing_sessionid":
		return "Cookie import failed: missing sessionid."
	case "bad_cookies":
		return "Cookie import failed: send a JSON export or a Cookie header from threads.com or instagram.com that includes sessionid and ds_user_id."
	case "logged_out":
		return "Cookies imported, but Threads still shows the session as logged out. They are expired or not from a signed-in browser."
	case "captcha":
		return "Cookie import stopped: Threads showed a captcha."
	case "busy":
		return "The agent is using the browser. Try again in a few minutes."
	}
	return "Cookie import failed. Try again later."
}

func stateText(status string) string {
	switch status {
	case "running":
		return "Running"
	case "paused":
		return "Paused"
	case "killed":
		return "Stopped permanently"
	case "":
		return "Unknown"
	}
	return status
}

// relativeTime is a short owner-facing phrase, not a precise duration.
func relativeTime(d time.Duration) string {
	if d <= 0 {
		return "due now"
	}
	minutes := int(d / time.Minute)
	switch {
	case minutes < 1:
		return "in under a minute"
	case minutes < 60:
		return fmt.Sprintf("in %d min", minutes)
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh %02dmin", minutes/60, minutes%60)
	}
	return fmt.Sprintf("in %d days", int(d/(24*time.Hour)))
}

func money(amount float64) string { return fmt.Sprintf("$%.2f", amount) }

// zoneLabel names the zone by its UTC offset ("UTC+07", "UTC+05:30").
func zoneLabel(t time.Time) string {
	_, offset := t.Zone()
	if offset == 0 {
		return "UTC"
	}
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	label := fmt.Sprintf("UTC%s%02d", sign, offset/3600)
	if m := offset % 3600 / 60; m != 0 {
		label += fmt.Sprintf(":%02d", m)
	}
	return label
}

// noticeText is the body of a code-owned notice kind. Only these kinds exist.
// reset is when every daily cap resets (the next local midnight), in the
// owner's zone.
func noticeText(kind, reset string) string {
	switch kind {
	case "daily_limit":
		return "Daily action limit reached. Posting and replying resume when the limits reset at " + reset + "."
	case "budget_80":
		return "Today's spend has reached 80% of the daily cap."
	case "budget_100":
		return "Daily spend cap reached. The agent stops until the spend cap resets at " + reset + "."
	case "logged_out":
		return "The Threads browser session is logged out. The agent is paused."
	case "logged_in":
		return "The Threads browser session is signed in again. The agent has resumed."
	case "captcha":
		return "Threads showed a captcha. The agent is paused."
	case "killed":
		return "The agent is stopped permanently."
	}
	return ""
}

// resetText shows an instant in the owner's zone.
func (s *Service) resetText(id string, at time.Time) string {
	local := at.In(s.location(id))
	return local.Format("15:04") + " " + zoneLabel(local)
}

// dayResetText is when the day ends (00:00 in schedule.timezone).
func (s *Service) dayResetText(id string) string {
	_, end, _ := s.templates[id].Limits.DayBounds(s.opts.Now())
	return s.resetText(id, end)
}

func (s *Service) location(id string) *time.Location {
	if loc := s.locations[id]; loc != nil {
		return loc
	}
	return time.UTC
}

func countOf(n, limit int) string {
	if limit > 0 {
		return fmt.Sprintf("%d of %d", n, limit)
	}
	return fmt.Sprintf("%d", n)
}

func (s *Service) spendLine(id string, summary store.OwnerSummary) string {
	line := money(summary.CostUSD)
	if limit := s.templates[id].Limits.MaxUSDPerDay; limit > 0 {
		line += fmt.Sprintf(" of %s (%.0f%%)", money(limit), 100*summary.CostUSD/limit)
	}
	if summary.StudyCostUSD > 0 || summary.DreamCostUSD > 0 {
		line += fmt.Sprintf("; wake %s, study %s, dream %s", money(summary.WakeCostUSD), money(summary.StudyCostUSD), money(summary.DreamCostUSD))
	}
	return line
}

func (s *Service) nextRunLine(id string, summary store.OwnerSummary) string {
	if summary.NextWake.IsZero() {
		return "none scheduled"
	}
	return summary.NextWake.In(s.location(id)).Format("15:04") + " (" + relativeTime(summary.NextWake.Sub(s.opts.Now())) + ")"
}

// statusText answers /status. Post, reply and spend counts are for the local
// day in schedule.timezone; the next run is in the owner's zone.
func (s *Service) statusText(id string, summary store.OwnerSummary) string {
	tmpl := s.templates[id]
	return strings.Join([]string{
		"Status",
		"State: " + stateText(summary.Status),
		"Next run: " + s.nextRunLine(id, summary),
		"Posts today: " + countOf(summary.Posts, tmpl.Limits.PostsPerDay),
		"Replies today: " + countOf(summary.Replies, tmpl.Limits.RepliesPerDay),
		"Spend today (resets at 00:00 local): " + s.spendLine(id, summary),
	}, "\n")
}

// reportText is the daily report, also sent by /report. summary covers the 24
// hours up to now, so consecutive daily reports leave no gap, except its spend,
// which is the local spend day's because the cap applies to that day. A missing
// metric is said to be missing, never shown as zero.
func (s *Service) reportText(id string, summary store.OwnerSummary) string {
	loc, now := s.location(id), s.opts.Now()
	local := now.In(loc)
	lines := []string{
		"Daily report, " + local.Format("2 Jan 2006"),
		fmt.Sprintf("Period: last 24 hours, %s to %s %s", local.Add(-24*time.Hour).Format("2 Jan 15:04"), local.Format("2 Jan 15:04"), zoneLabel(local)),
		"State: " + stateText(summary.Status) + ", next run " + s.nextRunLine(id, summary),
		"",
		fmt.Sprintf("Posts: %d", summary.Posts),
		fmt.Sprintf("Replies: %d", summary.Replies),
	}
	if summary.MetricSamples > 0 {
		lines = append(lines, fmt.Sprintf("Metrics (%d measured): %d views, %d likes, %d replies, %d reposts", summary.MetricSamples, summary.Views, summary.Likes, summary.Responses, summary.Reposts))
	} else {
		lines = append(lines, "Metrics: none measured")
	}
	if summary.Top.ActionID != 0 {
		lines = append(lines, fmt.Sprintf("Top post: %d views, %d engagements", summary.Top.Views, summary.Top.Engagements))
		if summary.Top.URL != "" {
			lines = append(lines, summary.Top.URL)
		}
	}
	switch {
	case summary.Followers == nil:
		lines = append(lines, "Followers: not measured")
	case summary.FollowersBefore == nil:
		lines = append(lines, fmt.Sprintf("Followers: %d", *summary.Followers))
	default:
		lines = append(lines, fmt.Sprintf("Followers: %d (%+d)", *summary.Followers, *summary.Followers-*summary.FollowersBefore))
	}
	if summary.PlaybookChanges > 0 {
		lines = append(lines, fmt.Sprintf("Playbook changes: %d", summary.PlaybookChanges))
	}
	lines = append(lines, "Spend today (resets at 00:00 local): "+s.spendLine(id, summary), "")
	failed := fmt.Sprintf("Failed runs: %d", summary.FailedWakes)
	if len(summary.FailReasons) > 0 {
		failed += " (" + refusalList(summary.FailReasons) + ")"
	}
	blocked := "Blocked actions: none"
	if len(summary.Blocked) > 0 {
		blocked = "Blocked actions: " + refusalList(summary.Blocked)
	}
	return strings.Join(append(lines, failed, blocked), "\n")
}

// snippet is the one-line preview of an archived entry in the /restore list.
func snippet(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return text
}

func refusalList(refusals []store.Refusal) string {
	parts := make([]string, len(refusals))
	for i, r := range refusals {
		parts[i] = fmt.Sprintf("%s ×%d", r.Reason, r.Count)
	}
	return strings.Join(parts, ", ")
}

// silentText is the once-a-day notice for an agent that published nothing.
func silentText(hours int, lastReason string, blocked []store.Refusal) string {
	if lastReason == "" {
		lastReason = "none"
	}
	top := "none"
	if len(blocked) > 0 {
		top = refusalList(blocked)
	}
	return fmt.Sprintf("No post or reply for %d hours.\nLast run ended: %s\nBlocked actions today: %s", hours, lastReason, top)
}

// restoreListText answers /restore with IDs the owner can send back.
func restoreListText(lines []string) string {
	if len(lines) == 0 {
		return "Nothing to restore"
	}
	return "Restorable items\nm = archived memory entry, s = earlier strategy revision. Send /restore m123 or /restore s4 to restore one.\n\n" + strings.Join(lines, "\n")
}

func restoreResultText(status string) string {
	switch status {
	case "ok":
		return "Restored"
	case "invalid":
		return "Send /restore for the list, then /restore m123 or /restore s4."
	}
	return "Could not restore: the ID is unknown, expired, or already the newest."
}
