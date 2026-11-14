package trends

import (
	"fmt"
	"math"
	"strings"

	"github.com/canhta/threadz/internal/config"
)

func unit(x, half float64) float64 {
	if x <= 0 {
		return 0
	}
	return x / (x + half)
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// TargetParts are the numeric reasons a post is worth a reply. They hold no
// text and no author data.
type TargetParts struct {
	ReplyVelocity  float64 `json:"reply_velocity"`
	AgeMinutes     int64   `json:"age_minutes"`
	AlreadyReplied bool    `json:"already_replied"`
	// JokeSaturation counts other observed posts that share a bigram with the
	// parent's snippet; reply text is not observed.
	JokeSaturation int `json:"joke_saturation"`
	// Replies, Likes and LikesPerHour are nil when not observed. Outside names
	// the knobs of the template's targets block the post breaks: "age",
	// "replies", "likes_per_hour", "likes". AgeMinutes is -1 when unknown.
	Replies      *int64   `json:"replies,omitempty"`
	Likes        *int64   `json:"likes,omitempty"`
	LikesPerHour *float64 `json:"likes_per_hour,omitempty"`
	Outside      []string `json:"outside_window,omitempty"`
}

// Outside reasons: the targets knob a post breaks.
const (
	outsideAge          = "age"
	outsideReplies      = "replies"
	outsideLikesPerHour = "likes_per_hour"
	outsideLikes        = "likes"
)

// TargetScore is a post's reply-target score with its parts. outside names
// each broken knob with the post's figure and the limit.
type TargetScore struct {
	Score float64 `json:"score"`
	TargetParts
	outside string
}

// Size tiers of an author relative to our follower count.
const (
	TierUnknown = "unknown"
	TierNear    = "near"
	TierTarget  = "target"
	TierMega    = "mega"
)

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// scoreTarget averages, each in 0..1: freshness against targets.max_age_minutes
// (neutral when the age is unknown), room in the replies against
// targets.max_replies (when known), momentum (likes per hour against
// targets.min_likes_per_hour, else observed reply velocity), size tier
// and novelty. A post that breaks a knob is flagged in Outside and scores
// 0; it is never removed.
func scoreTarget(p TargetParts, tier string, k config.TargetsConfig) TargetScore {
	if p.AlreadyReplied {
		return TargetScore{TargetParts: p}
	}
	p.Outside = nil
	var broken []string
	outside := func(knob, figure string) {
		p.Outside = append(p.Outside, knob)
		broken = append(broken, figure)
	}
	recency := 0.5
	if p.AgeMinutes >= 0 {
		recency = clamp01(1 - float64(p.AgeMinutes)/float64(k.MaxAgeMinutes))
		if p.AgeMinutes > int64(k.MaxAgeMinutes) {
			outside(outsideAge, fmt.Sprintf("age %dm > max_age_minutes %d", p.AgeMinutes, k.MaxAgeMinutes))
		}
	}
	terms := []float64{recency}
	if p.Replies != nil {
		terms = append(terms, clamp01(1-float64(*p.Replies)/float64(k.MaxReplies)))
		if *p.Replies > int64(k.MaxReplies) {
			outside(outsideReplies, fmt.Sprintf("replies %d > max_replies %d", *p.Replies, k.MaxReplies))
		}
	}
	momentum := unit(p.ReplyVelocity, 3)
	if p.LikesPerHour != nil {
		momentum = unit(*p.LikesPerHour, k.MinLikesPerHour)
		if *p.LikesPerHour < k.MinLikesPerHour {
			outside(outsideLikesPerHour, fmt.Sprintf("likes_per_hour %g < min_likes_per_hour %g", *p.LikesPerHour, k.MinLikesPerHour))
		}
	}
	if p.Likes != nil && *p.Likes < int64(k.MinLikes) {
		outside(outsideLikes, fmt.Sprintf("likes %d < min_likes %d", *p.Likes, k.MinLikes))
	}
	if len(broken) > 0 {
		return TargetScore{TargetParts: p, outside: strings.Join(broken, ", ")}
	}
	size := map[string]float64{TierUnknown: 0.5, TierNear: 0.6, TierTarget: 1, TierMega: 0.2}[tier]
	terms = append(terms, momentum, size, 1/float64(1+p.JokeSaturation))
	sum := 0.0
	for _, term := range terms {
		sum += term
	}
	return TargetScore{Score: round2(sum / float64(len(terms))), TargetParts: p}
}
