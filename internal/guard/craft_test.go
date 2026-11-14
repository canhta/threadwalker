package guard_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
)

const anchor = "https://example.test/trend/1"

func craftGuard(t *testing.T, anchored bool) *guard.Guard {
	t.Helper()
	opts := liveOpts(t)
	opts.Template.Guard.TrendAnchor = anchored
	clock := opts.Now()
	opts.Now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	opts.APIQuota = func(context.Context) (guard.APIQuota, error) { return guard.APIQuota{Posts: -1, Replies: 2}, nil }
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func post(g *guard.Guard, args string) error {
	_, err := g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: args}, func() (string, error) { return "", nil })
	return err
}

func TestPostNeedsHookRationaleAndATrendReadThisWake(t *testing.T) {
	good := `"text":"model mới giá 3 đô\ndòng sau","hook":"trend_detail","rationale":"bám model mới, hook là giá, dev sẽ cãi","trend":"t1"`
	for name, tc := range map[string]struct {
		args string
		want string
	}{
		"anchored":            {good, ""},
		"unread trend":        {strings.Replace(good, `"t1"`, `"t9"`, 1), "no_trend_anchor"},
		"no hook":             {strings.Replace(good, `"hook":"trend_detail",`, "", 1), "post_brief"},
		"no rationale":        {strings.Replace(good, `"rationale":"bám model mới, hook là giá, dev sẽ cãi",`, "", 1), "post_brief"},
		"two-line rationale":  {strings.Replace(good, "bám model mới,", `bám model mới,\n`, 1), "post_brief"},
		"hook line too long":  {`"text":"` + strings.Repeat("a", 81) + `","hook":"number","rationale":"r","trend":"t1"`, "text_length"},
		"long second line ok": {`"text":"ngắn\n` + strings.Repeat("a", 200) + `","hook":"number","rationale":"r","trend":"t1"`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			g := craftGuard(t, true)
			g.NoteTrend(anchor)
			err := post(g, "{"+tc.args+"}")
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if got := blockedReason(t, err); got != tc.want {
				t.Fatalf("reason=%q want %q", got, tc.want)
			}
		})
	}
}

func TestPostWithoutTrendAnchorSettingNeedsNone(t *testing.T) {
	if err := post(craftGuard(t, false), `{"text":"hello"}`); err != nil {
		t.Fatal(err)
	}
}

func TestSeriesChecksEveryPartAndCountsAsOnePost(t *testing.T) {
	for name, tc := range map[string]struct{ parts, want string }{
		"clean series":      {`["hai","ba"]`, ""},
		"bait in part 3":    {`["hai","like và share nhé"]`, "engagement_bait"},
		"part over 500":     {`["hai","` + strings.Repeat("あ", 501) + `"]`, "text_length"},
		"too many parts":    {`["2","3","4","5","6"]`, "too_many_parts"},
		"blank part":        {`["hai"," "]`, "invalid_tool_arguments"},
		"quota for replies": {`["2","3","4"]`, "api_quota_exhausted"},
	} {
		t.Run(name, func(t *testing.T) {
			err := post(craftGuard(t, false), `{"text":"một","parts":`+tc.parts+`}`)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if got := blockedReason(t, err); got != tc.want {
				t.Fatalf("reason=%q want %q", got, tc.want)
			}
		})
	}

	opts := liveOpts(t)
	opts.Template.Limits.PostsPerDay = 1
	opts.Template.Limits.MinSecondsBetweenActions = 1
	clock := opts.Now()
	opts.Now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = post(g, `{"text":"một","parts":["hai","ba","bốn"]}`); err != nil {
		t.Fatal(err)
	}
	if err = post(g, `{"text":"another"}`); !errors.Is(err, guard.ErrActionLimit) {
		t.Fatalf("second post err=%v, want the daily limit after one series", err)
	}
}

func TestPostRepeatingATopicOfTheLastDayIsRefused(t *testing.T) {
	g := craftGuard(t, true)
	g.NoteTrend(anchor, "https://example.test/trend/2")
	first := `{"text":"Orchestrator tốn ba triệu token mỗi ngày","hook":"number","rationale":"r","trend":"t1"}`
	if err := post(g, first); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]string{
		"same trend":   `{"text":"chuyện khác hẳn","hook":"number","rationale":"r","trend":"t1"}`,
		"same wording": `{"text":"Orchestrator tốn ba triệu token mỗi tuần","hook":"number","rationale":"r","trend":"t2"}`,
	} {
		if got := blockedReason(t, post(g, args)); got != "repeat_topic" {
			t.Fatalf("%s: reason=%q", name, got)
		}
	}
	other := `{"text":"Cursor bán gói mới giá hai mươi đô","hook":"number","rationale":"r","trend":"t2"}`
	if err := post(g, other); err != nil {
		t.Fatal(err)
	}
}

func TestOpenOwnerNoteRefusesPostsThatDoNotAnswerIt(t *testing.T) {
	opts := liveOpts(t)
	clock := opts.Now()
	opts.Now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = opts.Store.OwnerNote(context.Background(), []string{opts.Template.ID}, "k", "post about the AP story", clock); err != nil {
		t.Fatal(err)
	}
	notes, err := opts.Store.OpenOwnerNotes(context.Background(), opts.Template.ID, clock)
	if err != nil || len(notes) != 1 {
		t.Fatalf("notes=%v err=%v", notes, err)
	}
	if got := blockedReason(t, post(g, `{"text":"unrelated"}`)); got != "owner_note_open" {
		t.Fatalf("reason=%q", got)
	}
	if got := blockedReason(t, post(g, `{"text":"unrelated","answers_note":99}`)); got != "note_not_open" {
		t.Fatalf("reason=%q", got)
	}
	if err = post(g, `{"text":"the AP story","answers_note":`+strconv.FormatInt(notes[0].ID, 10)+`}`); err != nil {
		t.Fatal(err)
	}
	if err = post(g, `{"text":"anything else"}`); err != nil {
		t.Fatalf("note answered, post should pass: %v", err)
	}
}

func TestTrendResolvesOnlyItsRef(t *testing.T) {
	const long = "https://news.example.test/rss/articles/CBMisAFBVV95cUxNNVk1clpGZXV2a3lhRkc0eUIyUEJ0LWZORnB1OWRFbG1m?oc=5"
	g := craftGuard(t, true)
	g.NoteTrend(long)
	if got, ok := g.Trend(" T1 "); !ok || got != long {
		t.Fatalf("the ref: %q %v", got, ok)
	}
	for _, copied := range []string{long, strings.Replace(long, "RkG0", "RkG8", 1), "t2", "x1", ""} {
		if got, ok := g.Trend(copied); ok {
			t.Fatalf("%q resolved to %q", copied, got)
		}
	}
}
