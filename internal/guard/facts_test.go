package guard_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

func TestSelfStatNumbersMustMatchFactSheet(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Guard.SelfStatUnits = []string{"cent", "$", "view", "token", "đô"}
	opts.Template.Limits.PostsPerDay = 100
	opts.Template.Limits.RepliesPerDay = 100
	opts.Template.Limits.RepliesPerUser7d = 100
	opts.Template.Limits.MinSecondsBetweenActions = 1
	opts.Template.Limits.MaxUSDPerDay = 1
	if err := st.RecordLLMUsage(context.Background(), "agent", 1, "2026-11-11", store.LLMUsage{TokensIn: 1000, TokensOut: 500, CostUSD: 0.0813}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		text, refused string
	}{
		{"hôm nay tiêu 8 cent", ""},
		{"hôm nay tiêu $0.08", ""},
		{"hôm nay tiêu $0,1", ""},
		{"tiêu 8.1 cent", ""},
		{"1500 token rồi", ""},
		{"còn $0.92 nữa", ""},
		{"CÒN 92 CENT", ""},
		{"tiêu 9 cent", "9"},
		{"tiêu $0.09", "0.09"},
		{"tiêu 0.5 đô", "0.5"},
		{"được 3 view", "3"},
		{"6 tháng chưa ngủ, 1000000 lần hay hơn bạn", ""},
		{"3 viewer", ""},
	}
	base := opts.Now()
	step := 0
	opts.Now = func() time.Time { return base.Add(time.Duration(step) * time.Minute) }
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		step++
		// Earlier posts must not count as a repeat topic.
		_, _ = st.DB().Exec(`UPDATE actions SET failed=1`)
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"` + c.text + `"}`}, func() (string, error) { return "ok", nil })
		var blocked *guard.BlockedError
		switch {
		case c.refused == "" && err != nil:
			t.Errorf("%q refused: %v", c.text, err)
		case c.refused != "" && (!errors.As(err, &blocked) || blocked.Reason != "ungrounded_number" || blocked.Detail != c.refused):
			t.Errorf("%q: want ungrounded %q, got %v", c.text, c.refused, err)
		}
	}
}

func TestSelfStatUnitsWithoutSymbolPrefix(t *testing.T) {
	_, opts := setup(t)
	opts.Template.Guard.SelfStatUnits = []string{"view"}
	opts.Template.Limits.PostsPerDay, opts.Template.Limits.RepliesPerDay, opts.Template.Limits.RepliesPerUser7d, opts.Template.Limits.MinSecondsBetweenActions = 9, 9, 9, 1
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"5 view"}`}, func() (string, error) { return "", nil })
	if !errors.Is(err, guard.ErrBlocked) {
		t.Fatalf("want ungrounded refusal, got %v", err)
	}
}

func TestRecordedFollowerCountGroundsAFollowerFigure(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Guard.SelfStatUnits = []string{"follower"}
	opts.Template.Limits.PostsPerDay = 100
	opts.Template.Limits.RepliesPerDay = 100
	opts.Template.Limits.RepliesPerUser7d = 100
	opts.Template.Limits.MinSecondsBetweenActions = 1
	base := opts.Now()
	step := 0
	opts.Now = func() time.Time { step++; return base.Add(time.Duration(step) * time.Minute) }
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	post := func(text string) error {
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"` + text + `"}`}, func() (string, error) { return "ok", nil })
		return err
	}
	if err = post("tròn 42 follower"); err == nil {
		t.Fatal("follower figure accepted before any follower count was recorded")
	}
	if err = st.RecordFollowers(context.Background(), "agent", 42, base); err != nil {
		t.Fatal(err)
	}
	if err = post("tròn 42 follower"); err != nil {
		t.Fatalf("recorded follower count refused: %v", err)
	}
	if err = post("tròn 43 follower"); err == nil {
		t.Fatal("wrong follower figure accepted")
	}
	facts, err := guard.FactSheet(context.Background(), st, opts.Template, base)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range facts {
		found = found || (f.ID == "followers" && f.Value == 42)
	}
	if !found {
		t.Fatalf("facts=%v", facts)
	}
}

func TestReplyMayQuoteNumbersOfThePostItAnswers(t *testing.T) {
	_, opts := setup(t)
	opts.Template.Guard.SelfStatUnits = []string{"$"}
	opts.Template.Limits.PostsPerDay, opts.Template.Limits.RepliesPerDay, opts.Template.Limits.RepliesPerUser7d, opts.Template.Limits.MinSecondsBetweenActions = 9, 9, 9, 1
	opts.Template.Limits.BrowserRepliesPerDay = 9
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	const url = "https://www.threads.com/@someone/post/Abc"
	ref := readPost(t, g, url, "", "giá rẻ $8 in / $40 out")
	reply := func(text string) error {
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"` + text + `","target":"` + ref + `"}`}, func() (string, error) { return "ok", nil })
		return err
	}
	if err := reply("$9 would be fairer"); !errors.Is(err, guard.ErrBlocked) {
		t.Fatalf("a number the post lacks must stay ungrounded: %v", err)
	}
	if err := reply("$8 in is fine, $40 out hurts"); err != nil {
		t.Fatalf("quoting the post's numbers refused: %v", err)
	}
}
