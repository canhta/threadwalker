package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/store"
)

var indexAt = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

func TestMemoryIndex_LineBudgetArchivesOldest(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	insertNotesTestAgent(t, st, "b")

	if err := st.AddMemoryEntry(ctx, "b", "other agent entry", indexAt, store.MemoryActor{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.MaxIndexLines+3; i++ {
		if err := st.AddMemoryEntry(ctx, "a", fmt.Sprintf("entry %d", i), indexAt.Add(time.Duration(i)*time.Second), store.MemoryActor{}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.MemoryIndex(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != store.MaxIndexLines || got[0].Text != "entry 3" || got[len(got)-1].Text != fmt.Sprintf("entry %d", store.MaxIndexLines+2) {
		t.Fatalf("index = %d entries from %q to %q, want the newest %d", len(got), got[0].Text, got[len(got)-1].Text, store.MaxIndexLines)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM memory_index WHERE agent_id='a' AND archived_at IS NOT NULL`); n != 3 {
		t.Fatalf("archived rows = %d, want the 3 oldest kept, not deleted", n)
	}
	if other, _ := st.MemoryIndex(ctx, "b"); len(other) != 1 {
		t.Fatalf("other agent's index = %v, want untouched", other)
	}
}

func TestMemoryIndex_ByteBudgetArchivesOldest(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")

	entry := strings.Repeat("x", store.MaxEntryRunes-3)
	count := store.MaxIndexBytes/len(entry) + 2
	for i := 0; i < count; i++ {
		if err := st.AddMemoryEntry(ctx, "a", fmt.Sprintf("%s%03d", entry, i), indexAt.Add(time.Duration(i)*time.Second), store.MemoryActor{}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.MemoryIndex(ctx, "a")
	size := 0
	for _, e := range got {
		size += len(e.Text)
	}
	if size > store.MaxIndexBytes || len(got) != store.MaxIndexBytes/len(entry) {
		t.Fatalf("index holds %d entries, %d bytes; want %d entries within %d bytes", len(got), size, store.MaxIndexBytes/len(entry), store.MaxIndexBytes)
	}
}

func TestMemoryIndex_MultiLineEntriesCountLines(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")

	entry := strings.Repeat("l\n", store.MaxEntryLines-1) + "l"
	for i := 0; i < store.MaxIndexLines/store.MaxEntryLines+1; i++ {
		if err := st.AddMemoryEntry(ctx, "a", fmt.Sprintf("%s%d", entry, i), indexAt.Add(time.Duration(i)*time.Second), store.MemoryActor{}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := st.MemoryIndex(ctx, "a"); len(got) != store.MaxIndexLines/store.MaxEntryLines {
		t.Fatalf("index = %d entries, want %d", len(got), store.MaxIndexLines/store.MaxEntryLines)
	}
}

func TestMemoryIndex_RejectsBadEntries(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")

	for name, text := range map[string]string{
		"too many characters": strings.Repeat("x", store.MaxEntryRunes+1),
		"too many lines":      strings.Repeat("l\n", store.MaxEntryLines) + "l",
	} {
		if err := st.AddMemoryEntry(ctx, "a", text, indexAt, store.MemoryActor{}); err == nil || !strings.Contains(err.Error(), "too long") {
			t.Errorf("%s: err = %v, want a too-long error", name, err)
		}
	}
	if err := st.AddMemoryEntry(ctx, "a", " \n ", indexAt, store.MemoryActor{}); err == nil {
		t.Error("blank entry accepted")
	}
	if err := st.AddMemoryEntry(ctx, "missing", "x", indexAt, store.MemoryActor{}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown agent: err = %v, want sql.ErrNoRows", err)
	}
	if err := st.AddMemoryEntry(ctx, "a", strings.Repeat("ạ", store.MaxEntryRunes), indexAt, store.MemoryActor{}); err != nil {
		t.Errorf("multibyte entry at the rune limit rejected: %v", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM memory_index`); n != 1 {
		t.Errorf("rows = %d, want only the valid entry", n)
	}
}

func TestMemoryIndex_ClosedStoreErrors(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	st.Close()
	if err := st.AddMemoryEntry(ctx, "a", "x", indexAt, store.MemoryActor{}); err == nil {
		t.Error("AddMemoryEntry on closed store succeeded")
	}
	if _, err := st.MemoryIndex(ctx, "a"); err == nil {
		t.Error("MemoryIndex on closed store succeeded")
	}
}

func TestTopics_WriteReplaceRead(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	insertNotesTestAgent(t, st, "b")

	first, err := st.WriteTopic(ctx, "a", "hook-styles", "what opens well", "first plain questions", indexAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.WriteTopic(ctx, "a", "hook-styles", "what opens well", "second self-roast wins", indexAt.Add(time.Hour))
	if err != nil || second != first {
		t.Fatalf("rewrite = %d, %v; want the same note %d", second, err, first)
	}
	if _, err := st.WriteTopic(ctx, "b", "hook-styles", "", "b's own", indexAt); err != nil {
		t.Fatalf("same topic name for another agent: %v", err)
	}

	mem, err := st.ReadMemory(ctx, "a", "topic:hook-styles")
	if err != nil || mem.Text != "second self-roast wins" || !mem.At.Equal(indexAt.Add(time.Hour)) {
		t.Fatalf("ReadMemory(topic) = %+v, %v", mem, err)
	}
	if byID, err := st.ReadMemory(ctx, "a", fmt.Sprintf("note:%d", first)); err != nil || byID.Text != mem.Text {
		t.Fatalf("ReadMemory(note:id) = %+v, %v", byID, err)
	}
	if hits, _ := st.SearchMemory(ctx, "a", "roast", nil, time.Time{}, time.Time{}, 5); len(hits) != 1 {
		t.Fatalf("search after rewrite = %v, want the new text indexed", hits)
	}
	if hits, _ := st.SearchMemory(ctx, "a", "plain", nil, time.Time{}, time.Time{}, 5); len(hits) != 0 {
		t.Fatalf("search after rewrite = %v, want the old text gone", hits)
	}
	if _, err := st.ReadMemory(ctx, "a", "topic:nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown topic: err = %v", err)
	}
	if _, err := st.ReadMemory(ctx, "a", "topic:"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("empty topic ref: err = %v", err)
	}
}

func TestTopics_RejectsBadInput(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")

	cases := map[string][3]string{
		"empty topic":      {"", "d", "text"},
		"multiline topic":  {"a\nb", "d", "text"},
		"padded topic":     {" a", "d", "text"},
		"long topic":       {strings.Repeat("t", store.MaxTopicRunes+1), "d", "text"},
		"blank text":       {"t", "d", " "},
		"long description": {"t", strings.Repeat("d", 201), "text"},
	}
	for name, c := range cases {
		if _, err := st.WriteTopic(ctx, "a", c[0], c[1], c[2], indexAt); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := st.WriteTopic(ctx, "missing", "t", "", "text", indexAt); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown agent: err = %v, want sql.ErrNoRows", err)
	}
}

func loadIndexEntries(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/memory_index_entries.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

// TestMemoryIndexBudgetMeasurement fills the index with ten realistic Vietnamese
// entries cycled until it overflows, then with the longest allowed entries, and
// records which cap binds and how many entries survive.
func TestMemoryIndexBudgetMeasurement(t *testing.T) {
	ctx := context.Background()
	fill := func(entry func(i int) string) (kept, bytes int) {
		st := openNotesTestStore(t)
		insertNotesTestAgent(t, st, "a")
		for i := 0; i < 200; i++ {
			if err := st.AddMemoryEntry(ctx, "a", entry(i), indexAt.Add(time.Duration(i)*time.Second), store.MemoryActor{}); err != nil {
				t.Fatal(err)
			}
		}
		got, _ := st.MemoryIndex(ctx, "a")
		for _, e := range got {
			bytes += len(e.Text)
		}
		return len(got), bytes
	}

	entries := loadIndexEntries(t)
	kept, size := fill(func(i int) string { return fmt.Sprintf("%s %d", entries[i%len(entries)], i) })
	t.Logf("realistic one-line entries: %d kept, %d bytes", kept, size)
	if kept >= store.MaxIndexLines || size > store.MaxIndexBytes || size+len(entries[0])*2 < store.MaxIndexBytes {
		t.Errorf("realistic entries: %d kept, %d bytes; want the byte cap to bind before the %d-line cap", kept, size, store.MaxIndexLines)
	}

	longest := strings.Repeat("ệ", store.MaxEntryRunes-3)
	kept, size = fill(func(i int) string { return fmt.Sprintf("%s%03d", longest, i) })
	t.Logf("longest entries (%d bytes each): %d kept, %d bytes", len(longest)+3, kept, size)
	if kept != store.MaxIndexBytes/(len(longest)+3) {
		t.Errorf("longest entries: %d kept, want %d", kept, store.MaxIndexBytes/(len(longest)+3))
	}
}

func TestTopicList_NewestFirstAndBounded(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	insertNotesTestAgent(t, st, "b")

	if _, err := st.WriteTopic(ctx, "b", "other", "other agent", "x", indexAt); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteTopic(ctx, "a", "old", "first", "x", indexAt); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteTopic(ctx, "a", "mid", "", "x", indexAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteTopic(ctx, "a", "old", "first rewritten", "y", indexAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	st.DB().ExecContext(ctx, `INSERT INTO notes(agent_id,text,created_at) VALUES('a','untitled note',?)`, indexAt.Format(time.RFC3339Nano))

	got, err := st.TopicList(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	want := []store.TopicSummary{{Topic: "old", Description: "first rewritten"}, {Topic: "mid"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("TopicList = %+v, want %+v", got, want)
	}

	for i := 0; i < store.MaxTopicList+5; i++ {
		if _, err := st.WriteTopic(ctx, "a", fmt.Sprintf("t%02d", i), "d", "x", indexAt.Add(time.Duration(3+i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = st.TopicList(ctx, "a")
	if len(got) != store.MaxTopicList || got[0].Topic != fmt.Sprintf("t%02d", store.MaxTopicList+4) {
		t.Fatalf("TopicList = %d entries starting at %q, want the newest %d", len(got), got[0].Topic, store.MaxTopicList)
	}

	for i := range got {
		st.DB().ExecContext(ctx, `UPDATE notes SET description=? WHERE agent_id='a' AND topic=?`, strings.Repeat("d", 200), got[i].Topic)
	}
	got, _ = st.TopicList(ctx, "a")
	chars := 0
	for _, g := range got {
		chars += utf8.RuneCountInString(g.Topic) + utf8.RuneCountInString(g.Description)
	}
	if chars > store.MaxTopicListRunes || len(got) == 0 || len(got) >= store.MaxTopicList {
		t.Fatalf("TopicList = %d entries, %d characters; want at most %d characters", len(got), chars, store.MaxTopicListRunes)
	}
}

func TestMemoryUpkeep(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	by := store.MemoryActor{WakeID: 7, Phase: "dream"}
	add := func(text string) {
		t.Helper()
		if err := st.AddMemoryEntry(ctx, "a", text, indexAt, by); err != nil {
			t.Fatal(err)
		}
	}
	texts := func() (out []string) {
		got, _ := st.MemoryIndex(ctx, "a")
		for _, e := range got {
			out = append(out, e.Text)
		}
		return out
	}
	changes := func(kind string) int {
		return countRows(t, st, `SELECT COUNT(*) FROM memory_journal WHERE agent_id='a' AND change='`+kind+`'`)
	}

	add("post at 21h")
	add("post at 21h")
	if got := texts(); len(got) != 1 || changes("added") != 1 {
		t.Fatalf("duplicate was not dropped: %q, %d added rows", got, changes("added"))
	}

	add("short lived · until 2026-11-12")
	if err := st.ExpireMemory(ctx, "a", indexAt.Add(23*time.Hour), by); err != nil || len(texts()) != 2 {
		t.Fatalf("entry expired on its last day: %q %v", texts(), err)
	}
	if err := st.ExpireMemory(ctx, "a", indexAt.Add(24*time.Hour), by); err != nil || len(texts()) != 1 || changes("expired") != 1 {
		t.Fatalf("expired entry still live: %q %v", texts(), err)
	}

	live, _ := st.MemoryIndex(ctx, "a")
	add("post at 7h")
	live, _ = st.MemoryIndex(ctx, "a")
	if err := st.SupersedeMemory(ctx, "a", live[1].ID, live[0].ID, indexAt, by); err == nil {
		t.Fatal("superseding the newer entry with the older one was accepted")
	}
	if err := st.SupersedeMemory(ctx, "a", live[0].ID, live[1].ID, indexAt, by); err != nil {
		t.Fatal(err)
	}
	if got := texts(); len(got) != 1 || got[0] != "post at 7h" || changes("superseded") != 1 {
		t.Fatalf("after supersede: %q", got)
	}

	var journalID int64
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM memory_journal WHERE change='superseded'`).Scan(&journalID); err != nil {
		t.Fatal(err)
	}
	if err := st.RestoreMemory(ctx, "a", journalID, indexAt); err != nil {
		t.Fatal(err)
	}
	if got := texts(); len(got) != 2 || got[1] != "post at 21h" || changes("restored") != 1 {
		t.Fatalf("after restore: %q", got)
	}
	if err := st.RestoreMemory(ctx, "a", 1, indexAt); err == nil {
		t.Fatal("an added journal row was restored")
	}
	var expiredID int64
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM memory_journal WHERE change='expired'`).Scan(&expiredID); err != nil {
		t.Fatal(err)
	}
	if err := st.RestoreMemory(ctx, "a", expiredID, indexAt.Add(48*time.Hour)); err == nil {
		t.Fatal("an expired entry was restored")
	}
}

func TestSupersedeCapIsAFifthPerWake(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	by := store.MemoryActor{WakeID: 3, Phase: "dream"}
	for i := 0; i < 10; i++ {
		if err := st.AddMemoryEntry(ctx, "a", fmt.Sprintf("entry %d", i), indexAt, by); err != nil {
			t.Fatal(err)
		}
	}
	live, _ := st.MemoryIndex(ctx, "a")
	newest := live[9].ID
	for i, wantErr := range []bool{false, false, true} {
		err := st.SupersedeMemory(ctx, "a", live[i].ID, newest, indexAt, by)
		if (err != nil) != wantErr {
			t.Fatalf("supersede %d: err %v, want error %v", i, err, wantErr)
		}
	}
	if err := st.SupersedeMemory(ctx, "a", live[3].ID, newest, indexAt, store.MemoryActor{WakeID: 4, Phase: "dream"}); err != nil {
		t.Fatalf("a later wake has its own cap: %v", err)
	}
}
