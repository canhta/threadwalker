package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// retentionPolicy is the one place a table's lifetime is stated. A row older
// than maxAge on ageCol is deleted; ageCol NULL (a wake still running, a job
// not completed) is never deleted. maxRows, when set, also caps each agent's
// newest rows.
type retentionPolicy struct {
	table   string
	ageCol  string
	maxAge  time.Duration
	maxRows int
}

const (
	day = 24 * time.Hour

	// replyWindow is the guard's per-contact reply-count window.
	replyWindow = 7 * day

	// threadsObservationRetention is the trend brief's baseline window plus a day.
	// Wake transcripts and tool call details hold the same outside text, so they share it.
	threadsObservationRetention = 8 * day
)

// retention lists every pruned table. actions, metrics, ledger, ledger_phases,
// notes and the playbook tables are the experiment's record and are not here;
// contacts has its own rule in Prune.
var retention = []retentionPolicy{
	{table: "trend_stories", ageCol: "observed_at", maxAge: 7 * day, maxRows: 500},
	{table: "threads_observations", ageCol: "observed_at", maxAge: threadsObservationRetention},
	{table: "wake_transcripts", ageCol: "at", maxAge: threadsObservationRetention},
	{table: "tool_call_details", ageCol: "at", maxAge: threadsObservationRetention},
	{table: "tool_calls", ageCol: "at", maxAge: 90 * day},
	{table: "wakes", ageCol: "ended_at", maxAge: 90 * day},
	{table: "owner_msgs", ageCol: "at", maxAge: 90 * day},
	{table: "metric_jobs", ageCol: "completed_at", maxAge: 30 * day},
	{table: "webhook_events", ageCol: "at", maxAge: 30 * day},
}

// RetentionAge reports how long table keeps rows. ok is false for a table
// that is never pruned by age.
func RetentionAge(table string) (age time.Duration, ok bool) {
	for _, p := range retention {
		if p.table == table {
			return p.maxAge, true
		}
	}
	return 0, false
}

// Pruned is the number of rows Prune deleted from one table.
type Pruned struct {
	Table   string
	Deleted int64
}

const contactsTable = "contacts"

// Prune applies the retention table to every agent in one transaction, then
// returns freed pages to the file. It is idempotent.
func (s *Store) Prune(ctx context.Context, now time.Time) ([]Pruned, error) {
	var out []Pruned
	err := s.write(ctx, func(conn *sql.Conn) error {
		out = out[:0]
		for _, p := range retention {
			deleted, err := pruneTable(ctx, conn, p, now)
			if err != nil {
				return fmt.Errorf("pruning %s: %w", p.table, err)
			}
			out = append(out, Pruned{Table: p.table, Deleted: deleted})
		}
		// A contact is bookkeeping for the reply limit: once no reply to it
		// falls in the guard's window it carries no information. Usernames are
		// normalised the way NormalizeUsername does.
		res, err := conn.ExecContext(ctx, `DELETE FROM contacts WHERE do_not_contact=0 AND NOT EXISTS (
			SELECT 1 FROM actions a WHERE a.agent_id=contacts.agent_id AND a.kind='reply'
			AND lower(ltrim(trim(a.target_username),'@'))=contacts.username
			AND julianday(a.posted_at)>=julianday(?) AND julianday(a.posted_at)<=julianday(?))`,
			formatTime(now.Add(-replyWindow)), formatTime(now))
		if err != nil {
			return fmt.Errorf("pruning %s: %w", contactsTable, err)
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("pruning %s: %w", contactsTable, err)
		}
		out = append(out, Pruned{Table: contactsTable, Deleted: deleted})
		return nil
	})
	if err != nil {
		return nil, err
	}
	var free int
	if err = s.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil {
		return nil, fmt.Errorf("counting freed pages: %w", err)
	}
	if free > 0 {
		if _, err = s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
			return nil, fmt.Errorf("returning freed pages: %w", err)
		}
	}
	return out, nil
}

func pruneTable(ctx context.Context, conn *sql.Conn, p retentionPolicy, now time.Time) (int64, error) {
	total := int64(0)
	res, err := conn.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE julianday(%s)<julianday(?)`, p.table, p.ageCol), formatTime(now.Add(-p.maxAge)))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	total += n
	if p.maxRows > 0 {
		res, err = conn.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %[1]s WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY agent_id ORDER BY substr(%[2]s,1,19) DESC, id DESC) AS n FROM %[1]s) WHERE n>?)`, p.table, p.ageCol), p.maxRows)
		if err != nil {
			return 0, err
		}
		if n, err = res.RowsAffected(); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// OverdueRetention counts, per table with an age limit, the rows older than
// that limit plus the day the scheduler allows between prunes.
func (s *Store) OverdueRetention(ctx context.Context, now time.Time) ([]Pruned, error) {
	var out []Pruned
	for _, p := range retention {
		var n int64
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE julianday(%s)<julianday(?)`, p.table, p.ageCol)
		if err := s.db.QueryRowContext(ctx, q, formatTime(now.Add(-p.maxAge-day))).Scan(&n); err != nil {
			return nil, fmt.Errorf("counting overdue %s: %w", p.table, err)
		}
		out = append(out, Pruned{Table: p.table, Deleted: n})
	}
	return out, nil
}
