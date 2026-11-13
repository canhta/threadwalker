package guard_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/quality"
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

const criticReplyArgs = `{"text":"giá vé tăng gấp đôi mà vẫn hết trong 5 phút, ai mua được vậy?","target_url":"https://www.threads.com/@someone/post/abc","target_username":"someone"}`

func criticReply(g *guard.Guard, args string) (string, error) {
	g.NoteRead("", "https://www.threads.com/@someone/post/abc", "vé concert tăng giá gấp đôi")
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
	g.NoteRead("", "https://www.threads.com/@someone/post/abc", "bài gốc")
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
	long := `{"text":"` + strings.Repeat("rất dài ", 80) + `","target_url":"https://www.threads.com/@someone/post/abc","target_username":"someone"}`
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
	for _, withSleep := range []bool{true, false} {
		_, scope := setup(t)
		clock := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		scope.Now = func() time.Time { return clock }
		slept := time.Duration(0)
		if withSleep {
			scope.Sleep = func(_ context.Context, d time.Duration) error { slept += d; clock = clock.Add(d); return nil }
		}
		scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) { return verdict(true), nil }
		scope.Template = policyTemplate()
		scope.Template.ID = "agent"
		scope.Template.Limits.MinSecondsBetweenActions = 300
		scope.Template.Limits.BrowserRepliesPerDay = 5
		g, err := guard.New(scope)
		if err != nil {
			t.Fatal(err)
		}
		reply := func(user string) error {
			url := "https://www.threads.com/@" + user + "/post/abc"
			g.NoteRead("", url, "vé concert tăng giá gấp đôi")
			_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"giá vé tăng gấp đôi mà vẫn hết trong 5 phút","target_url":"` + url + `","target_username":"` + user + `"}`}, func() (string, error) { return "sent", nil })
			return err
		}
		if err := reply("first"); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(2 * time.Minute)
		err = reply("second")
		if withSleep && (err != nil || slept < 3*time.Minute || slept > 3*time.Minute+2*time.Second) {
			t.Fatalf("reviewed draft not published after waiting: %v slept %v", err, slept)
		}
		if !withSleep && err == nil {
			t.Fatal("without a sleeper the pacing rule must still refuse")
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
