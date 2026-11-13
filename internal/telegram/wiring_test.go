package telegram_test

import (
	"context"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
	"testing"
)

func TestServicesGroupBotsAndRejectConflictingOwners(t *testing.T) {
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tmpl := func(id, token, owner string, enabled bool) config.Template {
		return config.Template{ID: id, Telegram: config.TelegramConfig{Enabled: enabled, BotToken: config.NewSecret(token), OwnerChatID: config.NewSecret(owner)}}
	}
	for _, tc := range []struct {
		name      string
		templates []config.Template
		count     int
		invalid   bool
	}{{"disabled", []config.Template{tmpl("one", "", "", false)}, 0, false}, {"shared", []config.Template{tmpl("one", "fake-bot", "77", true), tmpl("two", "fake-bot", "77", true)}, 1, false}, {"separate", []config.Template{tmpl("one", "fake-one", "77", true), tmpl("two", "fake-two", "77", true)}, 2, false}, {"conflicting_owners", []config.Template{tmpl("one", "fake-bot", "77", true), tmpl("two", "fake-bot", "99", true)}, 0, true}, {"bad_owner", []config.Template{tmpl("one", "fake-bot", "not-an-id", true)}, 0, true}, {"missing_token", []config.Template{tmpl("one", "", "77", true)}, 0, true}, {"invalid_token", []config.Template{tmpl("one", "invalid/token", "77", true)}, 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			services, bindings, err := telegram.Services(st, tc.templates, nil, nil)
			if (err != nil) != tc.invalid {
				t.Fatalf("error %v", err)
			}
			if err == nil && len(services) != tc.count {
				t.Fatalf("groups %d", len(services))
			}
			if tc.name == "shared" && bindings["one"] != bindings["two"] {
				t.Fatal("shared bot got competing pollers")
			}
			if tc.name == "disabled" && len(bindings) != 0 {
				t.Fatal("disabled binding")
			}
		})
	}
}
