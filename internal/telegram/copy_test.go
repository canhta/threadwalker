package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
)

func copyService(templates ...config.Template) *Service {
	s := &Service{
		opts:      ServiceOptions{Now: func() time.Time { return time.Date(2026, 1, 1, 7, 15, 0, 0, time.UTC) }},
		templates: map[string]config.Template{},
		locations: map[string]*time.Location{},
	}
	for _, tmpl := range templates {
		s.templates[tmpl.ID] = tmpl
		s.ids = append(s.ids, tmpl.ID)
		s.locations[tmpl.ID] = time.FixedZone("ICT", 7*60*60)
	}
	return s
}

func sampleData() OwnerMessageData {
	return OwnerMessageData{
		Name: "Kaito", Language: "vi", AgentID: "one", Status: "running", StatusText: "đang chạy",
		NextWake: "14:33", NextIn: "còn 18 phút",
		Posts: 1, PostsLimit: 3, Replies: 0, RepliesLimit: 15,
		Cost: "$0.00", Budget: "$0.50", HasMetrics: true,
		Views: 10, Likes: 2, Responses: 3, Reposts: 4, Playbook: 3, Username: "someone", Notice: "tạm nghỉ nha",
	}
}

func TestDefaultOwnerMessagesRenderForEveryKind(t *testing.T) {
	kinds := []string{"digest", "status", "pause", "resume", "kill", "dnc", "help", "note_ack", "question_hint", "notice"}
	parts := map[string][]string{
		"digest":        {"1/3", "0/15", "$0.00", "$0.50", "14:33", "còn 18 phút"},
		"status":        {"đang chạy", "14:33", "còn 18 phút", "$0.00/$0.50"},
		"pause":         {"/resume"},
		"resume":        {},
		"kill":          {"/resume"},
		"dnc":           {"@someone"},
		"help":          {"/status", "/pause", "/resume", "/kill", "/dnc <username>"},
		"note_ack":      {},
		"question_hint": {"trả lời"},
		"notice":        {"tạm nghỉ nha"},
	}
	for _, lang := range []string{"vi", "en"} {
		for _, kind := range kinds {
			t.Run(lang+"/"+kind, func(t *testing.T) {
				s := copyService(config.Template{ID: "one", Persona: config.PersonaConfig{Language: lang}})
				text, err := s.render("one", kind, sampleData())
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(text) == "" || strings.Contains(text, "{{") {
					t.Fatalf("bad copy %q", text)
				}
				if lang == "vi" {
					for _, part := range parts[kind] {
						if !strings.Contains(text, part) {
							t.Fatalf("%s missing %q: %s", kind, part, text)
						}
					}
				}
			})
		}
	}
}

func TestMultiAgentHelpDocumentsAgentID(t *testing.T) {
	s := copyService(config.Template{ID: "one", Persona: config.PersonaConfig{Language: "vi"}}, config.Template{ID: "two", Persona: config.PersonaConfig{Language: "vi"}})
	data := sampleData()
	data.MultiAgent = true
	text, err := s.render("one", "help", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "/dnc <agent_id> <username>") {
		t.Fatalf("help omits the agent-id form: %s", text)
	}
}

func TestPersonaMessagesOverrideDefaults(t *testing.T) {
	s := copyService(config.Template{ID: "one", Persona: config.PersonaConfig{Name: "Giáo sư", Language: "vi"}, Telegram: config.TelegramConfig{Messages: map[string]string{
		"digest": "tao là {{.Name}}, hôm nay đăng {{.Posts}} bài{{if .Budget}}, tiêu {{.Cost}} trên {{.Budget}}{{end}}",
	}}})
	data := sampleData()
	data.Name = "Giáo sư"
	text, err := s.render("one", "digest", data)
	if err != nil {
		t.Fatal(err)
	}
	if text != "tao là Giáo sư, hôm nay đăng 1 bài, tiêu $0.00 trên $0.50" {
		t.Fatalf("override not used: %s", text)
	}
}

func TestUnknownMessageTemplateFails(t *testing.T) {
	s := copyService()
	if _, err := s.render("one", "digest", sampleData()); err == nil {
		t.Fatal("unconfigured agent rendered")
	}
	s = copyService(config.Template{ID: "one"})
	if _, err := s.render("one", "not-a-kind", sampleData()); err == nil {
		t.Fatal("unknown kind rendered")
	}
}

func TestRelativeTimeAndMoney(t *testing.T) {
	for _, tc := range []struct {
		d  time.Duration
		vi string
		en string
	}{
		{-time.Minute, "tới giờ rồi", "due now"},
		{30 * time.Second, "dưới 1 phút", "under a minute"},
		{18 * time.Minute, "còn 18 phút", "in 18 min"},
		{2*time.Hour + 5*time.Minute, "còn 2h05p", "in 2h05m"},
		{49 * time.Hour, "còn 2 ngày", "in 2 days"},
	} {
		if got := relativeTime("vi", tc.d); got != tc.vi {
			t.Errorf("vi %v = %q, want %q", tc.d, got, tc.vi)
		}
		if got := relativeTime("en", tc.d); got != tc.en {
			t.Errorf("en %v = %q, want %q", tc.d, got, tc.en)
		}
	}
	if money(0.5) != "$0.50" || money(0) != "$0.00" {
		t.Fatal("money format")
	}
}
