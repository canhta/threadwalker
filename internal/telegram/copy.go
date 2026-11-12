package telegram

import (
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/store"
)

// OwnerMessageData is the complete, code-owned input to owner-facing copy. It
// carries identifiers, exact facts and persona fields only; Threads content,
// transport payloads and chat metadata are never added.
type OwnerMessageData struct {
	Name         string
	Language     string
	AgentID      string
	MultiAgent   bool
	Status       string
	StatusText   string
	NotRunning   bool
	NextWake     string
	NextIn       string
	Posts        int
	PostsLimit   int
	Replies      int
	RepliesLimit int
	Cost         string
	Budget       string
	// Phase split of today's spend. HasPhaseSpend hides the line until study or
	// dream spend exists, so normal wakes keep the old digest length.
	WakeCost      string
	StudyCost     string
	DreamCost     string
	HasPhaseSpend bool
	HasMetrics    bool
	Views         int
	Likes         int
	Responses     int
	Reposts       int
	Playbook      int
	Username      string
	Notice        string
}

// defaultOwnerMessages are neutral fallbacks for a template that does not
// override a message. A persona's own voice belongs in telegram.messages in its
// agent template; these strings carry facts and no persona-specific slang.
func defaultOwnerMessages(lang string) map[string]string {
	if strings.HasPrefix(strings.ToLower(lang), "vi") {
		return map[string]string{
			"digest":        "hôm nay đăng {{.Posts}}{{if .PostsLimit}}/{{.PostsLimit}}{{end}} bài, reply {{.Replies}}{{if .RepliesLimit}}/{{.RepliesLimit}}{{end}}, xài {{.Cost}}{{if .Budget}} trên {{.Budget}}{{end}}.{{if .HasPhaseSpend}}{{nl}}chia ra: thức {{.WakeCost}} · bắt trend {{.StudyCost}} · mơ {{.DreamCost}}{{end}}{{if .HasMetrics}}{{nl}}số liệu mới nhất: {{.Views}} view, {{.Likes}} like, {{.Responses}} reply, {{.Reposts}} repost.{{end}}{{if .Playbook}}{{nl}}playbook vừa sửa {{.Playbook}} chỗ.{{end}}{{if .NotRunning}}{{nl}}{{.StatusText}}.{{end}}{{if .NextWake}}{{nl}}{{.NextWake}} ({{.NextIn}}) chạy tiếp nha.{{end}}",
			"status":        "{{.StatusText}}.{{if .NextWake}}{{nl}}lần dậy tới: {{.NextWake}} ({{.NextIn}}){{else}}{{nl}}chưa có lần dậy tiếp theo{{end}}{{nl}}hôm nay: {{.Posts}}{{if .PostsLimit}}/{{.PostsLimit}}{{end}} bài · {{.Replies}}{{if .RepliesLimit}}/{{.RepliesLimit}}{{end}} reply · {{.Cost}}{{if .Budget}}/{{.Budget}}{{end}}{{if .HasPhaseSpend}}{{nl}}chia ra: thức {{.WakeCost}} · bắt trend {{.StudyCost}} · mơ {{.DreamCost}}{{end}}{{if .HasMetrics}}{{nl}}số liệu: {{.Views}} view · {{.Likes}} like · {{.Responses}} reply · {{.Reposts}} repost{{end}}{{if .Playbook}}{{nl}}playbook: {{.Playbook}} chỗ vừa sửa{{end}}",
			"pause":         "ok, tạm dừng đây. gõ /resume để chạy tiếp nha.",
			"resume":        "ok, chạy tiếp đây.",
			"kill":          "ok, tắt hẳn luôn. /resume ko cứu được nữa đâu.",
			"dnc":           "ok, né @{{.Username}} ra, ko đụng nữa.",
			"help":          "mấy lệnh điều khiển:{{nl}}/status — xem đang làm gì{{nl}}/pause — tạm dừng{{nl}}/resume — chạy tiếp{{nl}}/kill — tắt hẳn, ko bật lại được{{nl}}/dnc {{if .MultiAgent}}<agent_id> {{end}}<username> — né username đó ra",
			"note_ack":      "ok, ghi lại rồi. lần dậy tới đọc nha.",
			"question_hint": "↩️ trả lời ngay tin nhắn này để gửi lại nha",
			"notice":        "{{.Notice}}",
		}
	}
	return map[string]string{
		"digest":        "today: {{.Posts}}{{if .PostsLimit}}/{{.PostsLimit}}{{end}} posts, {{.Replies}}{{if .RepliesLimit}}/{{.RepliesLimit}}{{end}} replies, spent {{.Cost}}{{if .Budget}} of {{.Budget}}{{end}}.{{if .HasPhaseSpend}}{{nl}}split: wake {{.WakeCost}} · study {{.StudyCost}} · dream {{.DreamCost}}{{end}}{{if .HasMetrics}}{{nl}}latest: {{.Views}} views, {{.Likes}} likes, {{.Responses}} replies, {{.Reposts}} reposts.{{end}}{{if .Playbook}}{{nl}}playbook changed {{.Playbook}} places.{{end}}{{if .NotRunning}}{{nl}}{{.StatusText}}.{{end}}{{if .NextWake}}{{nl}}next run {{.NextWake}} ({{.NextIn}}).{{end}}",
		"status":        "{{.StatusText}}.{{if .NextWake}}{{nl}}next run: {{.NextWake}} ({{.NextIn}}){{else}}{{nl}}no next run scheduled{{end}}{{nl}}today: {{.Posts}}{{if .PostsLimit}}/{{.PostsLimit}}{{end}} posts · {{.Replies}}{{if .RepliesLimit}}/{{.RepliesLimit}}{{end}} replies · {{.Cost}}{{if .Budget}}/{{.Budget}}{{end}}{{if .HasPhaseSpend}}{{nl}}split: wake {{.WakeCost}} · study {{.StudyCost}} · dream {{.DreamCost}}{{end}}{{if .HasMetrics}}{{nl}}metrics: {{.Views}} views · {{.Likes}} likes · {{.Responses}} replies · {{.Reposts}} reposts{{end}}{{if .Playbook}}{{nl}}playbook: {{.Playbook}} edits{{end}}",
		"pause":         "ok, pausing. send /resume to wake me.",
		"resume":        "ok, back to work.",
		"kill":          "ok, shut down for good. /resume can't bring me back.",
		"dnc":           "ok, avoiding @{{.Username}} from now on.",
		"help":          "control commands:{{nl}}/status — what i'm doing{{nl}}/pause — pause me{{nl}}/resume — wake me up{{nl}}/kill — shut down for good{{nl}}/dnc {{if .MultiAgent}}<agent_id> {{end}}<username> — never reply to that username",
		"note_ack":      "ok, noted. i'll read it on the next wake.",
		"question_hint": "↩️ reply to this message and i'll get it",
		"notice":        "{{.Notice}}",
	}
}

