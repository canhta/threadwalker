package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func TestTranscriptKeepsOrderAndIsolatesAgents(t *testing.T) {
	s, _ := retentionStore(t)
	ctx := context.Background()
	at := retentionNow
	for i, k := range []string{store.TranscriptContext, store.TranscriptAssistant, store.TranscriptEnd} {
		if err := s.AppendTranscript(ctx, "a", 1, k, k+"-body", at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Transcript(ctx, "a", 1)
	if err != nil || len(got) != 3 || got[0].Kind != store.TranscriptContext || got[2].Seq != 3 || got[1].Body != "assistant-body" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	other, err := s.Transcript(ctx, "b", 1)
	if err != nil || len(other) != 0 {
		t.Fatalf("other agent saw %+v err=%v", other, err)
	}
}

func TestTranscriptIsBoundedByEntriesAndBytes(t *testing.T) {
	ctx := context.Background()
	for name, body := range map[string]string{"entries": "x", "bytes": strings.Repeat("y", store.MaxTranscriptBytes/4)} {
		t.Run(name, func(t *testing.T) {
			s, _ := retentionStore(t)
			for i := 0; i < store.MaxTranscriptEntries+10; i++ {
				if err := s.AppendTranscript(ctx, "a", 1, store.TranscriptAssistant, body, retentionNow); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.AppendTranscript(ctx, "a", 1, store.TranscriptEnd, "completed", retentionNow); err != nil {
				t.Fatal(err)
			}
			got, err := s.Transcript(ctx, "a", 1)
			if err != nil {
				t.Fatal(err)
			}
			size, truncated := 0, 0
			for _, e := range got {
				size += len(e.Body)
				if e.Kind == store.TranscriptTruncated {
					truncated++
				}
			}
			last := got[len(got)-1]
			if len(got) > store.MaxTranscriptEntries || size > store.MaxTranscriptBytes+len("completed") || truncated != 1 || last.Kind != store.TranscriptEnd {
				t.Fatalf("entries=%d size=%d truncated=%d last=%s", len(got), size, truncated, last.Kind)
			}
		})
	}
}

func TestTranscriptPrunedAfterEightDays(t *testing.T) {
	s, _ := retentionStore(t)
	ctx := context.Background()
	if err := s.AppendTranscript(ctx, "a", 1, store.TranscriptContext, "old", retentionNow.Add(-9*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTranscript(ctx, "a", 2, store.TranscriptContext, "new", retentionNow.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := prune(t, s)["wake_transcripts"]; got != 1 {
		t.Fatalf("pruned=%d", got)
	}
	if age, ok := store.RetentionAge("wake_transcripts"); !ok || age != 8*24*time.Hour {
		t.Fatalf("age=%v ok=%v", age, ok)
	}
}

func TestWakesListsNewestFirstWithinSince(t *testing.T) {
	s, _ := retentionStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		id, err := s.BeginWake(ctx, "a", retentionNow.Add(time.Duration(i)*time.Hour), retentionNow.Add(24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err = s.FinishWake(ctx, id, retentionNow.Add(time.Duration(i)*time.Hour+time.Minute), 2, "completed"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Wakes(ctx, "a", retentionNow.Add(30*time.Minute), 5)
	if err != nil || len(got) != 2 || got[0].ID != 3 || got[1].EndReason != "completed" || got[1].Steps != 2 || got[1].Kind != "agent" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if limited, _ := s.Wakes(ctx, "a", time.Time{}.Add(24*time.Hour), 1); len(limited) != 1 {
		t.Fatalf("limit ignored: %+v", limited)
	}
}

func TestOverdueRetentionAndSchemaVersion(t *testing.T) {
	s, _ := retentionStore(t)
	ctx := context.Background()
	applied, expected, err := s.SchemaVersion(ctx)
	if err != nil || applied != expected || applied == 0 {
		t.Fatalf("applied=%d expected=%d err=%v", applied, expected, err)
	}
	if err = s.AppendTranscript(ctx, "a", 1, store.TranscriptContext, "x", retentionNow.Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.AppendTranscript(ctx, "a", 2, store.TranscriptContext, "x", retentionNow.Add(-8*24*time.Hour-time.Hour)); err != nil {
		t.Fatal(err)
	}
	over, err := s.OverdueRetention(ctx, retentionNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range over {
		if want := int64(map[string]int{"wake_transcripts": 1}[o.Table]); o.Deleted != want {
			t.Fatalf("%s overdue=%d want %d", o.Table, o.Deleted, want)
		}
	}
	if _, err = s.Prune(ctx, retentionNow); err != nil {
		t.Fatal(err)
	}
	if over, _ = s.OverdueRetention(ctx, retentionNow); over[0].Deleted != 0 {
		t.Fatalf("overdue after prune: %+v", over)
	}
}
