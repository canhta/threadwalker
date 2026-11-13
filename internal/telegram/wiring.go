package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

// Services makes one poller per bot. Telegram offsets belong to a bot, so a
// token configured with conflicting owner chats is rejected rather than polled
// by competing consumers. Disabled configurations never create HTTP clients.
func Services(st *store.Store, templates []config.Template, now func() time.Time, logger *slog.Logger) ([]*Service, map[string]*Service, error) {
	type group struct {
		owner     int64
		token     config.Secret
		templates []config.Template
	}
	groups := map[string]*group{}
	redactor := config.NewRedactor(templates)
	for _, t := range templates {
		if !t.Telegram.Enabled {
			continue
		}
		token := t.Telegram.BotToken.Expose()
		owner, err := strconv.ParseInt(t.Telegram.OwnerChatID.Expose(), 10, 64)
		if token == "" || err != nil || owner == 0 {
			return nil, nil, errors.New("configuring Telegram: enabled integration requires valid runtime credentials")
		}
		sum := sha256.Sum256([]byte(token))
		key := hex.EncodeToString(sum[:])
		g := groups[key]
		if g == nil {
			g = &group{owner: owner, token: t.Telegram.BotToken}
			groups[key] = g
		} else if g.owner != owner {
			return nil, nil, errors.New("configuring Telegram: one bot must have one configured owner chat")
		}
		g.templates = append(g.templates, t)
	}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	services := []*Service{}
	bindings := map[string]*Service{}
	for _, key := range keys {
		g := groups[key]
		client, err := NewClient(ClientOptions{Token: g.token, Timeout: 30 * time.Second})
		if err != nil {
			return nil, nil, err
		}
		s, err := NewService(ServiceOptions{Store: st, Bot: client, BotKey: key, OwnerChatID: g.owner, Templates: g.templates, Now: now, Logger: logger, Redactor: redactor})
		if err != nil {
			return nil, nil, err
		}
		services = append(services, s)
		for _, t := range g.templates {
			bindings[t.ID] = s
		}
	}
	return services, bindings, nil
}
