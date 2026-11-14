package store_test

import (
	"context"
	"testing"
	"time"
)

func TestOnlyLiveConfirmedActionsCountAsApplied(t *testing.T) {
	f := newLessonFixture(t)
	ctx := context.Background()
	check := now.Add(15 * 24 * time.Hour)
	id, err := f.add(t, "Mở bằng số.", "post", "do", f.top[:2]...)
	if err != nil {
		t.Fatal(err)
	}
	dry, unconfirmed, live := f.ids[8], f.ids[9], f.ids[0]
	for _, a := range []int64{dry, unconfirmed, live} {
		postedAt(t, f.st, a, check.Add(-time.Hour))
		f.apply(t, id, a)
	}
	if _, err = f.st.DB().Exec(`UPDATE actions SET dry_run=1 WHERE id=?`, dry); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.DB().Exec(`UPDATE actions SET media_id='' WHERE id=?`, unconfirmed); err != nil {
		t.Fatal(err)
	}
	lessons, _ := f.st.Lessons(ctx, "a", false)
	uses, err := f.st.LessonUses(ctx, "a", lessons)
	if err != nil || uses[id].AppliedN != 1 {
		t.Fatalf("applied %+v, %v; want only the live confirmed action", uses[id], err)
	}
	if n, err := f.st.ExpireLessons(ctx, "a", check); err != nil || n != 0 {
		t.Fatalf("a live use kept it alive: %d %v", n, err)
	}
	if _, err = f.st.DB().Exec(`UPDATE actions SET failed=1 WHERE id=?`, live); err != nil {
		t.Fatal(err)
	}
	if n, err := f.st.ExpireLessons(ctx, "a", check); err != nil || n != 1 {
		t.Fatalf("dry-run, unconfirmed and failed uses kept it alive: %d %v", n, err)
	}
}
