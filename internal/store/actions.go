package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// PublishedAction is a confirmed live own post or reply that people can reply to.
type PublishedAction struct {
	MediaID, Text string
	PostedAt      time.Time
	// Root is the conversation root of a reply of ours; empty for a post or a reply stored without one.
	Root string
}

// PublishedSince returns the confirmed live own posts and replies of the agent
// posted since the given time, newest first.
func (s *Store) PublishedSince(ctx context.Context, agentID string, since time.Time, limit int) ([]PublishedAction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT media_id,text,posted_at,CASE WHEN kind='reply' THEN root_url ELSE '' END FROM actions WHERE agent_id=? AND dry_run=0 AND failed=0 AND thread_url<>'' AND media_id<>'' AND julianday(posted_at)>=julianday(?) ORDER BY substr(posted_at,1,19) DESC,id DESC LIMIT ?`, agentID, since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("reading published actions: %w", err)
	}
	defer rows.Close()
	var out []PublishedAction
	for rows.Next() {
		var p PublishedAction
		var at string
		if err = rows.Scan(&p.MediaID, &p.Text, &at, &p.Root); err != nil {
			return nil, fmt.Errorf("reading published actions: %w", err)
		}
		if p.PostedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("reading published actions: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ConfirmedAction is a published own action as the metric reads address it.
type ConfirmedAction struct{ MediaID, Kind, TargetUsername string }

// ConfirmedAction returns the media id and reply target of a published own post
// or reply; ok is false when the action is missing, foreign, dry-run,
// unconfirmed or has no media id.
func (s *Store) ConfirmedAction(ctx context.Context, agentID string, actionID int64) (c ConfirmedAction, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT media_id,kind,target_username FROM actions WHERE id=? AND agent_id=? AND dry_run=0 AND kind IN ('post','reply') AND thread_url<>'' AND media_id<>''`, actionID, agentID).Scan(&c.MediaID, &c.Kind, &c.TargetUsername)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, fmt.Errorf("reading confirmed action: %w", err)
	}
	return c, true, nil
}

// ConfirmPublished records the acknowledged URL and media id of a reserved live
// action. It reports false unless exactly one unconfirmed live action of the
// agent matched.
func (s *Store) ConfirmPublished(ctx context.Context, agentID string, actionID int64, threadURL, mediaID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE actions SET thread_url=?,media_id=? WHERE id=? AND agent_id=? AND dry_run=0 AND thread_url=''`, threadURL, mediaID, actionID, agentID)
	if err != nil {
		return false, fmt.Errorf("confirming published action: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("confirming published action: %w", err)
	}
	return n == 1, nil
}

// HookTypes are the hook types an action may name, the one list the guard, the
// post-mortem and the template's seed hypotheses are checked against.
var HookTypes = []string{"number", "contrarian", "curiosity_gap", "personal_stake", "trend_detail"}

// MaxSeriesParts is the most parts a series post has, the hook included.
const MaxSeriesParts = 5

// ConfirmPart records the permalink and media id of a published later part of
// a series. It reports false when the part is unknown or already confirmed.
func (s *Store) ConfirmPart(ctx context.Context, agentID string, actionID int64, part int, threadURL, mediaID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE action_parts SET thread_url=?,media_id=? WHERE action_id=? AND part=? AND thread_url='' AND EXISTS(SELECT 1 FROM actions WHERE id=? AND agent_id=? AND dry_run=0)`, threadURL, mediaID, actionID, part, actionID, agentID)
	if err != nil {
		return false, fmt.Errorf("confirming published part: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("confirming published part: %w", err)
	}
	return n == 1, nil
}

// MetricsWakeReady reports whether the agent is running with no open wake.
func (s *Store) MetricsWakeReady(ctx context.Context, agentID string) (bool, error) {
	var ready bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND status='running') AND NOT EXISTS(SELECT 1 FROM wakes WHERE agent_id=? AND ended_at IS NULL)`, agentID, agentID).Scan(&ready)
	if err != nil {
		return false, fmt.Errorf("checking metrics wake readiness: %w", err)
	}
	return ready, nil
}

// CanonicalThread normalizes a Threads post URL to one comparable form: https,
// no www, no query, fragment or trailing slash.
func CanonicalThread(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", errors.New("invalid thread URL")
	}
	u.Scheme = "https"
	u.Host = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// ConversationCap is the template's limit on replies in one conversation: Max
// replies (0 is off), lifted when the first reply's newest metrics reach
// HotLikes likes or HotReplies replies. DryRun selects dry-run or live actions.
type ConversationCap struct {
	Max, HotLikes, HotReplies int
	DryRun                    bool
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// conversationReplies is the one rule for a conversation at its cap: it returns
// how many replies the agent made under root and whether they are capped. Rows
// stored without a root are not counted.
func conversationReplies(ctx context.Context, q rowQuerier, agentID, root string, c ConversationCap) (n int, capped bool, err error) {
	if c.Max <= 0 || root == "" {
		return 0, false, nil
	}
	var likes, replies sql.NullInt64
	err = q.QueryRowContext(ctx, `WITH r AS (SELECT id FROM actions WHERE agent_id=? AND kind='reply' AND failed=0 AND dry_run=? AND root_url=?)
		SELECT (SELECT COUNT(*) FROM r),
		(SELECT likes FROM metrics WHERE action_id=(SELECT MIN(id) FROM r) ORDER BY julianday(measured_at) DESC LIMIT 1),
		(SELECT replies FROM metrics WHERE action_id=(SELECT MIN(id) FROM r) ORDER BY julianday(measured_at) DESC LIMIT 1)`, agentID, c.DryRun, root).Scan(&n, &likes, &replies)
	if err != nil {
		return 0, false, fmt.Errorf("reading conversation replies: %w", err)
	}
	hot := (likes.Valid && likes.Int64 >= int64(c.HotLikes)) || (replies.Valid && replies.Int64 >= int64(c.HotReplies))
	return n, n >= c.Max && !hot, nil
}

// ConversationReplies reports the agent's replies under the conversation root and whether it is at the cap.
func (s *Store) ConversationReplies(ctx context.Context, agentID, root string, c ConversationCap) (int, bool, error) {
	return conversationReplies(ctx, s.db, agentID, root, c)
}

// ConversationReplies is the guard's view of the same rule inside its transaction.
func (t ReservationTx) ConversationReplies(ctx context.Context, agentID, root string, c ConversationCap) (int, bool, error) {
	return conversationReplies(ctx, t.conn, agentID, root, c)
}
