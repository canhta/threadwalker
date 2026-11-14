package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

func verdict(pass bool, problems ...string) quality.CriticVerdict {
	return quality.CriticVerdict{Pass: pass, Problems: problems, Scores: quality.CriticScores{Hook: 3, NaturalVietnamese: 3, Specificity: 3, ForcedSelfReference: 5, TargetFit: 3, Novelty: 3}}
}

func criticGuard(t *testing.T, scope guard.Scope) *guard.Guard {
	t.Helper()
	scope.Template = policyTemplate()
	scope.Template.ID = "agent"
	scope.Template.Limits.BrowserRepliesPerDay = 5
	g, err := guard.New(scope)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// criticReplyArgs answers the post criticParent stored: the first post of a fresh store is p1.
const criticReplyArgs = `{"text":"giá vé tăng gấp đôi mà vẫn hết trong 5 phút, ai mua được vậy?","target":"p1"}`

const criticPostURL = "https://www.threads.com/@someone/post/abc"

// criticParent has g read the post the critic tests reply to, with the text the critic sees as its parent.
func criticParent(g *guard.Guard, text string) {
	g.NotePost(context.Background(), threads.Post{URL: criticPostURL, Text: text})
}

func criticReply(g *guard.Guard, args string) (string, error) {
	criticParent(g, "vé concert tăng giá gấp đôi")
	return g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: args}, func() (string, error) { return "sent", nil })
}

func TestCriticPassStoresVerdictAndPublishes(t *testing.T) {
	st, scope := setup(t)
	var seen quality.CriticInput
	scope.Critic = func(_ context.Context, in quality.CriticInput) (quality.CriticVerdict, error) {
		seen = in
		return verdict(true), nil
	}
	if out, err := criticReply(criticGuard(t, scope), criticReplyArgs); err != nil || out != "sent" {
		t.Fatalf("out %q err %v", out, err)
	}
	if seen.Kind != "reply" || seen.Parent != "vé concert tăng giá gấp đôi" || len(seen.Texts) != 1 {
		t.Fatalf("critic input %+v", seen)
	}
	var stored string
	if err := st.DB().QueryRow(`SELECT critic_json FROM actions`).Scan(&stored); err != nil || !strings.Contains(stored, `"pass":true`) || !strings.Contains(stored, `"natural_vietnamese":3`) {
		t.Fatalf("stored verdict %q %v", stored, err)
	}
}

func TestCriticRejectionRefusesWithProblemsAndCapsTheWake(t *testing.T) {
	st, scope := setup(t)
	calls := 0
	scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) {
		calls++
		return verdict(false, "câu đầu không giữ chân người đọc", "hai câu đùa dán bằng dấu chấm phẩy"), nil
	}
	g := criticGuard(t, scope)
	for i := 0; i < guard.DefaultCriticRejections; i++ {
		_, err := criticReply(g, criticReplyArgs)
		var blocked *guard.BlockedError
		if !errors.As(err, &blocked) || blocked.Reason != "critic_rejected" || !strings.Contains(guard.Hint(blocked.Reason, blocked.Detail), "dấu chấm phẩy") {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// The cap is spent: no further draft reaches the critic.
	_, err := criticReply(g, criticReplyArgs)
	var blocked *guard.BlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "critic_limit" || calls != guard.DefaultCriticRejections {
		t.Fatalf("err %v calls %d", err, calls)
	}
	var actions int
	st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&actions)
	if actions != 0 {
		t.Fatalf("%d actions reserved", actions)
	}
}

func TestCriticFailureFailsClosed(t *testing.T) {
	_, scope := setup(t)
	scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) {
		return quality.CriticVerdict{}, errors.New("provider down")
	}
	_, err := criticReply(criticGuard(t, scope), criticReplyArgs)
	var blocked *guard.BlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "critic_unavailable" {
		t.Fatalf("err %v", err)
	}
}

