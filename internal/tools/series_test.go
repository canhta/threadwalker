package tools_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func seriesHarness(t *testing.T, waits *[]time.Duration) *browserHarness {
	t.Helper()
	h := newBrowserHarness(t, false, func(o *tools.Options) {
		o.Template.DryRun = false
		o.Template.Limits.PostsPerDay = 10
		o.Template.Limits.RepliesPerDay = 10
		o.Template.Limits.RepliesPerUser7d = 3
		o.Template.Limits.MinSecondsBetweenActions = 1
		o.Template.Limits.MinMinutesBetweenWakes = 15
		o.Wait = func(_ context.Context, d time.Duration) error { *waits = append(*waits, d); return nil }
	})
	h.backend.Results[threads.OpPost] = "https://www.threads.com/@me/post/P1"
	h.backend.Results[threads.OpReply] = "https://www.threads.com/@me/post/R2"
	return h
}

func partRows(t *testing.T, h *browserHarness) (urls []string) {
	t.Helper()
	rows, err := h.store.DB().Query(`SELECT thread_url FROM action_parts ORDER BY part`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err = rows.Scan(&u); err != nil {
			t.Fatal(err)
		}
		urls = append(urls, u)
	}
	return urls
}

func TestSeriesPublishesEachPartAsAReplyToThePreviousOne(t *testing.T) {
	var waits []time.Duration
	h := seriesHarness(t, &waits)
	h.mustCall("post", `{"text":"một","parts":["hai","ba"],"hook":"number"}`)
	var replies []threads.Call
	for _, c := range h.backend.Calls() {
		if c.Op == threads.OpReply {
			replies = append(replies, c)
		}
	}
	if len(replies) != 2 || replies[0].Arg != "P1" || replies[0].Text != "hai" || replies[1].Arg != "R2" || replies[1].Text != "ba" {
		t.Fatalf("part calls=%+v, want part 2 under P1 and part 3 under part 2", replies)
	}
	if got := countActions(t, h); got != 1 {
		t.Fatalf("actions=%d, want the series to be one action", got)
	}
	if urls := partRows(t, h); len(urls) != 2 || urls[0] == "" || urls[1] == "" {
		t.Fatalf("part rows=%v, want both parts recorded with their permalinks", urls)
	}
	if len(waits) != 2 {
		t.Fatalf("waits=%v, want the injected wait before each later part", waits)
	}
}

func TestSeriesPartFailureKeepsPublishedPartsAndEndsTheWake(t *testing.T) {
	var waits []time.Duration
	h := seriesHarness(t, &waits)
	replies := 0
	h.backend.Hook = func(_ context.Context, c threads.Call) error {
		if c.Op == threads.OpReply {
			if replies++; replies == 2 {
				return errors.New("part refused")
			}
		}
		return nil
	}
	h.next()
	_, err := h.call("post", `{"text":"một","parts":["hai","ba","bốn"]}`)
	if err == nil || !isTerminalToolError(err) {
		t.Fatalf("err=%v, want a terminal stop", err)
	}
	if urls := partRows(t, h); len(urls) != 3 || urls[0] == "" || urls[1] != "" || urls[2] != "" {
		t.Fatalf("part rows=%v, want part 2 recorded and parts 3 and 4 empty", urls)
	}
	if h.backend.Count(threads.OpReply) != 2 || h.backend.Count(threads.OpPost) != 1 {
		t.Fatalf("calls=%+v, want no retry of the failed part", h.backend.Calls())
	}
	if _, err = h.call("post", `{"text":"again"}`); err == nil || h.backend.Count(threads.OpPost) != 1 {
		t.Fatalf("a later post after a failed series must be refused, err=%v", err)
	}
}

func TestPostIsAnchoredToATrendReadThisWake(t *testing.T) {
	h := newBrowserHarness(t, true, func(o *tools.Options) {
		o.Template.Guard.TrendAnchor = true
		o.Template.Limits.PostsPerDay = 10
		o.Template.Limits.MinSecondsBetweenActions = 1
		o.Template.Limits.MinMinutesBetweenWakes = 15
		o.Template.Limits.RepliesPerDay = 10
		o.Template.Limits.RepliesPerUser7d = 3
	})
	args := `{"text":"giá mới 3 đô\nvà thế là xong","hook":"number","rationale":"bám giá mới, hook là con số, dev sẽ cãi","trend_ref":"https://example.test/trend"}`
	h.next()
	if _, err := h.call("post", args); err == nil || countActions(t, h) != 0 {
		t.Fatalf("err=%v actions=%d, want a post before any trend read refused", err, countActions(t, h))
	}
	h.mustCall("fetch_trends", `{}`)
	h.mustCall("post", args)
	var hook, rationale string
	if err := h.store.DB().QueryRow(`SELECT hook,rationale FROM actions`).Scan(&hook, &rationale); err != nil || hook != "number" || rationale == "" {
		t.Fatalf("hook=%q rationale=%q err=%v, want both stored on the action", hook, rationale, err)
	}
}

func TestRecentSearchAndProfileLookup(t *testing.T) {
	h := newBrowserHarness(t, true)
	followers := int64(900)
	h.backend.Profiles = map[string]threads.Profile{"big_one": {Username: "big_one", Followers: &followers}}
	h.mustCall("search", `{"query":"vibe code","recent":true}`)
	out := h.mustCall("profile", `{"username":"big_one"}`)
	if got := decodeBrowserEnvelope(t, out); got.Source != "profile" {
		t.Fatalf("profile source=%q", got.Source)
	}
	if h.backend.Last(threads.OpProfile).Arg != "big_one" {
		t.Fatalf("calls=%+v", h.backend.Calls())
	}
}
