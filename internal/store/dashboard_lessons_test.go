package store_test

import (
	"context"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func TestDashboardSnapshotCarriesBandsAndLessons(t *testing.T) {
	f := newLessonFixture(t)
	ctx := context.Background()
	id, err := f.add(t, "Lead with the number.", "post", "do", f.top[:2]...)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := f.st.DashboardSnapshot(ctx, store.DashboardQuery{AgentID: "a", From: epoch, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Lessons) != 1 || snap.Lessons[0].ID != id {
		t.Fatalf("lessons %+v", snap.Lessons)
	}
	if _, ok := snap.LessonUses[id]; !ok {
		t.Fatal("no use stats for the lesson")
	}
	bands := map[string]int{}
	for _, p := range snap.Publications {
		bands[p.Band]++
	}
	if bands[store.BandTop] != 3 || bands[store.BandBottom] != 3 || bands[store.BandMid] != 4 {
		t.Fatalf("bands %v", bands)
	}
}
