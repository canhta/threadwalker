package agent

import (
	"context"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
)

const (
	// exampleTextRunes bounds an example's text; a post is at most 300 runes.
	exampleTextRunes = 300
	exampleWindow    = 48 * time.Hour
)

// episodeView is an episode as the model reads it. Kind, hook, angle, fact,
// window and band are shown to the dream only: in a wake's examples the list an
// episode sits in is its band and its numbers are the newest window's.
type episodeView struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind,omitempty"`
	Text          string `json:"text"`
	Hook          string `json:"hook,omitempty"`
	Angle         string `json:"angle,omitempty"`
	Fact          string `json:"fact,omitempty"`
	Window        int    `json:"window_h,omitempty"`
	Views         int64  `json:"views"`
	Likes         int64  `json:"likes"`
	Replies       int64  `json:"replies"`
	Band          string `json:"band,omitempty"`
	ParentReplies *int64 `json:"parent_replies,omitempty"`
	ParentAgeMin  *int64 `json:"parent_age_min,omitempty"`
}

func viewOf(e store.Episode, redactor *config.Redactor, full bool) episodeView {
	v := episodeView{ID: e.ActionID, Text: stdx.CapRunes(redactor.String(e.Text), exampleTextRunes), Views: e.Views, Likes: e.Likes, Replies: e.Replies, ParentReplies: e.ParentReplies, ParentAgeMin: e.ParentAgeMinutes}
	if full {
		v.Kind, v.Hook, v.Angle, v.Fact = e.Kind, redactor.String(e.Hook), redactor.String(e.Angle), redactor.String(e.Fact)
		v.Window, v.Band = e.Window, e.Band
	}
	return v
}

// kindExamples is the best and worst episodes of one kind, best first.
type kindExamples struct {
	Top    []episodeView `json:"top"`
	Bottom []episodeView `json:"bottom"`
}

// examples holds both kinds; its field order is the JSON order.
type examples struct {
	Post  kindExamples `json:"post"`
	Reply kindExamples `json:"reply"`
}

// topBottom picks up to n episodes of a kind in the top band, best first, and
// up to n in the bottom band, worst first.
func topBottom(eps []store.Episode, kind string, n int, redactor *config.Redactor, full bool) kindExamples {
	of := eps[:0:0]
	for _, e := range eps {
		if e.Kind == kind {
			of = append(of, e)
		}
	}
	out := kindExamples{Top: []episodeView{}, Bottom: []episodeView{}}
	for _, e := range store.Best(of, len(of)) {
		if e.Band == store.BandTop && len(out.Top) < n {
			out.Top = append(out.Top, viewOf(e, redactor, full))
		}
	}
	for _, e := range store.Worst(of, len(of)) {
		if e.Band == store.BandBottom && len(out.Bottom) < n {
			out.Bottom = append(out.Bottom, viewOf(e, redactor, full))
		}
	}
	return out
}

func topBottomBoth(eps []store.Episode, n int, redactor *config.Redactor, full bool) examples {
	return examples{Post: topBottom(eps, "post", n, redactor, full), Reply: topBottom(eps, "reply", n, redactor, full)}
}

// recentExamples reads the agent's best and worst episodes of the last 48 hours.
func recentExamples(ctx context.Context, st *store.Store, agentID string, now time.Time, redactor *config.Redactor) (examples, error) {
	eps, err := st.Episodes(ctx, agentID, store.EpisodeQuery{Since: now.Add(-exampleWindow)})
	if err != nil {
		return examples{}, err
	}
	return topBottomBoth(eps, 3, redactor, false), nil
}

// writerExamples are the best and worst episodes of one kind from the last 48
// hours, best first, for the writer's prompt.
func writerExamples(ctx context.Context, st *store.Store, agentID string, now time.Time, kind string, redactor *config.Redactor) ([]quality.Example, error) {
	all, err := recentExamples(ctx, st, agentID, now, redactor)
	if err != nil {
		return nil, err
	}
	picked := all.Post
	if kind == quality.Reply {
		picked = all.Reply
	}
	var out []quality.Example
	add := func(band string, views []episodeView) {
		for _, v := range views {
			out = append(out, quality.Example{Text: v.Text, Band: band, Views: v.Views, Likes: v.Likes, Replies: v.Replies, ParentReplies: v.ParentReplies, ParentAgeMin: v.ParentAgeMin})
		}
	}
	add(store.BandTop, picked.Top)
	add(store.BandBottom, picked.Bottom)
	return out, nil
}
