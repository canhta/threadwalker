package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MaxSeenText bounds the text kept of a post: a Threads post is at most 500
// characters, so this keeps all of it.
const MaxSeenText = 500

// SeenPost is a post a read returned, kept under a durable per-agent id (the
// p<id> the model passes to reply). Likes, Replies and AgeMinutes are nil when
// the read did not show them; AgeMinutes is the age at ReadAt.
type SeenPost struct {
	ID         int64
	URL        string // canonical thread URL
	MediaID    string // API post id; empty for a post only a browser read showed
	Author     string
	Text       string
	Likes      *int64
	Replies    *int64
	AgeMinutes *int64
	ReadAt     time.Time
}

// NoteSeenPost records a post read and returns its id; the same URL keeps its
// id. A later read fills a missing media id and refreshes the figures: an age
// with its read time, likes and replies when shown. A read that shows no age
// leaves the stored age and read time alone, so the age stays true.
func (s *Store) NoteSeenPost(ctx context.Context, agentID string, p SeenPost) (int64, error) {
	url, err := CanonicalThread(p.URL)
	if err != nil {
		return 0, err
	}
	text := CapRunes(p.Text, MaxSeenText)
	var id int64
	err = s.db.QueryRowContext(ctx, `INSERT INTO seen_posts(agent_id,url,media_id,author,text,likes,replies,age_minutes,read_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(agent_id,url) DO UPDATE SET
			media_id=CASE WHEN excluded.media_id<>'' THEN excluded.media_id ELSE media_id END,
			author=excluded.author,
			text=CASE WHEN excluded.text<>'' THEN excluded.text ELSE text END,
			likes=COALESCE(excluded.likes,likes),
			replies=COALESCE(excluded.replies,replies),
			age_minutes=COALESCE(excluded.age_minutes,age_minutes),
			read_at=CASE WHEN excluded.age_minutes IS NOT NULL THEN excluded.read_at ELSE read_at END
		RETURNING id`, agentID, url, p.MediaID, p.Author, text, p.Likes, p.Replies, p.AgeMinutes, formatStoreTimestamp(p.ReadAt)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("recording seen post: %w", err)
	}
	return id, nil
}

const seenPostColumns = `id,url,media_id,author,text,likes,replies,age_minutes,read_at`

type scanner interface{ Scan(dest ...any) error }

func scanSeenPost(r scanner) (SeenPost, error) {
	var p SeenPost
	var likes, replies, age sql.NullInt64
	var at string
	if err := r.Scan(&p.ID, &p.URL, &p.MediaID, &p.Author, &p.Text, &likes, &replies, &age, &at); err != nil {
		return SeenPost{}, err
	}
	for _, f := range []struct {
		n   sql.NullInt64
		dst **int64
	}{{likes, &p.Likes}, {replies, &p.Replies}, {age, &p.AgeMinutes}} {
		if f.n.Valid {
			v := f.n.Int64
			*f.dst = &v
		}
	}
	var err error
	if p.ReadAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
		return SeenPost{}, fmt.Errorf("reading seen post time: %w", err)
	}
	return p, nil
}

// SeenPostByID returns this agent's post with the durable id; ok is false for
// an unknown, pruned or foreign id.
func (s *Store) SeenPostByID(ctx context.Context, agentID string, id int64) (SeenPost, bool, error) {
	p, err := scanSeenPost(s.db.QueryRowContext(ctx, `SELECT `+seenPostColumns+` FROM seen_posts WHERE agent_id=? AND id=?`, agentID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SeenPost{}, false, nil
	}
	if err != nil {
		return SeenPost{}, false, fmt.Errorf("reading seen post: %w", err)
	}
	return p, true, nil
}

// SeenPostsSince lists this agent's posts whose figures were read at or after since, newest first.
func (s *Store) SeenPostsSince(ctx context.Context, agentID string, since time.Time) ([]SeenPost, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+seenPostColumns+` FROM seen_posts WHERE agent_id=? AND julianday(read_at)>=julianday(?) ORDER BY julianday(read_at) DESC,id DESC`, agentID, formatStoreTimestamp(since))
	if err != nil {
		return nil, fmt.Errorf("reading seen posts: %w", err)
	}
	defer rows.Close()
	var out []SeenPost
	for rows.Next() {
		p, err := scanSeenPost(rows)
		if err != nil {
			return nil, fmt.Errorf("reading seen post: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
