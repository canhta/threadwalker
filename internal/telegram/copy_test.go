package telegram

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

func copyService(templates ...config.Template) *Service {
	s := &Service{
		opts:      ServiceOptions{Now: func() time.Time { return time.Date(2026, 1, 1, 14, 15, 0, 0, time.UTC) }},
		templates: map[string]config.Template{},
		locations: map[string]*time.Location{},
	}
	for _, tmpl := range templates {
		s.templates[tmpl.ID] = tmpl
		s.ids = append(s.ids, tmpl.ID)
		s.locations[tmpl.ID] = time.FixedZone("", 7*60*60)
	}
	return s
}

func TestReportText(t *testing.T) {
	s := copyService(config.Template{ID: "one", Limits: config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 15, MaxUSDPerDay: 0.5}})
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	followers, before := int64(1034), int64(1022)
	full := store.OwnerSummary{
		Status: "running", NextWake: day.Add(14*time.Hour + 45*time.Minute),
		Posts: 2, Replies: 5, CostUSD: 0.42, WakeCostUSD: 0.3, StudyCostUSD: 0.1, DreamCostUSD: 0.02,
		MetricSamples: 4, Views: 3400, Likes: 120, Responses: 30, Reposts: 8, PlaybookChanges: 1,
		Top:       store.TopPost{ActionID: 7, URL: "https://www.threads.com/@a/post/x", Views: 1234, Engagements: 56},
		Followers: &followers, FollowersBefore: &before,
		FailedWakes: 1, FailReasons: []store.Refusal{{Reason: "step_limit", Count: 1}},
		Blocked: []store.Refusal{{Reason: "blocked_phrase", Count: 3}, {Reason: "quota", Count: 1}},
	}
	want := `Daily report, 1 Jan 2026
Period: last 24 hours, 31 Dec 21:15 to 1 Jan 21:15 UTC+07
State: Running, next run 21:45 (in 30 min)

Posts: 2
Replies: 5
Metrics (4 measured): 3400 views, 120 likes, 30 replies, 8 reposts
Top post: 1234 views, 56 engagements
https://www.threads.com/@a/post/x
Followers: 1034 (+12)
Playbook changes: 1
Spend today (resets at 00:00 local): $0.42 of $0.50 (84%); wake $0.30, study $0.10, dream $0.02

Failed runs: 1 (step_limit ×1)
Blocked actions: blocked_phrase ×3, quota ×1`
	if got := s.reportText("one", full); got != want {
		t.Fatalf("report:\n%s\nwant:\n%s", got, want)
	}
	// Missing measurements say so instead of showing zero.
	empty := s.reportText("one", store.OwnerSummary{Status: "paused"})
	for _, part := range []string{"State: Paused, next run none scheduled", "Metrics: none measured\n", "Followers: not measured\n", "Failed runs: 0", "Blocked actions: none"} {
		if !strings.Contains(empty, part) {
			t.Fatalf("empty report missing %q:\n%s", part, empty)
		}
	}
	if strings.Contains(empty, "Top post") || strings.Contains(empty, "Playbook") {
		t.Fatalf("empty report shows absent facts:\n%s", empty)
	}
}

func TestStatusText(t *testing.T) {
	s := copyService(config.Template{ID: "one", Limits: config.LimitsConfig{PostsPerDay: 3}})
	got := s.statusText("one", store.OwnerSummary{Status: "running", Posts: 1, CostUSD: 0.1})
	want := "Status\nState: Running\nNext run: none scheduled\nPosts today: 1 of 3\nReplies today: 0\nSpend today (resets at 00:00 local): $0.10"
	if got != want {
		t.Fatalf("status:\n%s", got)
	}
}

func TestCommandMenuAndHelpShareOneTable(t *testing.T) {
	name := regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	for _, multi := range []bool{false, true} {
		menu := ownerCommands(multi)
		help := helpText(multi)
		for _, c := range menu {
			if !name.MatchString(c.Command) || len(c.Description) == 0 || len(c.Description) > 256 {
				t.Fatalf("invalid Bot API command %+v", c)
			}
			if c.Command != "start" && !strings.Contains(help, "\n/"+c.Command) {
				t.Fatalf("help omits /%s:\n%s", c.Command, help)
			}
		}
		if multi != strings.Contains(help, "/dnc <agent_id> <username>") {
			t.Fatalf("multi=%v help agent-id form wrong:\n%s", multi, help)
		}
	}
	for _, want := range []string{"start", "help", "status", "report", "pause", "resume"} {
		found := false
		for _, c := range ownerCommands(false) {
			found = found || c.Command == want
		}
		if !found {
			t.Fatalf("menu lacks /%s", want)
		}
	}
}

func TestNoticeKindsHaveCopy(t *testing.T) {
	for _, kind := range []string{"daily_limit", "budget_80", "budget_100", "logged_out", "logged_in", "captcha", "killed"} {
		if noticeText(kind, "07:00 UTC+07", "00:00 UTC+07") == "" {
			t.Fatalf("no copy for %s", kind)
		}
	}
	if noticeText("other", "07:00 UTC+07", "00:00 UTC+07") != "" {
		t.Fatal("unknown notice kind has copy")
	}
}

func TestRelativeTimeAndMoney(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Minute, "due now"},
		{30 * time.Second, "in under a minute"},
		{18 * time.Minute, "in 18 min"},
		{2*time.Hour + 5*time.Minute, "in 2h 05min"},
		{49 * time.Hour, "in 2 days"},
	} {
		if got := relativeTime(tc.d); got != tc.want {
			t.Errorf("%v = %q, want %q", tc.d, got, tc.want)
		}
	}
	if money(0.5) != "$0.50" || money(0) != "$0.00" {
		t.Fatal("money format")
	}
	if zoneLabel(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) != "UTC" {
		t.Fatal("UTC zone label")
	}
}
