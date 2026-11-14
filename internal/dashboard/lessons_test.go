package dashboard_test

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func TestLessonsPanelShowsActiveAndRetiredWithLinksAndBands(t *testing.T) {
	snap := measured()
	snap.Publications[0].Band, snap.Publications[0].Rank, snap.Publications[0].Pool, snap.Publications[0].Window = store.BandTop, 0.9, 20, 6
	snap.Publications = append(snap.Publications, pub(2, "reply", ago(day), ownLink("DEF456")), pub(3, "reply", ago(day), "https://www.threads.net/@someone_else/post/ZZZ"))
	snap.Lessons = []store.Lesson{
		{ID: 5, Kind: "reply", Polarity: "do", Text: "Answer with one concrete number.", Evidence: []int64{1, 2, 3}, CreatedAt: ago(3 * day)},
		{ID: 4, Kind: "post", Polarity: "avoid", Text: "Open with a greeting.", Evidence: []int64{1, 2}, CreatedAt: ago(9 * day), RetiredAt: ago(day), RetiredBy: store.RetiredByOwner, RetireReason: "forgotten by the owner"},
	}
	snap.LessonUses = map[int64]store.LessonUse{5: {AppliedN: 4, AppliedRanked: 3, AppliedMedian: 0.8, OthersN: 10, OthersMedian: 0.5}}
	h := newHandler(newSource(snap))
	body := get(h, "/").Body.String()
	for _, want := range []string{
		`<span class="ft">Lessons</span><span class="fs">1 active, 1 retired`,
		"Answer with one concrete number.",
		"Applied 4 times, median rank 0.80 (3 ranked); other actions 0.50 (10)",
		`href="https://threads.net/@own_alpha/post/DEF456"`,
		"Retired by the owner on",
		"Open with a greeting.",
		`tag band-top" title="Rank 0.90 of 20 at 6h"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "someone_else") || strings.Contains(body, "ZZZ") {
		t.Fatal("a foreign action became a lesson link")
	}
	if page := mustPage(t, h); len(page.Lessons.Active) != 1 || len(page.Lessons.Retired) != 1 || len(page.Lessons.Active[0].Cited) != 2 {
		t.Fatalf("lessons panel %+v", page.Lessons)
	}
}

func TestLessonsRowIsFlatWithoutLessons(t *testing.T) {
	body := get(newHandler(newSource(measured())), "/").Body.String()
	if !strings.Contains(body, `<span class="ft">Lessons</span><span class="fs">None yet.</span>`) {
		t.Fatal("empty lessons row missing")
	}
}
