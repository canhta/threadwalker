package trends

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
)

// Window says where a story is in its life.
type Window string

const (
	WindowEarly Window = "early"
	WindowPeak  Window = "peak"
	WindowLate  Window = "late"
)

const (
	earlySaturation = 2
	lateSaturation  = 8
	// externalScoreHalf and repliesHalf are the values at which a story score or
	// a reply velocity contributes half of its heat weight.
	externalScoreHalf = 10.0
	repliesHalf       = 2.0
)

// StorySignal is the code-computed life-cycle of one story or keyword.
type StorySignal struct {
	FirstSeen    time.Time `json:"first_seen"`
	AgeMinutes   int64     `json:"age_minutes"`
	Velocity     float64   `json:"velocity_per_hour"`
	Acceleration float64   `json:"acceleration_per_hour2"`
	// Saturation counts distinct Threads posts already on the story; SameAngle is
	// the most posts that share one bigram outside the story's own words.
	Saturation  int     `json:"saturation_posts"`
	SameAngle   int     `json:"same_angle_posts"`
	Heat        float64 `json:"heat"`
	Window      Window  `json:"window"`
	Recommended bool    `json:"recommended"`
}

type sample struct {
	At    time.Time
	Value float64
}

// rates returns velocity (per hour, newest pair) and acceleration (second
// difference over the newest three samples). Fewer samples give zero.
func rates(samples []sample) (velocity, acceleration float64) {
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
	n := len(samples)
	if n < 2 {
		return 0, 0
	}
	h2 := samples[n-1].At.Sub(samples[n-2].At).Hours()
	if h2 <= 0 {
		return 0, 0
	}
	velocity = (samples[n-1].Value - samples[n-2].Value) / h2
	if n < 3 {
		return velocity, 0
	}
	h1 := samples[n-2].At.Sub(samples[n-3].At).Hours()
	if h1 <= 0 {
		return velocity, 0
	}
	previous := (samples[n-2].Value - samples[n-3].Value) / h1
	return velocity, (velocity - previous) / ((h1 + h2) / 2)
}

func windowOf(saturation int, velocity, acceleration float64) Window {
	switch {
	case saturation >= lateSaturation || velocity < 0:
		return WindowLate
	case saturation <= earlySaturation && acceleration >= 0:
		return WindowEarly
	}
	return WindowPeak
}

func unit(x, half float64) float64 {
	if x <= 0 {
		return 0
	}
	return x / (x + half)
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// signalOf computes the signal of a story or keyword from its samples, first
// sighting and saturation.
func signalOf(samples []sample, first time.Time, level, half float64, saturation, sameAngle int, now time.Time) StorySignal {
	velocity, acceleration := rates(samples)
	window := windowOf(saturation, velocity, acceleration)
	factor := map[Window]float64{WindowEarly: 1, WindowPeak: 0.8, WindowLate: 0.3}[window]
	heat := (0.5*unit(level, half) + 0.5*unit(velocity, repliesHalf)) * factor
	return StorySignal{
		FirstSeen: first, AgeMinutes: int64(now.Sub(first).Minutes()),
		Velocity: round2(velocity), Acceleration: round2(acceleration),
		Saturation: saturation, SameAngle: sameAngle,
		Heat: round2(heat), Window: window, Recommended: window != WindowLate,
	}
}

// saturationOf counts the posts that share at least two title tokens, and the
// most of them that share one bigram not made of title tokens alone.
func saturationOf(titleTokens []string, posts []string) (saturation, sameAngle int) {
	title := map[string]bool{}
	for _, token := range titleTokens {
		title[token] = true
	}
	perBigram := map[string]int{}
	for _, post := range posts {
		tokens := config.TopicTokens(post)
		shared := 0
		for _, token := range tokens {
			if title[token] {
				shared++
			}
		}
		if shared < 2 {
			continue
		}
		saturation++
		for bigram := range bigrams(tokens) {
			if !title[bigram.a] || !title[bigram.b] {
				perBigram[bigram.a+" "+bigram.b]++
			}
		}
	}
	for _, n := range perBigram {
		if n > sameAngle {
			sameAngle = n
		}
	}
	return saturation, sameAngle
}

type bigram struct{ a, b string }

func bigrams(tokens []string) map[bigram]bool {
	set := map[bigram]bool{}
	for i := 1; i < len(tokens); i++ {
		set[bigram{tokens[i-1], tokens[i]}] = true
	}
	return set
}

// TargetParts are the numeric reasons a post is worth a reply. They hold no
// text and no author data.
type TargetParts struct {
	ReplyVelocity  float64 `json:"reply_velocity"`
	AgeMinutes     int64   `json:"age_minutes"`
	OnTopic        bool    `json:"on_topic"`
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

// sizeTier prefers authors 2 to 10 times our size. Missing counts give
// TierUnknown, which scores neutral.
func sizeTier(author, own int64) string {
	if author <= 0 || own <= 0 {
		return TierUnknown
	}
	ratio := float64(author) / float64(own)
	switch {
	case ratio < 2:
		return TierNear
	case ratio <= 10:
		return TierTarget
	}
	return TierMega
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// scoreTarget averages, each in 0..1: freshness against targets.max_age_minutes
// (neutral when the age is unknown), room in the replies against
// targets.max_replies (when known), momentum (likes per hour against
// targets.min_likes_per_hour, else observed reply velocity), size tier, topic
// match and novelty. A post that breaks a knob is flagged in Outside and scores
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
	topic := 0.0
	if p.OnTopic {
		topic = 1
	}
	terms = append(terms, momentum, size, topic, 1/float64(1+p.JokeSaturation))
	sum := 0.0
	for _, term := range terms {
		sum += term
	}
	return TargetScore{Score: round2(sum / float64(len(terms))), TargetParts: p}
}
