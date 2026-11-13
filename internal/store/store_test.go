package store_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func TestOpen_CreatesSchemaAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "threadz.db")

	st1, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	schema1, err := dumpSchema(ctx, st1)
	if err != nil {
		t.Fatalf("dumpSchema after first Open: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("closing first store: %v", err)
	}

	wantTables := map[string][]string{
		"agents":        {"id", "status", "next_wake_at"},
		"wakes":         {"id", "agent_id", "started_at", "ended_at", "steps", "tokens_in", "tokens_out", "cost_usd", "end_reason"},
		"tool_calls":    {"id", "agent_id", "wake_id", "tool", "args_json", "result_json", "blocked_by", "at"},
		"actions":       {"id", "agent_id", "kind", "text", "target_url", "target_username", "hypothesis_ids_json", "dry_run", "posted_at", "thread_url"},
		"metrics":       {"agent_id", "action_id", "measured_at", "views", "likes", "replies", "reposts"},
		"notes":         {"id", "agent_id", "text", "created_at", "topic", "description"},
		"notes_fts":     {"agent_id", "text"},
		"playbook":      {"id", "agent_id", "text", "status", "n", "created_at", "decided_at"},
		"contacts":      {"agent_id", "username", "reply_count_7d", "do_not_contact", "reason"},
		"ledger_phases": {"agent_id", "day", "phase", "tokens_in", "tokens_out", "cost_usd"},
		"owner_msgs":    {"id", "agent_id", "direction", "text", "answered_id", "at"},
	}

	for table, wantCols := range wantTables {
		gotCols, ok := schema1.columns[table]
		if !ok {
			t.Errorf("missing table %q in schema", table)
			continue
		}
		colSet := make(map[string]bool, len(gotCols))
		for _, c := range gotCols {
			colSet[c] = true
		}
		for _, wc := range wantCols {
			if !colSet[wc] {
				t.Errorf("table %q missing column %q (got %v)", table, wc, gotCols)
			}
		}
	}

	// Verify FTS5 virtual table works with MATCH query.
	if _, err := st1CheckFTS(ctx, dbPath); err != nil {
		t.Fatalf("FTS5 verification failed: %v", err)
	}

	// Open the same database a second time and verify identical schema.
	st2, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}
	defer st2.Close()

	schema2, err := dumpSchema(ctx, st2)
	if err != nil {
		t.Fatalf("dumpSchema after second Open: %v", err)
	}

	if !reflect.DeepEqual(schema1, schema2) {
		t.Errorf("schema changed after opening twice:\nfirst:  %+v\nsecond: %+v", schema1, schema2)
	}
}

type schemaSnapshot struct {
	ddl     map[string]string
	columns map[string][]string
}

func dumpSchema(ctx context.Context, st *store.Store) (schemaSnapshot, error) {
	rows, err := st.DB().QueryContext(ctx, `SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type IN ('table', 'index', 'trigger') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return schemaSnapshot{}, err
	}
	defer rows.Close()

	snap := schemaSnapshot{
		ddl:     make(map[string]string),
		columns: make(map[string][]string),
	}
	var tables []string
	for rows.Next() {
		var name, sqlStr string
		if err := rows.Scan(&name, &sqlStr); err != nil {
			return schemaSnapshot{}, err
		}
		snap.ddl[name] = sqlStr
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return schemaSnapshot{}, err
	}

	for _, tbl := range tables {
		colRows, err := st.DB().QueryContext(ctx, "PRAGMA table_info("+tbl+")")
		if err != nil {
			return schemaSnapshot{}, err
		}
		for colRows.Next() {
			var cid int
			var name, ctype string
			var notnull int
			var dflt any
			var pk int
			if err := colRows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				colRows.Close()
				return schemaSnapshot{}, err
			}
			snap.columns[tbl] = append(snap.columns[tbl], name)
		}
		colRows.Close()
	}
	return snap, nil
}

func st1CheckFTS(ctx context.Context, dbPath string) (int, error) {
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return 0, err
	}
	defer st.Close()

	_, err = st.DB().ExecContext(ctx, `INSERT INTO notes (id, agent_id, text, created_at) VALUES (?, ?, ?, ?)`, 1, "fixture", "builders in Vietnam love practical posts", "2026-11-11T00:00:00Z")
	if err != nil {
		return 0, err
	}
	var count int
	err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM notes_fts WHERE notes_fts MATCH ?`, "Vietnam").Scan(&count)
	if err != nil {
		return 0, err
	}
	if count != 1 {
		return count, context.DeadlineExceeded
	}
	return count, nil
}
