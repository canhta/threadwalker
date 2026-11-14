package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxLessons is how many lessons may be active at once.
	MaxLessons = 10
	// MaxLessonRunes bounds a lesson's text.
	MaxLessonRunes = 240
	// MinCitations and MaxCitations bound the actions a new lesson cites.
	MinCitations, MaxCitations = 2, 5
	// MaxLessonIDs is how many lessons one post or reply may name.
	MaxLessonIDs = 3
	// LessonConfirmDays and LessonUseDays are the expiry rule: a lesson not
	// confirmed for the first and not applied for the second is retired.
	LessonConfirmDays, LessonUseDays = 14, 7
	// citationWindow is the first window a cited action may be ranked at.
	citationWindow = 6
)

// Lesson retirers.
const (
	RetiredByDream  = "dream"
	RetiredByOwner  = "owner"
	RetiredByExpiry = "expiry"
)

// Lesson is a short, evidence-backed note the dream keeps. RetiredAt is zero
// while the lesson is active.
type Lesson struct {
	ID                                int64
	AgentID, Kind, Polarity, Text     string
	Evidence                          []int64
	CreatedAt, ConfirmedAt, RetiredAt time.Time
	RetiredBy, RetireReason           string
}

func (l Lesson) Active() bool { return l.RetiredAt.IsZero() }

// LessonRefusal is a lesson rule that refused a write. Reason is the guard
// code the model's hint is looked up by; Detail is the one fact the hint names.
type LessonRefusal struct{ Reason, Detail string }

func (e *LessonRefusal) Error() string { return "lesson refused: " + e.Reason }

func refuse(reason string, detail ...any) error {
	return &LessonRefusal{reason, strings.TrimSpace(fmt.Sprint(detail...))}
}

const lessonColumns = `id,agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at,retired_at,retired_by,retire_reason`

type lessonQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryLessons(ctx context.Context, q lessonQuerier, where string, args ...any) ([]Lesson, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+lessonColumns+` FROM lessons WHERE `+where+` ORDER BY retired_at IS NOT NULL,id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading lessons: %w", err)
	}
	defer rows.Close()
	var out []Lesson
	for rows.Next() {
		var l Lesson
		var evidence, created, confirmed string
		var retired, by sql.NullString
		if err = rows.Scan(&l.ID, &l.AgentID, &l.Kind, &l.Polarity, &l.Text, &evidence, &created, &confirmed, &retired, &by, &l.RetireReason); err != nil {
			return nil, fmt.Errorf("reading lesson: %w", err)
		}
		if err = json.Unmarshal([]byte(evidence), &l.Evidence); err != nil {
			return nil, fmt.Errorf("reading lesson %d evidence: %w", l.ID, err)
		}
		if l.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("reading lesson %d: %w", l.ID, err)
		}
		if l.ConfirmedAt, err = parseTime(confirmed); err != nil {
			return nil, fmt.Errorf("reading lesson %d: %w", l.ID, err)
		}
		if retired.Valid {
			if l.RetiredAt, err = parseTime(retired.String); err != nil {
				return nil, fmt.Errorf("reading lesson %d: %w", l.ID, err)
			}
			l.RetiredBy = by.String
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Lessons lists the agent's lessons, active first, newest first within each
// group; retired ones only when asked.
func (s *Store) Lessons(ctx context.Context, agentID string, withRetired bool) ([]Lesson, error) {
	where := `agent_id=?`
	if !withRetired {
		where += ` AND retired_at IS NULL`
	}
	return queryLessons(ctx, s.db, where, agentID)
}

// normalizeLesson makes two spellings of one sentence equal: lowercase,
// punctuation as space, runs of space collapsed.
func normalizeLesson(text string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, text)), " ")
}

// citations checks that every id is one of the agent's own episodes ranked at
// the 6h or 24h window, of the lesson's kind, and in the band its polarity
// needs: top for do, bottom for avoid.
func (s *Store) citations(ctx context.Context, agentID, kind, polarity string, ids []int64) error {
	eps, err := s.Episodes(ctx, agentID, EpisodeQuery{})
	if err != nil {
		return err
	}
	byID := make(map[int64]Episode, len(eps))
	for _, e := range eps {
		byID[e.ActionID] = e
	}
	want := BandTop
	if polarity == "avoid" {
		want = BandBottom
	}
	for _, id := range ids {
		e, ok := byID[id]
		switch {
		case !ok:
			return refuse("lesson_foreign_citation", id)
		case e.Window < citationWindow || e.Band == BandUnranked:
			return refuse("lesson_unranked", id)
		case kind != "any" && e.Kind != kind:
			return refuse("lesson_kind_mismatch", kind)
		case e.Band != want:
			return refuse("lesson_band", id)
		}
	}
	return nil
}

// CheckCitations is the citation rule of lesson_add: MinCitations to
// MaxCitations different ids, each a qualifying episode.
func (s *Store) CheckCitations(ctx context.Context, agentID, kind, polarity string, ids []int64) error {
	if n := len(ids); n < MinCitations || n > MaxCitations || len(slices.Compact(slices.Sorted(slices.Values(ids)))) != n {
		return refuse("lesson_citations")
	}
	return s.citations(ctx, agentID, kind, polarity, ids)
}

// AddLesson stores a lesson after every rule has passed: citations, text
// length, the cap on active lessons, and no repeat of an active lesson or of
// one the owner removed. Failures are *LessonRefusal.
func (s *Store) AddLesson(ctx context.Context, agentID, kind, polarity, text string, ids []int64, now time.Time) (int64, error) {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > MaxLessonRunes {
		return 0, refuse("lesson_too_long", utf8.RuneCountInString(text))
	}
	if err := s.CheckCitations(ctx, agentID, kind, polarity, ids); err != nil {
		return 0, err
	}
	evidence, _ := json.Marshal(ids)
	var id int64
	err := s.write(ctx, func(conn *sql.Conn) error {
		all, err := queryLessons(ctx, conn, `agent_id=? AND (retired_at IS NULL OR retired_by='owner')`, agentID)
		if err != nil {
			return err
		}
		active, norm := 0, normalizeLesson(text)
		for _, l := range all {
			if l.Active() {
				active++
			}
			if normalizeLesson(l.Text) == norm {
				if l.Active() {
					return refuse("lesson_duplicate", l.ID)
				}
				return refuse("lesson_forgotten")
			}
		}
		if active >= MaxLessons {
			return refuse("lessons_full", MaxLessons)
		}
		stamp := formatStoreTimestamp(now)
		res, err := conn.ExecContext(ctx, `INSERT INTO lessons(agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at) VALUES(?,?,?,?,?,?,?)`, agentID, kind, polarity, text, string(evidence), stamp, stamp)
		if err != nil {
			return fmt.Errorf("adding lesson: %w", err)
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// ConfirmLesson adds actions that agree with an active lesson to its evidence
// and renews it. At least one must be new; each must qualify as for AddLesson.
func (s *Store) ConfirmLesson(ctx context.Context, agentID string, lessonID int64, ids []int64, now time.Time) error {
	lessons, err := queryLessons(ctx, s.db, `agent_id=? AND id=? AND retired_at IS NULL`, agentID, lessonID)
	if err != nil {
		return err
	}
	if len(lessons) == 0 {
		return refuse("lesson_unknown", lessonID)
	}
	if n := len(ids); n < 1 || n > MaxCitations {
		return refuse("lesson_citations")
	}
	l := lessons[0]
	if err = s.citations(ctx, agentID, l.Kind, l.Polarity, ids); err != nil {
		return err
	}
	return s.write(ctx, func(conn *sql.Conn) error {
		cur, err := queryLessons(ctx, conn, `agent_id=? AND id=? AND retired_at IS NULL`, agentID, lessonID)
		if err != nil {
			return err
		}
		if len(cur) == 0 {
			return refuse("lesson_unknown", lessonID)
		}
		evidence := cur[0].Evidence
		before := len(evidence)
		for _, id := range ids {
			if !slices.Contains(evidence, id) {
				evidence = append(evidence, id)
			}
		}
		if len(evidence) == before {
			return refuse("lesson_no_new_evidence")
		}
		encoded, _ := json.Marshal(evidence)
		if _, err = conn.ExecContext(ctx, `UPDATE lessons SET evidence_json=?,confirmed_at=? WHERE id=?`, string(encoded), formatStoreTimestamp(now), lessonID); err != nil {
			return fmt.Errorf("confirming lesson: %w", err)
		}
		return nil
	})
}

// RetireLesson retires an active lesson; by is RetiredByDream, RetiredByOwner
// or RetiredByExpiry. An owner retirement is the tombstone that keeps the
// same sentence from being added again.
func (s *Store) RetireLesson(ctx context.Context, agentID string, lessonID int64, by, reason string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE lessons SET retired_at=?,retired_by=?,retire_reason=? WHERE agent_id=? AND id=? AND retired_at IS NULL`, formatStoreTimestamp(now), by, CapRunes(strings.TrimSpace(reason), MaxLessonRunes), agentID, lessonID)
	if err != nil {
		return fmt.Errorf("retiring lesson: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return refuse("lesson_unknown", lessonID)
	}
	return nil
}

