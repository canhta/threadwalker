package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

var now = epoch.AddDate(0, 0, 3)

// lessonFixture has ten posts at 6h, likes 5 3 8 1 9 0 7 2 6 4: ids[2], ids[4]
// and ids[6] are top, ids[3], ids[5] and ids[7] are bottom.
type lessonFixture struct {
	st          *store.Store
	ids         []int64
	top, bottom []int64
}

func newLessonFixture(t *testing.T) lessonFixture {
	t.Helper()
	st := openTestStore(t)
	insertTestAgent(t, st, "a")
	insertTestAgent(t, st, "b")
	ids := seedLikes(t, st, "a", 5, 3, 8, 1, 9, 0, 7, 2, 6, 4)
	return lessonFixture{st, ids, []int64{ids[2], ids[4], ids[6]}, []int64{ids[3], ids[5], ids[7]}}
}

func reason(err error) string {
	var r *store.LessonRefusal
	if errors.As(err, &r) {
		return r.Reason
	}
	if err != nil {
		return "error: " + err.Error()
	}
	return ""
}

func (f lessonFixture) add(t *testing.T, text, kind, polarity string, ids ...int64) (int64, error) {
	t.Helper()
	return f.st.AddLesson(context.Background(), "a", kind, polarity, text, ids, now)
}

