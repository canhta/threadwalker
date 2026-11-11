package guard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/canhta/threadwalker/internal/store"
)

type actionIntent struct {
	Text           string  `json:"text"`
	TargetURL      string  `json:"target_url"`
	TargetUsername string  `json:"target_username"`
	HypothesisIDs  []int64 `json:"hypothesis_ids"`
	thread         string
}

var mentions = regexp.MustCompile(`@[\p{L}\p{N}_.]+`)
var links = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://|(?:https?|ftp|mailto|data|javascript|file|tel):|www\.|//[\p{L}\p{N}]|[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?(?:\.[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?)+)`)

func canonicalThread(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", errors.New("invalid thread URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func (g *Guard) content(a Attempt) (*actionIntent, string) {
	if a.Tool != "post" && a.Tool != "reply" {
		return nil, ""
	}
	var intent actionIntent
	if !utf8.ValidString(a.Arguments) || json.Unmarshal([]byte(a.Arguments), &intent) != nil || strings.TrimSpace(intent.Text) == "" {
		return nil, "invalid_tool_arguments"
	}
	if utf8.RuneCountInString(intent.Text) > 500 {
		return nil, "text_length"
	}
	folded := strings.ToLower(intent.Text)
	for _, phrase := range g.opts.Template.Persona.ForbiddenPhrases {
		if phrase != "" && strings.Contains(folded, strings.ToLower(phrase)) {
			return nil, "forbidden_phrase"
		}
	}
	if a.Tool == "reply" {
		username, err := store.NormalizeUsername(intent.TargetUsername)
		if err != nil {
			return nil, "invalid_tool_arguments"
		}
		intent.TargetUsername = username
		thread, err := canonicalThread(intent.TargetURL)
		if err != nil {
			return nil, "invalid_tool_arguments"
		}
		intent.thread = thread
		// An author mention may contain dots; check URL syntax separately
		// from mention identity so mailto/HTTP links retain the URL rule ID.
		if links.MatchString(mentions.ReplaceAllString(intent.Text, "")) {
			return nil, "reply_url"
		}
		for _, mention := range mentions.FindAllString(intent.Text, -1) {
			normalized, err := store.NormalizeUsername(mention)
			if err != nil || normalized != username {
				return nil, "reply_mention"
			}
		}
	}
	l := g.opts.Template.Limits
	if l.PostsPerDay <= 0 || l.RepliesPerDay <= 0 || l.RepliesPerUser7d <= 0 || l.MinSecondsBetweenActions <= 0 {
		return nil, "invalid_policy"
	}
	return &intent, ""
}

// reserve runs after the attempted-call audit is committed. BEGIN IMMEDIATE
// serializes check+intent+contact writes across guards, stores and processes.
// The committed intent survives failed/unknown effects and is never replayed.
func (g *Guard) reserve(ctx context.Context, a Attempt, intent *actionIntent, auditID int64, now time.Time) (id int64, reason string, err error) {
	conn, err := g.opts.Store.DB().Conn(ctx)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=1000`); err != nil {
		return 0, "", err
	}
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return 0, "", err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()
	var running, open bool
	err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND status='running'),EXISTS(SELECT 1 FROM wakes WHERE id=? AND agent_id=? AND ended_at IS NULL)`, g.opts.Template.ID, g.opts.WakeID, g.opts.Template.ID).Scan(&running, &open)
	if err != nil {
		return 0, "", err
	}
	if !running {
		reason = "agent_not_running"
	} else if !open {
		reason = "wake_not_open"
	}
	if reason == "" {
		reason, err = g.quotas(ctx, conn, a.Tool, intent, now)
		if err != nil {
			return 0, "", err
		}
	}
	if reason == "" {
		for _, hypothesis := range intent.HypothesisIDs {
			var valid bool
			err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM playbook WHERE id=? AND agent_id=? AND status='testing')`, hypothesis, g.opts.Template.ID).Scan(&valid)
			if err != nil {
				return 0, "", err
			}
			if !valid {
				reason = "invalid_hypothesis"
				break
			}
		}
	}
	if reason == "" {
		ids := intent.HypothesisIDs
		if ids == nil {
			ids = []int64{}
		}
		encoded, _ := json.Marshal(ids)
		var result sql.Result
		result, err = conn.ExecContext(ctx, `INSERT INTO actions(agent_id,kind,text,target_url,target_username,hypothesis_ids_json,dry_run,posted_at,thread_url) VALUES(?,?,?,?,?,?,?,?,?)`, g.opts.Template.ID, a.Tool, intent.Text, intent.TargetURL, intent.TargetUsername, string(encoded), g.opts.Template.DryRun, now.Format(time.RFC3339Nano), intent.thread)
		if err != nil {
			return 0, "", err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return 0, "", err
		}
		if a.Tool == "reply" {
			_, err = conn.ExecContext(ctx, `INSERT INTO contacts(agent_id,username,reply_count_7d) VALUES(?,?,1) ON CONFLICT(agent_id,username) DO UPDATE SET reply_count_7d=contacts.reply_count_7d+1`, g.opts.Template.ID, intent.TargetUsername)
			if err != nil {
				return 0, "", err
			}
		}
	}
	if reason != "" {
		_, err = conn.ExecContext(ctx, `UPDATE tool_calls SET blocked_by=? WHERE id=?`, reason, auditID)
		if err != nil {
			return 0, "", err
		}
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return id, reason, err
}

func (g *Guard) quotas(ctx context.Context, conn *sql.Conn, kind string, intent *actionIntent, now time.Time) (string, error) {
	l := g.opts.Template.Limits
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := now.Add(-7 * 24 * time.Hour)
	posts, replies, contact := 0, 0, 0
	threadUsed := false
	var latest time.Time
	rows, err := conn.QueryContext(ctx, `SELECT kind,posted_at,target_username,target_url,thread_url FROM actions WHERE agent_id=?`, g.opts.Template.ID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var k, stamp, user, target, thread string
		if err = rows.Scan(&k, &stamp, &user, &target, &thread); err != nil {
			break
		}
		var at time.Time
		at, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			break
		}
		if at.After(latest) {
			latest = at
		}
		// Future persisted intents are counted conservatively after clock rollback.
		if !at.Before(day) && at.Before(day.Add(24*time.Hour)) {
			if k == "post" {
				posts++
			} else if k == "reply" {
				replies++
			}
		}
		if k != "reply" {
			continue
		}
		normalized, e := store.NormalizeUsername(user)
		if e != nil {
			err = errors.New("invalid stored contact")
			break
		}
		if normalized == intent.TargetUsername && at.After(from) && !at.After(now) {
			contact++
		}
		if thread == "" {
			thread = target
		}
		canonical, e := canonicalThread(thread)
		if e != nil {
			err = e
			break
		}
		if !at.Before(day) && at.Before(day.Add(24*time.Hour)) && canonical == intent.thread {
			threadUsed = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", err
	}
	// Refresh the materialized counters, preserving every permanent flag/reason.
	_, err = conn.ExecContext(ctx, `UPDATE contacts SET reply_count_7d=0 WHERE agent_id=?`, g.opts.Template.ID)
	if err != nil {
		return "", err
	}
	// All contact counters are derived from intents with exact Go timestamp parsing.
	rows, err = conn.QueryContext(ctx, `SELECT target_username,posted_at FROM actions WHERE agent_id=? AND kind='reply'`, g.opts.Template.ID)
	if err != nil {
		return "", err
	}
	counts := map[string]int{}
	for rows.Next() {
		var user, stamp string
		if err = rows.Scan(&user, &stamp); err != nil {
			break
		}
		var at time.Time
		at, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			break
		}
		var normalized string
		normalized, err = store.NormalizeUsername(user)
		if err != nil {
			break
		}
		if at.After(from) && !at.After(now) {
			counts[normalized]++
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", err
	}
	for user, n := range counts {
		_, err = conn.ExecContext(ctx, `INSERT INTO contacts(agent_id,username,reply_count_7d) VALUES(?,?,?) ON CONFLICT(agent_id,username) DO UPDATE SET reply_count_7d=excluded.reply_count_7d`, g.opts.Template.ID, user, n)
		if err != nil {
			return "", err
		}
	}
	if kind == "reply" {
		var dnc bool
		err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contacts WHERE agent_id=? AND username=? AND do_not_contact=1)`, g.opts.Template.ID, intent.TargetUsername).Scan(&dnc)
		if err != nil {
			return "", err
		}
		if dnc {
			return "do_not_contact", nil
		}
	}
	if kind == "post" && posts >= l.PostsPerDay {
		return "posts_per_day", nil
	}
	if kind == "reply" && replies >= l.RepliesPerDay {
		return "replies_per_day", nil
	}
	if kind == "reply" && contact >= l.RepliesPerUser7d {
		return "replies_per_user_7d", nil
	}
	if kind == "reply" && threadUsed {
		return "thread_per_day", nil
	}
	if !latest.IsZero() && now.Sub(latest) < time.Duration(l.MinSecondsBetweenActions)*time.Second {
		return "min_seconds_between_actions", nil
	}
	return "", nil
}

// ObserveStopRequest is harness ingestion, never a model tool. Inbound text is
// transient data; only a normalized identifier and fixed source code persist.
func (g *Guard) ObserveStopRequest(ctx context.Context, username, text string, addressedToAgent bool) (bool, error) {
	if !addressedToAgent {
		return false, nil
	}
	for _, phrase := range g.stopPhrases {
		if strings.Contains(strings.ToLower(text), strings.ToLower(phrase)) {
			if err := g.opts.Store.MarkDoNotContact(ctx, g.opts.Template.ID, username, "stop_request"); err != nil {
				g.event(ctx, "guard.checked", "error", "stop_request_persist_failed", "")
				return false, &auditError{err}
			}
			g.event(ctx, "guard.checked", "blocked", "stop_request", "")
			return true, nil
		}
	}
	return false, nil
}