// ForgetLesson is the owner removing a lesson for good.
func (s *Store) ForgetLesson(ctx context.Context, agentID string, lessonID int64, now time.Time) error {
	return s.RetireLesson(ctx, agentID, lessonID, RetiredByOwner, "forgotten by the owner", now)
}

// ExpireLessons retires every active lesson not confirmed for LessonConfirmDays
// and not applied by an action posted in the last LessonUseDays. It returns how
// many it retired.
func (s *Store) ExpireLessons(ctx context.Context, agentID string, now time.Time) (int, error) {
	day := 24 * time.Hour
	res, err := s.db.ExecContext(ctx, `UPDATE lessons SET retired_at=?,retired_by=?,retire_reason=? WHERE agent_id=? AND retired_at IS NULL AND julianday(confirmed_at)<=julianday(?)
		AND NOT EXISTS(SELECT 1 FROM action_lessons al JOIN actions a ON a.id=al.action_id WHERE al.lesson_id=lessons.id AND julianday(a.posted_at)>julianday(?))`,
		formatStoreTimestamp(now), RetiredByExpiry, "not confirmed for "+strconv.Itoa(LessonConfirmDays)+" days and not applied for "+strconv.Itoa(LessonUseDays), agentID,
		formatStoreTimestamp(now.Add(-LessonConfirmDays*day)), formatStoreTimestamp(now.Add(-LessonUseDays*day)))
	if err != nil {
		return 0, fmt.Errorf("expiring lessons: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// LessonUse is how a lesson's applied actions ranked against the agent's other
// actions of its kind since the lesson was created. AppliedN counts every
// applied action; the medians use only the ranked ones, counted by AppliedRanked
// and OthersN. A median is zero when its count is.
type LessonUse struct {
	AppliedN, AppliedRanked, OthersN int
	AppliedMedian, OthersMedian      float64
}

// LessonUses computes the use stats of the given lessons, keyed by lesson id.
func (s *Store) LessonUses(ctx context.Context, agentID string, lessons []Lesson) (map[int64]LessonUse, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT al.lesson_id,al.action_id FROM action_lessons al JOIN lessons l ON l.id=al.lesson_id JOIN actions a ON a.id=al.action_id WHERE l.agent_id=? AND a.failed=0`, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading lesson use: %w", err)
	}
	applied := map[int64]map[int64]bool{}
	for rows.Next() {
		var lesson, action int64
		if err = rows.Scan(&lesson, &action); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reading lesson use: %w", err)
		}
		if applied[lesson] == nil {
			applied[lesson] = map[int64]bool{}
		}
		applied[lesson][action] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("reading lesson use: %w", err)
	}
	eps, err := s.Episodes(ctx, agentID, EpisodeQuery{})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]LessonUse, len(lessons))
	for _, l := range lessons {
		u := LessonUse{AppliedN: len(applied[l.ID])}
		var with, without []float64
		for _, e := range eps {
			if e.Band == BandUnranked || (l.Kind != "any" && e.Kind != l.Kind) {
				continue
			}
			if applied[l.ID][e.ActionID] {
				with = append(with, e.Rank)
			} else if !e.PostedAt.Before(l.CreatedAt) {
				without = append(without, e.Rank)
			}
		}
		u.AppliedRanked, u.AppliedMedian = len(with), median(with)
		u.OthersN, u.OthersMedian = len(without), median(without)
		out[l.ID] = u
	}
	return out, nil
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	slices.Sort(xs)
	if n := len(xs); n%2 == 0 {
		return (xs[n/2-1] + xs[n/2]) / 2
	}
	return xs[len(xs)/2]
}

// LessonsFit reports whether every id is an active lesson of the agent that
// applies to an action of the kind, inside the reservation transaction.
func (t ReservationTx) LessonsFit(ctx context.Context, agentID, kind string, ids []int64) (bool, error) {
	if len(ids) == 0 {
		return true, nil
	}
	args := []any{agentID, kind}
	for _, id := range ids {
		args = append(args, id)
	}
	var n int
	err := t.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM lessons WHERE agent_id=? AND retired_at IS NULL AND kind IN (?,'any') AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("checking lesson ids: %w", err)
	}
	return n == len(ids), nil
}

// DistinctLessonIDs reports whether ids has at most MaxLessonIDs different
// positive ids.
func DistinctLessonIDs(ids []int64) bool {
	return len(ids) <= MaxLessonIDs && len(slices.Compact(slices.Sorted(slices.Values(ids)))) == len(ids) && !slices.ContainsFunc(ids, func(id int64) bool { return id < 1 })
}