func TestWriterRevisesWithProblemsAndFinalTextIsPublishedAndStored(t *testing.T) {
	st, scope := setup(t)
	var problemsSeen [][]string
	scope.Writer = func(_ context.Context, in quality.WriterInput) ([]string, error) {
		problemsSeen = append(problemsSeen, in.Problems)
		if len(in.Problems) == 0 {
			return []string{"bản nháp đầu"}, nil
		}
		return []string{"bản đã sửa"}, nil
	}
	scope.Critic = func(_ context.Context, in quality.CriticInput) (quality.CriticVerdict, error) {
		if in.Texts[0] == "bản nháp đầu" {
			return verdict(false, "quá cứng"), nil
		}
		return verdict(true), nil
	}
	g := criticGuard(t, scope)
	criticParent(g, "bài gốc")
	var final []string
	_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: criticReplyArgs, Final: &final}, func() (string, error) { return "sent", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(problemsSeen) != 2 || problemsSeen[1][0] != "quá cứng" || len(final) != 1 || final[0] != "bản đã sửa" {
		t.Fatalf("problems %v final %v", problemsSeen, final)
	}
	var text string
	st.DB().QueryRow(`SELECT text FROM actions`).Scan(&text)
	if text != "bản đã sửa" {
		t.Fatalf("stored text %q", text)
	}
}

func TestWriterOutputMeetsTheContentRules(t *testing.T) {
	_, scope := setup(t)
	scope.Writer = func(context.Context, quality.WriterInput) ([]string, error) {
		return []string{"xem thêm tại https://spam.example"}, nil
	}
	scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) {
		t.Fatal("critic saw a draft the content rules refuse")
		return quality.CriticVerdict{}, nil
	}
	_, err := criticReply(criticGuard(t, scope), criticReplyArgs)
	var blocked *guard.BlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "critic_rejected" || !strings.Contains(blocked.Detail, "link") {
		t.Fatalf("err %v", err)
	}
}

func TestALongDraftReachesTheWriterWhoseTextIsCheckedInFull(t *testing.T) {
	_, scope := setup(t)
	scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) { return verdict(true), nil }
	short := "vé concert hết trong 5 phút, ai mua được vậy?"
	final := short
	scope.Writer = func(context.Context, quality.WriterInput) ([]string, error) { return []string{final}, nil }
	long := `{"text":"` + strings.Repeat("rất dài ", 80) + `","target":"p1"}`
	if out, err := criticReply(criticGuard(t, scope), long); err != nil || out != "sent" {
		t.Fatalf("a long rough draft never reached the writer: %q %v", out, err)
	}
	_, scope = setup(t)
	scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) { return verdict(true), nil }
	scope.Writer = func(context.Context, quality.WriterInput) ([]string, error) {
		return []string{strings.Repeat("rất dài ", 80)}, nil
	}
	if _, err := criticReply(criticGuard(t, scope), long); err == nil {
		t.Fatal("a final text over 500 characters was published")
	}
}