func messageLanguage(tmpl config.Template) string { return tmpl.Persona.Language }

func statusText(lang, status string) string {
	vi := strings.HasPrefix(strings.ToLower(lang), "vi")
	switch status {
	case "running":
		if vi {
			return "đang chạy"
		}
		return "running"
	case "paused":
		if vi {
			return "đang nghỉ"
		}
		return "paused"
	case "killed":
		if vi {
			return "đã tắt hẳn"
		}
		return "stopped for good"
	default:
		return status
	}
}

// relativeTime is a short owner-facing phrase, not a precise duration.
func relativeTime(lang string, d time.Duration) string {
	vi := strings.HasPrefix(strings.ToLower(lang), "vi")
	if d <= 0 {
		if vi {
			return "tới giờ rồi"
		}
		return "due now"
	}
	minutes := int(d / time.Minute)
	switch {
	case minutes < 1:
		if vi {
			return "dưới 1 phút"
		}
		return "under a minute"
	case minutes < 60:
		if vi {
			return fmt.Sprintf("còn %d phút", minutes)
		}
		return fmt.Sprintf("in %d min", minutes)
	case d < 24*time.Hour:
		if vi {
			return fmt.Sprintf("còn %dh%02dp", minutes/60, minutes%60)
		}
		return fmt.Sprintf("in %dh%02dm", minutes/60, minutes%60)
	default:
		days := int(d / (24 * time.Hour))
		if vi {
			return fmt.Sprintf("còn %d ngày", days)
		}
		return fmt.Sprintf("in %d days", days)
	}
}

func money(amount float64) string { return fmt.Sprintf("$%.2f", amount) }

