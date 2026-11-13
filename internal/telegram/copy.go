package telegram

import (
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
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
	// dream spend exists.
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
			"help":          "mấy lệnh điều khiển:{{nl}}/status — xem đang làm gì{{nl}}/pause — tạm dừng{{nl}}/resume — chạy tiếp{{nl}}/kill — tắt hẳn, ko bật lại được{{nl}}/restore {{if .MultiAgent}}<agent_id> {{end}}[m123 | s4] — xem và khôi phục trí nhớ hoặc chiến lược agent đã bỏ{{nl}}/cookie <cookie> — đăng nhập lại Threads (xuất JSON bằng Cookie-Editor; hoặc dán header Cookie){{nl}}/dnc {{if .MultiAgent}}<agent_id> {{end}}<username> — né username đó ra",
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
		"help":          "control commands:{{nl}}/status — what i'm doing{{nl}}/pause — pause me{{nl}}/resume — wake me up{{nl}}/kill — shut down for good{{nl}}/restore {{if .MultiAgent}}<agent_id> {{end}}[m123 | s4] — list and restore memory or strategy the agent dropped{{nl}}/cookie <cookie> — log in to Threads again (Cookie-Editor JSON export, or a Cookie header){{nl}}/dnc {{if .MultiAgent}}<agent_id> {{end}}<username> — never reply to that username",
		"note_ack":      "ok, noted. i'll read it on the next wake.",
		"question_hint": "↩️ reply to this message and i'll get it",
		"notice":        "{{.Notice}}",
	}
}

// CookieHowTo is the line that tells the owner how to log the agent in again.
func CookieHowTo(lang string) string {
	if strings.HasPrefix(strings.ToLower(lang), "vi") {
		return "gửi /cookie kèm cookie của tài khoản đã đăng nhập threads.com: dùng Cookie-Editor → Export → JSON (dán cả mảng JSON), hoặc copy header Cookie của một request trong tab Network của devtools. Tin nhắn sẽ bị xóa ngay. Hoặc chạy: threadz ctl threads cookies --agent ID < file"
	}
	return "send /cookie followed by the cookies of a logged-in threads.com browser: Cookie-Editor → Export → JSON (paste the whole array), or copy the Cookie header of any logged-in threads.com request from the devtools Network tab. The message is deleted right away. Or run: threadz ctl threads cookies --agent ID < file"
}

// CookieResultText is the one-line answer to a cookie import.
func CookieResultText(lang, status string) string {
	vi := strings.HasPrefix(strings.ToLower(lang), "vi")
	switch status {
	case "ok":
		if vi {
			return "đăng nhập xong, chạy tiếp"
		}
		return "logged in; agent resumed"
	case "missing_sessionid":
		if vi {
			return "thiếu sessionid"
		}
		return "missing sessionid"
	case "bad_cookies":
		if vi {
			return "ko đọc được cookie (cần JSON export hoặc header Cookie của threads.com/instagram.com, có sessionid và ds_user_id)"
		}
		return "could not read the cookies (need a JSON export or a Cookie header from threads.com or instagram.com with sessionid and ds_user_id)"
	case "logged_out":
		if vi {
			return "đã nhập cookie nhưng vẫn chưa đăng nhập, cookie hết hạn hoặc ko phải của tài khoản đã đăng nhập"
		}
		return "cookies imported but Threads still shows logged out; they are expired or not from a logged-in browser"
	case "captcha":
		if vi {
			return "Threads hiện captcha, chưa vào được"
		}
		return "Threads showed a captcha"
	case "busy":
		if vi {
			return "agent đang dùng trình duyệt, thử lại sau vài phút"
		}
		return "the agent is using the browser; try again in a few minutes"
	}
	if vi {
		return "nhập cookie ko thành công, thử lại sau"
	}
	return "cookie import failed; try again later"
}

const credentialRefused = "not saved: that looked like a credential"

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
	case "logged_in":
		if vi {
			return "đăng nhập lại xong, chạy tiếp nha"
		}
		return "logged back in; resuming"
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

// failureLine is the digest line with the day's failed wakes and top refusal
// reasons; empty when the day had neither.
func failureLine(lang string, summary store.OwnerSummary) string {
	if summary.FailedWakes == 0 && len(summary.Refusals) == 0 {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(lang), "vi") {
		return fmt.Sprintf("lần dậy hỏng: %d · lý do bị chặn nhiều nhất: %s", summary.FailedWakes, refusalList(summary.Refusals))
	}
	return fmt.Sprintf("failed wakes: %d · top refusal reasons: %s", summary.FailedWakes, refusalList(summary.Refusals))
}

// silentText is the once-a-day notice for an agent that published nothing.
func silentText(lang string, hours int, lastReason string, refusals []store.Refusal) string {
	if lastReason == "" {
		lastReason = "-"
	}
	top := "-"
	if len(refusals) > 0 {
		top = refusalList(refusals)
	}
	if strings.HasPrefix(strings.ToLower(lang), "vi") {
		return fmt.Sprintf("%d giờ rồi ko đăng bài hay reply nào; lần dậy gần nhất kết thúc vì %s; lý do bị chặn nhiều nhất hôm nay: %s", hours, lastReason, top)
	}
	return fmt.Sprintf("no post or reply for %d hours; last wake ended %s; top refusal reasons today: %s", hours, lastReason, top)
}

// restoreListText answers /restore with ids the owner can send back.
func restoreListText(lang string, lines []string) string {
	vi := strings.HasPrefix(strings.ToLower(lang), "vi")
	if len(lines) == 0 {
		if vi {
			return "chưa có gì để khôi phục"
		}
		return "nothing to restore"
	}
	head := "m = trí nhớ đã bỏ, s = chiến lược cũ. gửi /restore m123 hoặc /restore s4 để khôi phục:"
	if !vi {
		head = "m = archived memory, s = earlier strategy. send /restore m123 or /restore s4 to restore:"
	}
	return head + "\n" + strings.Join(lines, "\n")
}

func restoreResultText(lang, status string) string {
	vi := strings.HasPrefix(strings.ToLower(lang), "vi")
	switch status {
	case "ok":
		if vi {
			return "khôi phục xong"
		}
		return "restored"
	case "invalid":
		if vi {
			return "gửi /restore để xem danh sách, rồi /restore m123 hoặc /restore s4"
		}
		return "send /restore for the list, then /restore m123 or /restore s4"
	}
	if vi {
		return "ko khôi phục được (id sai, đã hết hạn hoặc là bản mới nhất)"
	}
	return "could not restore (unknown id, expired, or already the newest)"
}