func TestAddLessonGates(t *testing.T) {
	f := newLessonFixture(t)
	ctx := context.Background()
	early := seed(t, f.st, "a", "post", 1, 1, 0, []int{1}, time.Hour)
	foreign := seed(t, f.st, "b", "post", 1, 1, 0, []int{6}, 6*time.Hour)
	for _, tc := range []struct {
		name, kind, polarity, text string
		ids                        []int64
		want                       string
	}{
		{"one id", "post", "do", "one", f.top[:1], "lesson_citations"},
		{"six ids", "any", "do", "six", append(slices.Clone(f.top), f.ids[8], f.ids[0], f.ids[1]), "lesson_citations"},
		{"repeated id", "post", "do", "repeat", []int64{f.top[0], f.top[0]}, "lesson_citations"},
		{"foreign action", "post", "do", "foreign", []int64{f.top[0], foreign}, "lesson_foreign_citation"},
		{"unknown action", "post", "do", "unknown", []int64{f.top[0], 9999}, "lesson_foreign_citation"},
		{"not ranked at 6h", "post", "do", "early", []int64{f.top[0], early}, "lesson_unranked"},
		{"wrong kind", "reply", "do", "kind", f.top[:2], "lesson_kind_mismatch"},
		{"do cites a bottom action", "post", "do", "band", []int64{f.top[0], f.bottom[0]}, "lesson_band"},
		{"do cites a mid action", "post", "do", "mid", []int64{f.top[0], f.ids[0]}, "lesson_band"},
		{"avoid cites a top action", "post", "avoid", "band", []int64{f.bottom[0], f.top[0]}, "lesson_band"},
		{"too long", "post", "do", strings.Repeat("ê", store.MaxLessonRunes+1), f.top[:2], "lesson_too_long"},
	} {
		if _, err := f.add(t, tc.text, tc.kind, tc.polarity, tc.ids...); reason(err) != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.want)
		}
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM lessons`); n != 0 {
		t.Fatalf("refusals stored %d lessons", n)
	}
	if _, err := f.add(t, strings.Repeat("ê", store.MaxLessonRunes), "post", "do", f.top[:2]...); err != nil {
		t.Fatalf("240 runes: %v", err)
	}
	if _, err := f.add(t, "avoid long hooks", "any", "avoid", f.bottom...); err != nil {
		t.Fatalf("avoid with bottom actions and kind any: %v", err)
	}
	lessons, err := f.st.Lessons(ctx, "a", false)
	if err != nil || len(lessons) != 2 || lessons[0].Polarity != "avoid" || !slices.Equal(lessons[0].Evidence, f.bottom) || !lessons[0].Active() || !lessons[0].ConfirmedAt.Equal(now) {
		t.Fatalf("stored %+v, %v", lessons, err)
	}
}

func TestLessonCapDuplicatesAndTombstones(t *testing.T) {
	f := newLessonFixture(t)
	ctx := context.Background()
	id, err := f.add(t, "Mở bằng một con số cụ thể.", "post", "do", f.top[:2]...)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"mở bằng một con số cụ thể", "  MỞ  bằng một con số, cụ thể!! "} {
		if _, err := f.add(t, text, "post", "do", f.top[:2]...); reason(err) != "lesson_duplicate" {
			t.Errorf("%q: %v, want duplicate", text, err)
		}
	}
	if err = f.st.ForgetLesson(ctx, "a", id, now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.add(t, "mở bằng một con số cụ thể", "post", "do", f.top[:2]...); reason(err) != "lesson_forgotten" {
		t.Errorf("owner-forgotten lesson came back: %v", err)
	}
	// A lesson the dream retired may be written again.
	id, err = f.add(t, "Hỏi một câu ngắn.", "post", "do", f.top[:2]...)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.RetireLesson(ctx, "a", id, store.RetiredByDream, "no longer holds", now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.add(t, "hỏi một câu ngắn", "post", "do", f.top[:2]...); err != nil {
		t.Errorf("dream-retired lesson refused: %v", err)
	}
	// Ten active lessons is the cap.
	for i := range store.MaxLessons - 1 {
		if _, err = f.add(t, "lesson number "+strings.Repeat("x", i+1), "post", "do", f.top[:2]...); err != nil {
			t.Fatalf("lesson %d: %v", i, err)
		}
	}
	if _, err = f.add(t, "one too many", "post", "do", f.top[:2]...); reason(err) != "lessons_full" {
		t.Fatalf("eleventh: %v", err)
	}
	if err = f.st.RetireLesson(ctx, "a", id+1, store.RetiredByDream, "make room", now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.add(t, "one too many", "post", "do", f.top[:2]...); err != nil {
		t.Fatalf("after retiring: %v", err)
	}
	// The same sentence is another agent's own.
	if _, err = f.st.AddLesson(ctx, "b", "post", "do", "one too many", f.top[:2], now); reason(err) != "lesson_foreign_citation" {
		t.Fatalf("agent b citing agent a: %v", err)
	}
}

func TestConfirmRetireAndForget(t *testing.T) {
	f := newLessonFixture(t)
	ctx := context.Background()
	id, err := f.add(t, "Mở bằng số.", "post", "do", f.top[:2]...)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(48 * time.Hour)
	for _, tc := range []struct {
		name string
		id   int64
		ids  []int64
		want string
	}{
		{"nothing new", id, f.top[:2], "lesson_no_new_evidence"},
		{"new but bottom", id, []int64{f.bottom[0]}, "lesson_band"},
		{"no ids", id, nil, "lesson_citations"},
		{"unknown lesson", 77, f.top[2:], "lesson_unknown"},
	} {
		if err := f.st.ConfirmLesson(ctx, "a", tc.id, tc.ids, later); reason(err) != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.want)
		}
	}
	if err = f.st.ConfirmLesson(ctx, "a", id, f.top, later); err != nil {
		t.Fatal(err)
	}
	got, _ := f.st.Lessons(ctx, "a", false)
	if len(got) != 1 || !slices.Equal(got[0].Evidence, f.top) || !got[0].ConfirmedAt.Equal(later) || !got[0].CreatedAt.Equal(now) {
		t.Fatalf("after confirm: %+v", got)
	}
	if err = f.st.RetireLesson(ctx, "a", id, store.RetiredByDream, "contradicted", later); err != nil {
		t.Fatal(err)
	}
	if err = f.st.RetireLesson(ctx, "a", id, store.RetiredByDream, "again", later); reason(err) != "lesson_unknown" {
		t.Errorf("retiring twice: %v", err)
	}
	if err = f.st.ConfirmLesson(ctx, "a", id, f.top, later); reason(err) != "lesson_unknown" {
		t.Errorf("confirming a retired lesson: %v", err)
	}
	if err = f.st.ForgetLesson(ctx, "b", id, later); reason(err) != "lesson_unknown" {
		t.Errorf("another agent forgot it: %v", err)
	}
	active, _ := f.st.Lessons(ctx, "a", false)
	all, _ := f.st.Lessons(ctx, "a", true)
	if len(active) != 0 || len(all) != 1 || all[0].RetiredBy != "dream" || all[0].RetireReason != "contradicted" || !all[0].RetiredAt.Equal(later) {
		t.Fatalf("active %d, all %+v", len(active), all)
	}
}

func (f lessonFixture) apply(t *testing.T, lesson, action int64) {
	t.Helper()
	if _, err := f.st.DB().Exec(`INSERT INTO action_lessons(action_id,lesson_id) VALUES(?,?)`, action, lesson); err != nil {
		t.Fatal(err)
	}
}

// postedAt moves an action to a given time.
func postedAt(t *testing.T, st *store.Store, id int64, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(`UPDATE actions SET posted_at=? WHERE id=?`, at.Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
}

func TestExpiryNeedsNoConfirmationAndNoUse(t *testing.T) {
	f := newLessonFixture(t)
	ctx := context.Background()
	add := func(text string) int64 {
		id, err := f.add(t, text, "post", "do", f.top[:2]...)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	check := now.Add(15 * 24 * time.Hour)
	setConfirmed := func(id int64, at time.Time) {
		if _, err := f.st.DB().Exec(`UPDATE lessons SET confirmed_at=? WHERE id=?`, at.Format(time.RFC3339Nano), id); err != nil {
			t.Fatal(err)
		}
	}
	stale, oldUse, used, confirmed, young := add("stale"), add("old use"), add("used"), add("confirmed"), add("young")
	setConfirmed(confirmed, check.Add(-5*24*time.Hour))
	setConfirmed(young, check.Add(-13*24*time.Hour))
	f.apply(t, oldUse, f.ids[8]) // posted 15 days before the check
	postedAt(t, f.st, f.ids[9], check.Add(-24*time.Hour))
	f.apply(t, used, f.ids[9])
	n, err := f.st.ExpireLessons(ctx, "a", check)
	if err != nil || n != 2 {
		t.Fatalf("expired %d, %v; want 2", n, err)
	}
	all, _ := f.st.Lessons(ctx, "a", true)
	by := map[int64]string{}
	for _, l := range all {
		by[l.ID] = l.RetiredBy
	}
	if by[stale] != "expiry" || by[oldUse] != "expiry" || by[used] != "" || by[confirmed] != "" || by[young] != "" {
		t.Fatalf("retired by: %v", by)
	}
	if again, _ := f.st.ExpireLessons(ctx, "a", check); again != 0 {
		t.Errorf("expired %d twice", again)
	}
}

func TestLessonUseStats(t *testing.T) {
	f := newLessonFixture(t)
	ctx := context.Background()
	id, err := f.add(t, "Mở bằng số.", "post", "do", f.top[:2]...)
	if err != nil {
		t.Fatal(err)
	}
	// Everything below is posted after the lesson: three applied, two not, one failed.
	post := func(likes int64) int64 {
		a := seed(t, f.st, "a", "post", likes, likes, 0, []int{6}, 6*time.Hour)
		postedAt(t, f.st, a, now.Add(time.Hour))
		return a
	}
	for _, likes := range []int64{9, 8, 0} {
		f.apply(t, id, post(likes))
	}
	post(4)
	post(5)
	failed := post(1)
	f.apply(t, id, failed)
	if _, err := f.st.DB().Exec(`UPDATE actions SET failed=1 WHERE id=?`, failed); err != nil {
		t.Fatal(err)
	}
	lessons, _ := f.st.Lessons(ctx, "a", false)
	uses, err := f.st.LessonUses(ctx, "a", lessons)
	if err != nil {
		t.Fatal(err)
	}
	u := uses[id]
	if u.AppliedN != 3 || u.AppliedRanked != 3 || u.OthersN != 2 || u.AppliedMedian <= u.OthersMedian || u.OthersMedian == 0 {
		t.Fatalf("use %+v", u)
	}
	other, _ := f.add(t, "Hỏi ngắn.", "post", "do", f.top[:2]...)
	lessons, _ = f.st.Lessons(ctx, "a", false)
	uses, _ = f.st.LessonUses(ctx, "a", lessons)
	if z := uses[other]; z.AppliedN != 0 || z.AppliedMedian != 0 || z.OthersN != 5 {
		t.Fatalf("unused %+v", z)
	}
}
