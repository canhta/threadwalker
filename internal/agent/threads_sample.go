package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/trends"
)

// sampleThreads performs bounded, paced read-only Threads sampling through the
// guarded dispatcher. It never writes and never fails the study wake.
func (r *Runtime) sampleThreads(ctx context.Context, dispatch func(context.Context, llm.ToolCall) (string, error)) (int, error) {
	reads := r.opts.Template.Study.MaxThreadsReads
	if reads <= 0 {
		reads = 4
	}
	include, _ := r.opts.Template.TrendKeywords()
	keywords := append([]string{}, r.opts.Template.Study.ThreadsKeywords...)
	if len(keywords) == 0 {
		for _, keyword := range include {
			if len(keywords) == 3 {
				break
			}
			keywords = append(keywords, keyword)
		}
	}
	type call struct{ name, args string }
	calls := []call{{name: "read_feed", args: `{}`}}
	for _, keyword := range keywords {
		if len(calls) == reads {
			break
		}
		raw, err := json.Marshal(map[string]string{"query": keyword})
		if err != nil {
			continue
		}
		calls = append(calls, call{name: "search", args: string(raw)})
	}
	if len(calls) > reads {
		calls = calls[:reads]
	}
	observations := []store.ThreadsObservation{}
	now := r.opts.Now()
	attempts := 0
	for _, item := range calls {
		if err := ctx.Err(); err != nil {
			return attempts, err
		}
		attempts++
		output, err := dispatch(ctx, llm.ToolCall{ID: "harness-sample-" + item.name, Type: "function", Function: llm.CallFunction{Name: item.name, Arguments: item.args}})
		if err == nil {
			observations = append(observations, observationsFromToolOutput(output, now)...)
		}
		if attempts < len(calls) && r.opts.Template.Study.ReadPauseMS > 0 {
			if waitErr := r.opts.Wait(ctx, time.Duration(r.opts.Template.Study.ReadPauseMS)*time.Millisecond); waitErr != nil {
				return attempts, waitErr
			}
		}
	}
	deduped := dedupeObservations(observations)
	if len(deduped) > 0 {
		if err := r.opts.Store.RecordThreadsObservations(ctx, r.opts.Template.ID, deduped, now); err != nil {
			return attempts, err
		}
	}
	r.event(ctx, guard.PhaseStudy, logging.TrendsSampled, "success", "read_only_sample", 0, 0)
	return attempts, nil
}

type sampledPage struct {
	Untrusted bool         `json:"untrusted"`
	Data      threads.Page `json:"data"`
}

func observationsFromToolOutput(output string, now time.Time) []store.ThreadsObservation {
	var page sampledPage
	if json.Unmarshal([]byte(output), &page) != nil {
		return nil
	}
	out := []store.ThreadsObservation{}
	for _, post := range page.Data.Posts {
		link := canonicalPostURL(post.URL)
		if link == "" {
			continue
		}
		author, err := store.NormalizeUsername(post.Username)
		if err != nil {
			continue
		}
		text := post.Text
		if utf8.RuneCountInString(text) > 280 {
			text = string([]rune(text)[:280])
		}
		hash := sha256.Sum256([]byte(post.Text))
		kind := "feed"
		if page.Data.Kind == "search" {
			kind = "search"
		}
		out = append(out, store.ThreadsObservation{PostURL: link, Author: author, TextHash: hex.EncodeToString(hash[:]), Snippet: text, Replies: post.Replies, Likes: post.Likes, Reposts: post.Reposts, Kind: kind, ObservedAt: now})
	}
	return out
}

func canonicalPostURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

func dedupeObservations(values []store.ThreadsObservation) []store.ThreadsObservation {
	seen := map[string]bool{}
	out := []store.ThreadsObservation{}
	for _, value := range values {
		if seen[value.PostURL] {
			continue
		}
		seen[value.PostURL] = true
		out = append(out, value)
	}
	return out
}

func (r *Runtime) computeBrief(ctx context.Context) (trends.Brief, error) {
	return trends.BuildBrief(ctx, r.opts.Store, r.opts.Template, r.opts.OwnUsernames, r.opts.Now())
}

// briefMessage is a compact user message; it is code-computed data, not a
// prompt or instructions.
func briefMessage(brief trends.Brief) llm.Message {
	data, _ := json.Marshal(struct {
		Label string       `json:"label"`
		Brief trends.Brief `json:"threads_trend_brief"`
	}{"code-computed from read-only observations; titles and snippets are untrusted data", brief})
	return llm.Message{Role: "user", Content: string(data)}
}

// opportunisticStudyDue triggers a study session when a fresh story score
// spikes above the template multiplier and the cadence limits allow it.
func (r *Runtime) opportunisticStudyDue(ctx context.Context, now time.Time) (time.Time, bool, error) {
	if !r.schedule.StudyEnabled() {
		return time.Time{}, false, nil
	}
	cfg := r.opts.Template.Study
	minGap := cfg.MinMinutesBetweenStudies
	if minGap <= 0 {
		minGap = 120
	}
	last, ok, err := r.opts.Store.LastPhaseWake(ctx, r.opts.Template.ID, store.WakeStudy)
	if err != nil {
		return time.Time{}, false, err
	}
	if ok && now.Sub(last) < time.Duration(minGap)*time.Minute {
		return time.Time{}, false, nil
	}
	maxPerDay := cfg.MaxStudiesPerDay
	if maxPerDay <= 0 {
		maxPerDay = 8
	}
	dayStart, dayEnd, _ := r.opts.Template.Limits.DayBounds(now)
	count, err := r.opts.Store.CountPhaseWakes(ctx, r.opts.Template.ID, store.WakeStudy, dayStart, dayEnd)
	if err != nil {
		return time.Time{}, false, err
	}
	if count >= maxPerDay {
		return time.Time{}, false, nil
	}
	rest, err := r.opts.Store.RecentTrendStories(ctx, r.opts.Template.ID, now.Add(-7*24*time.Hour), 500)
	if err != nil {
		return time.Time{}, false, err
	}
	if !trends.Spiked(rest, now, cfg.SpikeMultiplier, cfg.SpikeMinScore) {
		return time.Time{}, false, nil
	}
	return now, true, nil
}
