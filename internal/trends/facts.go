package trends

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

// ItemFacts is what one item read this wake says in numbers: Line is the compact
// text the ideas prompt shows, Score orders items (best first), and the
// embedded context is what an action built on the item stores. Outside, empty
// for a post inside the reply window, names each targets knob the post breaks
// with its figure and the limit.
type ItemFacts struct {
	PostingContext
	Line    string
	Score   float64
	Outside string
}

// minAgeHours floors the age under a likes-per-hour figure, so a post seconds
// old does not read as a runaway.
const minAgeHours = 10.0 / 60

// PostFacts scores a post read from Threads as a reply target. A post that
// shows no age, likes or replies has no facts.
func PostFacts(p threads.Post, k config.TargetsConfig) (ItemFacts, error) {
	if p.AgeMinutes == nil && p.Likes == nil && p.Replies == nil {
		return ItemFacts{}, nil
	}
	parts := TargetParts{AgeMinutes: -1, Replies: p.Replies, Likes: p.Likes}
	if p.AgeMinutes != nil {
		parts.AgeMinutes = *p.AgeMinutes
		if p.Likes != nil {
			rate := round2(float64(*p.Likes) / max(float64(*p.AgeMinutes)/60, minAgeHours))
			parts.LikesPerHour = &rate
		}
	}
	target := scoreTarget(parts, TierUnknown, k)
	encoded, err := json.Marshal(target)
	if err != nil {
		return ItemFacts{}, fmt.Errorf("encoding reply target score: %w", err)
	}
	line := []string{}
	if p.AgeMinutes != nil {
		line = append(line, formatAge(*p.AgeMinutes)+" old")
	}
	if p.Likes != nil {
		line = append(line, formatCount(*p.Likes)+" likes")
	}
	if p.Replies != nil {
		line = append(line, formatCount(*p.Replies)+" replies")
	}
	line = append(line, fmt.Sprintf("target score %.2f", target.Score))
	if len(target.Outside) > 0 {
		line = append(line, "outside window: "+strings.Join(target.Outside, ", "))
	}
	return ItemFacts{
		PostingContext: PostingContext{TargetScoreJSON: string(encoded), ParentReplies: p.Replies, ParentAgeMinutes: p.AgeMinutes},
		Line:           "[" + strings.Join(line, " · ") + "]",
		Score:          target.Score,
		Outside:        target.outside,
	}, nil
}

// Heat is the evidence of one topic: nil or zero fields are unknown.
type Heat struct {
	// Posts is the topic's post count on Threads.
	Posts *int64
	// Sources is how many distinct sources cover the story this wake.
	Sources int
	// FirstSeen is when a fetch first listed it.
	FirstSeen time.Time
	// Rank is its position in this fetch and PrevRank in the previous one (0
	// first); PrevRank is nil when it was not listed.
	Rank     int
	PrevRank *int
}

const (
	postsHalf = 1000.0
	// freshHalfMinutes is the age at which a story's recency term is 0.5.
	freshHalfMinutes = 360.0
	// rankStep is the heat one rank place of movement adds or removes.
	rankStep = 0.1
)

// TopicFacts normalises a topic's heat to 0..1 as the mean of the terms it has
// evidence for: post volume, coverage by several sources, recency of its first
// sighting and rank movement since the previous fetch. A topic with no evidence
// has no facts.
func TopicFacts(h Heat, now time.Time) ItemFacts {
	var terms []float64
	line := []string{}
	if h.Posts != nil {
		terms = append(terms, unit(float64(*h.Posts), postsHalf))
		line = append(line, formatCount(*h.Posts)+" posts")
	}
	if h.Sources > 0 {
		terms = append(terms, unit(float64(h.Sources-1), 1))
		line = append(line, fmt.Sprintf("%d sources", h.Sources))
	}
	if !h.FirstSeen.IsZero() {
		age := max(0, now.Sub(h.FirstSeen).Minutes())
		terms = append(terms, freshHalfMinutes/(freshHalfMinutes+age))
		line = append(line, "seen "+formatAge(int64(age)))
	}
	if h.PrevRank != nil {
		moved := *h.PrevRank - h.Rank
		terms = append(terms, clamp01(0.5+rankStep*float64(moved)))
		switch {
		case moved > 0:
			line = append(line, fmt.Sprintf("up %d", moved))
		case moved < 0:
			line = append(line, fmt.Sprintf("down %d", -moved))
		}
	}
	if len(terms) == 0 {
		return ItemFacts{}
	}
	sum := 0.0
	for _, term := range terms {
		sum += term
	}
	heat := round2(sum / float64(len(terms)))
	return ItemFacts{
		PostingContext: PostingContext{TopicHeat: &heat},
		Line:           fmt.Sprintf("[topic heat %.2f · %s]", heat, strings.Join(line, " · ")),
		Score:          heat,
	}
}

// StoryHeats reads the history of earlier agent-phase fetches for each story:
// its first sighting and its rank in the latest earlier fetch that listed it.
// A story not listed before is first seen now. Call it before recording the
// current fetch.
func StoryHeats(stories []Story, history []store.TrendStory, now time.Time) []Heat {
	type seen struct {
		first, last time.Time
		rank        int
	}
	byTitle := map[string]*seen{}
	for _, h := range history {
		if h.Phase != "agent" {
			continue
		}
		key := normalizeTrendText(h.Title)
		s := byTitle[key]
		if s == nil {
			s = &seen{first: h.ObservedAt, last: h.ObservedAt, rank: h.Rank}
			byTitle[key] = s
		}
		s.first = minTime(s.first, h.ObservedAt)
		if h.ObservedAt.After(s.last) {
			s.last, s.rank = h.ObservedAt, h.Rank
		}
	}
	out := make([]Heat, len(stories))
	for i, story := range stories {
		out[i] = Heat{Sources: len(story.Sources), FirstSeen: now, Rank: i}
		if s := byTitle[normalizeTrendText(story.Title)]; s != nil {
			out[i].FirstSeen, out[i].PrevRank = s.first, &s.rank
		}
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func formatAge(minutes int64) string {
	switch {
	case minutes < 60:
		return fmt.Sprintf("%dm", minutes)
	case minutes < 24*60:
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dd", minutes/(24*60))
}

func formatCount(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1000000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e3), ".0") + "K"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + "M"
}