// noticeText localizes code-owned notice kinds. Only these kinds exist.
func noticeText(lang, kind string) string {
	vi := strings.HasPrefix(strings.ToLower(lang), "vi")
	switch kind {
	case "daily_limit":
		if vi {
			return "chạm giới hạn hành động hôm nay rồi, mai làm tiếp nha"
		}
		return "daily action limit reached; continuing tomorrow"
	case "budget_80":
		if vi {
			return "đã xài 80% budget token hôm nay rồi đó"
		}
		return "daily token budget is at 80%"
	case "budget_100":
		if vi {
			return "hết budget token hôm nay rồi, dừng tới mai nha"
		}
		return "daily token budget is spent; stopped until tomorrow"
	case "logged_out":
		if vi {
			return "bị đăng xuất khỏi trình duyệt rồi, tạm nghỉ nha"
		}
		return "browser logged out; paused"
	case "captcha":
		if vi {
			return "dính captcha rồi, tạm nghỉ nha"
		}
		return "captcha detected; paused"
	case "killed":
		if vi {
			return "đã bị tắt hẳn"
		}
		return "stopped for good"
	default:
		return ""
	}
}

func (s *Service) location(id string) *time.Location {
	if loc := s.locations[id]; loc != nil {
		return loc
	}
	return time.UTC
}

func (s *Service) ownerData(id string, summary store.OwnerSummary) OwnerMessageData {
	tmpl := s.templates[id]
	now := s.opts.Now()
	lang := messageLanguage(tmpl)
	data := OwnerMessageData{
		Name:          tmpl.Persona.Name,
		Language:      lang,
		AgentID:       id,
		MultiAgent:    len(s.ids) > 1,
		Status:        summary.Status,
		StatusText:    statusText(lang, summary.Status),
		NotRunning:    summary.Status != "" && summary.Status != "running",
		Posts:         summary.Posts,
		PostsLimit:    tmpl.Limits.PostsPerDay,
		Replies:       summary.Replies,
		RepliesLimit:  tmpl.Limits.RepliesPerDay,
		Cost:          money(summary.CostUSD),
		WakeCost:      money(summary.WakeCostUSD),
		StudyCost:     money(summary.StudyCostUSD),
		DreamCost:     money(summary.DreamCostUSD),
		HasPhaseSpend: summary.StudyCostUSD > 0 || summary.DreamCostUSD > 0,
		HasMetrics:    summary.MetricSamples > 0,
		Views:         summary.Views,
		Likes:         summary.Likes,
		Responses:     summary.Responses,
		Reposts:       summary.Reposts,
		Playbook:      summary.PlaybookChanges,
	}
	if tmpl.Limits.MaxUSDPerDay > 0 {
		data.Budget = money(tmpl.Limits.MaxUSDPerDay)
	}
	if !summary.NextWake.IsZero() {
		data.NextWake = summary.NextWake.In(s.location(id)).Format("15:04")
		data.NextIn = relativeTime(lang, summary.NextWake.Sub(now))
	}
	return data
}

// render fills one message kind. The template's telegram.messages entry wins;
// otherwise a neutral language default applies. A broken template is a code or
// configuration bug caught at startup, so the error is returned, not hidden.
func (s *Service) render(id, kind string, data OwnerMessageData) (string, error) {
	tmpl, ok := s.templates[id]
	if !ok {
		return "", fmt.Errorf("rendering owner message: unconfigured agent")
	}
	source := tmpl.Telegram.Messages[kind]
	if source == "" {
		source = defaultOwnerMessages(messageLanguage(tmpl))[kind]
	}
	if source == "" {
		return "", fmt.Errorf("rendering owner message: unknown kind")
	}
	parsed, err := template.New(kind).Option("missingkey=error").Funcs(template.FuncMap{"nl": func() string { return "\n" }}).Parse(source)
	if err != nil {
		return "", fmt.Errorf("rendering owner message: parse failed")
	}
	var out strings.Builder
	if err = parsed.Execute(&out, data); err != nil {
		return "", fmt.Errorf("rendering owner message: execute failed")
	}
	return strings.TrimSpace(out.String()), nil
}

func (s *Service) summaryText(id string, summary store.OwnerSummary, digest bool) (string, error) {
	kind := "status"
	if digest {
		kind = "digest"
	}
	return s.render(id, kind, s.ownerData(id, summary))
}

func (s *Service) receiptText(id, command, username string) (string, error) {
	data := s.ownerData(id, store.OwnerSummary{})
	data.Username = username
	return s.render(id, command, data)
}

func (s *Service) helpText(id string) (string, error) {
	return s.render(id, "help", s.ownerData(id, store.OwnerSummary{}))
}

func (s *Service) noteAck(id string) (string, error) {
	return s.render(id, "note_ack", s.ownerData(id, store.OwnerSummary{}))
}