func TestAReviewedDraftWaitsOutThePacingRuleInsteadOfBeingLost(t *testing.T) {
	for _, c := range []struct {
		withSleep bool
		spacing   int
	}{{true, 300}, {false, 300}, {true, 3600}} {
		// A wait beyond MaxPacingWait is refused even with a sleeper.
		withSleep, waits := c.withSleep, c.withSleep && c.spacing == 300
		st, scope := setup(t)
		clock := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		scope.Now = func() time.Time { return clock }
		slept := time.Duration(0)
		if withSleep {
			scope.Sleep = func(_ context.Context, d time.Duration) error { slept += d; clock = clock.Add(d); return nil }
		}
		scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) { return verdict(true), nil }
		scope.Template = policyTemplate()
		scope.Template.ID = "agent"
		scope.Template.Limits.MinSecondsBetweenActions = c.spacing
		scope.Template.Limits.BrowserRepliesPerDay = 5
		g, err := guard.New(scope)
		if err != nil {
			t.Fatal(err)
		}
		reply := func(user string) error {
			url := "https://www.threads.com/@" + user + "/post/abc"
			ref := g.NotePost(context.Background(), threads.Post{URL: url, Text: "vé concert tăng giá gấp đôi"})
			_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"giá vé tăng gấp đôi mà vẫn hết trong 5 phút","target":"` + ref + `"}`}, func() (string, error) { return "sent", nil })
			return err
		}
		if err := reply("first"); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(2 * time.Minute)
		err = reply("second")
		if waits && (err != nil || slept < 3*time.Minute || slept > 3*time.Minute+2*time.Second) {
			t.Fatalf("reviewed draft not published after waiting: %v slept %v", err, slept)
		}
		if !waits && err == nil {
			t.Fatal("without a sleeper, or past MaxPacingWait, the pacing rule must still refuse")
		}
		var blocked []string
		rows, qerr := st.DB().Query(`SELECT blocked_by FROM tool_calls ORDER BY id`)
		if qerr != nil {
			t.Fatal(qerr)
		}
		for rows.Next() {
			var b string
			if qerr = rows.Scan(&b); qerr != nil {
				t.Fatal(qerr)
			}
			blocked = append(blocked, b)
		}
		rows.Close()
		want := []string{"", ""}
		if !waits {
			want[1] = "min_seconds_between_actions"
		}
		if fmt.Sprint(blocked) != fmt.Sprint(want) {
			t.Fatalf("case %+v audit blocked_by %q, want %q", c, blocked, want)
		}
	}
}

func TestALimitRefusesBeforeTheWriterAndCriticSpendAnything(t *testing.T) {
	_, scope := setup(t)
	writes, reviews := 0, 0
	scope.Writer = func(_ context.Context, in quality.WriterInput) ([]string, error) { writes++; return in.Draft, nil }
	scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) {
		reviews++
		return verdict(true), nil
	}
	g := criticGuard(t, scope)
	if _, err := criticReply(g, criticReplyArgs); err != nil {
		t.Fatal(err)
	}
	_, err := criticReply(g, strings.Replace(criticReplyArgs, "giá vé", "vé", 1))
	var blocked *guard.BlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "thread_per_day" {
		t.Fatalf("second reply in the thread: %v", err)
	}
	if writes != 1 || reviews != 1 {
		t.Fatalf("the refused reply still ran the writer %d and critic %d times", writes-1, reviews-1)
	}
}

func TestTraceKeepsTheBriefAndEveryDraftBounded(t *testing.T) {
	st, scope := setup(t)
	scope.Writer = func(_ context.Context, in quality.WriterInput) ([]string, error) {
		if len(in.Problems) == 0 {
			return []string{"bản nháp đầu"}, nil
		}
		return []string{strings.Repeat("dài ", 400)}, nil
	}
	scope.Critic = func(_ context.Context, in quality.CriticInput) (quality.CriticVerdict, error) {
		if in.Texts[0] == "bản nháp đầu" {
			return verdict(false, "quá cứng"), nil
		}
		return verdict(true), nil
	}
	g := criticGuard(t, scope)
	criticParent(g, strings.Repeat("gốc ", 500))
	args := `{"text":"nháp thô","target":"p1","hook":"trend_detail","angle":"góc nhìn","fact":"một con số"}`
	// The second draft is over the length rule, so the call is refused after it.
	if _, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: args}, func() (string, error) { return "sent", nil }); err == nil {
		t.Fatal("an over-long draft was published")
	}
	var raw string
	if err := st.DB().QueryRow(`SELECT detail_json FROM tool_call_details WHERE detail_json LIKE '%"brief"%'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var d store.CallDetail
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	b := d.Brief
	if b == nil || b.Kind != "reply" || b.Angle != "góc nhìn" || b.Fact != "một con số" || b.Hook != "trend_detail" || len(b.Rough) != 1 || b.Rough[0] != "nháp thô" {
		t.Fatalf("brief %+v", b)
	}
	if n := utf8.RuneCountInString(b.Parent); n != stdx.MaxThreadsPostRunes {
		t.Fatalf("parent kept %d runes", n)
	}
	if len(d.Tries) != 4 || len(d.Tries[0].Texts) != 0 || d.Tries[1].Texts[0] != "bản nháp đầu" || utf8.RuneCountInString(d.Tries[3].Texts[0]) != store.MaxTraceText || d.Tries[3].Pass {
		t.Fatalf("tries %+v", d.Tries)
	}
}

func TestWriterGetsTheCitedLessonsOnly(t *testing.T) {
	st, scope := setup(t)
	for i, l := range []struct{ kind, text string }{{"reply", "answer with one detail"}, {"post", "post only lesson"}, {"any", "unused lesson"}} {
		if _, err := st.DB().Exec(`INSERT INTO lessons(id,agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at) VALUES(?,?,?,?,?,'[]','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z')`, i+1, "agent", l.kind, "do", l.text); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	scope.Writer = func(_ context.Context, in quality.WriterInput) ([]string, error) {
		seen = in.Lessons
		return in.Draft, nil
	}
	scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) { return verdict(true), nil }
	args := strings.Replace(criticReplyArgs, `"text"`, `"lesson_ids":[1],"text"`, 1)
	if _, err := criticReply(criticGuard(t, scope), args); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "answer with one detail" {
		t.Fatalf("writer lessons %v", seen)
	}
}
