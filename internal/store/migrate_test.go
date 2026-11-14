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

// buildSchema opens a database holding a historical schema from testdata.
func buildSchema(t *testing.T, file string, version int) *sql.DB {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), file+".db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(string(raw) + fmt.Sprintf("\nPRAGMA user_version=%d;", version)); err != nil {
		t.Fatalf("building %s: %v", file, err)
	}
	return db
}

func TestMigrate13MatchesFreshSchema(t *testing.T) {
	ctx := context.Background()
	migration, err := os.ReadFile(filepath.Join("..", "..", "deploy", "migrate-13.sql"))
	if err != nil {
		t.Fatal(err)
	}
	old := buildSchema(t, "schema-v12.sql", 12)
	defer old.Close()
	if _, err = old.ExecContext(ctx, `
		INSERT INTO agents(id,status) VALUES('a','running');
		INSERT INTO actions(agent_id,kind,text,posted_at) VALUES('a','post','hello','2026-11-01T00:00:00Z');`); err != nil {
		t.Fatalf("seeding v12: %v", err)
	}
	if _, err = old.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("applying migrate-13.sql: %v", err)
	}
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "v13.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if want, got := readShape(t, fresh.db), readShape(t, old); !reflect.DeepEqual(want, got) {
		t.Fatalf("migrated schema differs from a fresh v13 schema\nfresh:    %+v\nmigrated: %+v", want, got)
	}
	var version int
	if err = old.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}
	// Existing rows stay and the new table takes rows; a URL keeps one id per agent.
	var actions int
	if err = old.QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions); err != nil || actions != 1 {
		t.Errorf("actions after migration = %d, %v; want 1", actions, err)
	}
	insert := `INSERT INTO seen_posts(agent_id,url,author,text,read_at) VALUES('a','https://threads.com/@x/post/1','x','t','2026-11-01T00:00:00Z')`
	if _, err = old.Exec(insert); err != nil {
		t.Fatalf("writing seen_posts after migration: %v", err)
	}
	if _, err = old.Exec(insert); err == nil {
		t.Error("seen_posts accepts the same URL twice for one agent")
	}
}

func TestMigrate13RefusesOtherVersions(t *testing.T) {
	migration, err := os.ReadFile(filepath.Join("..", "..", "deploy", "migrate-13.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA user_version=13`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err == nil {
		t.Fatal("migration ran on a v13 database")
	}
}
