package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// shape is the structure of a database: every table with its columns, every
// index and every trigger. CREATE text is compared only for indexes and
// triggers, which the migration creates verbatim; tables are compared by
// pragma, because ALTER TABLE rewrites their text.
type shape struct {
	Tables   map[string][]string
	Indexes  map[string]string
	Triggers map[string]string
}

var spaces = regexp.MustCompile(`\s+`)

func readShape(t *testing.T, db *sql.DB) shape {
	t.Helper()
	s := shape{Tables: map[string][]string{}, Indexes: map[string]string{}, Triggers: map[string]string{}}
	rows, err := db.Query(`SELECT type,name,COALESCE(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	type object struct{ kind, name, sql string }
	var objects []object
	for rows.Next() {
		var o object
		if err = rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, o)
	}
	rows.Close()
	for _, o := range objects {
		text := spaces.ReplaceAllString(strings.TrimSpace(o.sql), " ")
		switch o.kind {
		case "table":
			cols, err := db.Query(fmt.Sprintf(`SELECT cid,name,type,"notnull",COALESCE(dflt_value,''),pk FROM pragma_table_info('%s')`, o.name))
			if err != nil {
				t.Fatal(err)
			}
			for cols.Next() {
				var cid, notNull, pk int
				var name, typ, dflt string
				if err = cols.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
					t.Fatal(err)
				}
				s.Tables[o.name] = append(s.Tables[o.name], fmt.Sprintf("%d %s %s notnull=%d default=%q pk=%d", cid, name, typ, notNull, dflt, pk))
			}
			cols.Close()
		case "index":
			s.Indexes[o.name] = text
		case "trigger":
			s.Triggers[o.name] = text
		}
	}
	return s
}

func TestMigrate12MatchesFreshSchema(t *testing.T) {
	ctx := context.Background()
	v11, err := os.ReadFile(filepath.Join("testdata", "schema-v11.sql"))
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "deploy", "migrate-12.sql"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v11.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1) // the script holds one transaction across statements
	if _, err = old.ExecContext(ctx, string(v11)+"\nPRAGMA user_version=11;"); err != nil {
		t.Fatalf("building v11: %v", err)
	}
	if _, err = old.ExecContext(ctx, `
		INSERT INTO agents(id,status) VALUES('a','running');
		INSERT INTO actions(agent_id,kind,text,posted_at,hypothesis_ids_json,explore) VALUES('a','post','hello','2026-11-01T00:00:00Z','[1]',1);
		INSERT INTO notes(agent_id,text,created_at) VALUES('a','a note','2026-11-01T00:00:00Z');
		INSERT INTO wakes(id,agent_id,started_at,kind) VALUES(1,'a','2026-11-01T00:00:00Z','agent'),(2,'a','2026-11-01T01:00:00Z','study'),(3,'a','2026-11-01T02:00:00Z','dream');
		INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,at) VALUES('a',1,'post','{}','2026-11-01T00:00:00Z'),('a',2,'note_write','{}','2026-11-01T01:00:00Z');
		INSERT INTO wake_transcripts(agent_id,wake_id,seq,kind,body,at) VALUES('a',2,1,'start','{}','2026-11-01T01:00:00Z');
		INSERT INTO ledger_phases(agent_id,day,phase,tokens_in,tokens_out,cost_usd) VALUES('a','2026-11-01','agent',10,1,0.5),('a','2026-11-01','study',20,2,0.25),('a','2026-11-02','study',5,1,0.1);
		INSERT INTO trend_stories(agent_id,observed_at,phase,source,title,url) VALUES('a','2026-11-01T00:00:00Z','agent','s','kept','u'),('a','2026-11-01T00:00:00Z','study','s','gone','u');`); err != nil {
		t.Fatalf("seeding v11: %v", err)
	}
	if _, err = old.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("applying migrate-12.sql: %v", err)
	}

	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "v12.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	want, got := readShape(t, fresh.db), readShape(t, old)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("migrated schema differs from a fresh v12 schema\nfresh:    %+v\nmigrated: %+v", want, got)
	}
	var version int
	if err = old.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}

	// The CHECKs that listed study no longer accept it, in either database.
	for name, db := range map[string]*sql.DB{"migrated": old, "fresh": fresh.db} {
		if _, err = db.Exec(`INSERT INTO wakes(agent_id,started_at,kind) VALUES('a','2026-11-03T00:00:00Z','study')`); err == nil {
			t.Errorf("%s: wakes accepts kind study", name)
		}
		if _, err = db.Exec(`INSERT INTO ledger_phases(agent_id,day,phase) VALUES('a','2026-11-03','study')`); err == nil {
			t.Errorf("%s: ledger_phases accepts phase study", name)
		}
	}

	// Study wakes and their audit rows are deleted, its spend is kept under agent.
	var wakes, calls, transcripts, stories int
	if err = old.QueryRow(`SELECT (SELECT COUNT(*) FROM wakes),(SELECT COUNT(*) FROM tool_calls),(SELECT COUNT(*) FROM wake_transcripts),(SELECT COUNT(*) FROM trend_stories)`).Scan(&wakes, &calls, &transcripts, &stories); err != nil {
		t.Fatal(err)
	}
	if wakes != 2 || calls != 1 || transcripts != 0 || stories != 1 {
		t.Errorf("after migration wakes=%d tool_calls=%d transcripts=%d stories=%d, want 2 1 0 1", wakes, calls, transcripts, stories)
	}
	var tokensIn int64
	var cost float64
	if err = old.QueryRow(`SELECT SUM(tokens_in),SUM(cost_usd) FROM ledger_phases WHERE phase='agent'`).Scan(&tokensIn, &cost); err != nil {
		t.Fatal(err)
	}
	if tokensIn != 35 || cost < 0.849 || cost > 0.851 {
		t.Errorf("agent ledger after migration = %d tokens, $%.2f; want 35, $0.85", tokensIn, cost)
	}
	if err = old.QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&wakes); err != nil || wakes != 1 {
		t.Errorf("actions kept = %d, %v; want 1", wakes, err)
	}

	// Existing actions get an empty brief, and the lesson tables take rows.
	var angle, fact string
	if err = old.QueryRow(`SELECT angle,fact FROM actions`).Scan(&angle, &fact); err != nil || angle != "" || fact != "" {
		t.Errorf("migrated action brief = %q %q, %v; want empty", angle, fact, err)
	}
	if _, err = old.Exec(`INSERT INTO lessons(agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at) VALUES('a','post','do','t','[1,2]','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z');
		INSERT INTO action_lessons(action_id,lesson_id) VALUES(1,1)`); err != nil {
		t.Errorf("writing lessons after migration: %v", err)
	}
}

func TestMigrate12RefusesOtherVersions(t *testing.T) {
	migration, err := os.ReadFile(filepath.Join("..", "..", "deploy", "migrate-12.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA user_version=12`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err == nil {
		t.Fatal("migration ran on a v12 database")
	}
}
