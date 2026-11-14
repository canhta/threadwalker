package guard

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/trends"
)

// Open reply candidates: posts read in the last CandidateWindow that the
// account has not answered and that still score at least OpenTargetScore inside
// the template's targets window. This is the one source of "posts worth a
// reply": the data message lists them and the stop check names them.
const (
	CandidateWindow = 6 * time.Hour
	MaxCandidates   = 8
	OpenTargetScore = 0.5
	candidateClip   = 160
)

// Candidate is one open reply target. Text is untrusted.
type Candidate struct {
	Ref        string  `json:"ref"`
	Author     string  `json:"author"`
	AgeMinutes int64   `json:"age_min"`
	Likes      *int64  `json:"likes,omitempty"`
	Replies    *int64  `json:"replies,omitempty"`
	Text       string  `json:"text"`
	Score      float64 `json:"-"`
}

// OpenCandidates lists up to MaxCandidates open reply targets, best target
// score first and newer first among equals. Each post's age is its age at
// read time plus the time since, so a post that grew too old is out of the
// window. own holds the account's own usernames.
func OpenCandidates(ctx context.Context, st *store.Store, t config.Template, own []string, now time.Time) ([]Candidate, error) {
	rows, err := st.SeenPostsSince(ctx, t.ID, now.Add(-CandidateWindow))
	if err != nil {
		return nil, err
	}
	replied, err := st.RepliedSince(ctx, t.ID, t.DryRun, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	answered := map[string]bool{}
	for u := range replied {
		if c, err := store.CanonicalThread(u); err == nil {
			answered[c] = true
		}
	}
	mine := map[string]bool{}
	for _, u := range append(slices.Clone(own), t.Threads.Username) {
		if n, err := store.NormalizeUsername(u); err == nil {
			mine[n] = true
		}
	}
	var out []Candidate
	for _, row := range rows {
		if row.AgeMinutes == nil || answered[row.URL] || mine[row.Author] || !strings.Contains(row.URL, "/post/") {
			continue
		}
		age := *row.AgeMinutes + int64(max(now.Sub(row.ReadAt), 0)/time.Minute)
		facts, err := trends.PostFacts(threads.Post{Likes: row.Likes, Replies: row.Replies, AgeMinutes: &age}, t.Targets)
		if err != nil || facts.Outside != "" || facts.Score < OpenTargetScore {
			continue
		}
		if blocked, err := st.IsDoNotContact(ctx, t.ID, row.Author); err != nil {
			return nil, err
		} else if blocked {
			continue
		}
		text, _ := stdx.TruncateUTF8(strings.Join(strings.Fields(row.Text), " "), candidateClip)
		out = append(out, Candidate{Ref: "p" + strconv.FormatInt(row.ID, 10), Author: row.Author, AgeMinutes: age, Likes: row.Likes, Replies: row.Replies, Text: text, Score: facts.Score})
	}
	slices.SortStableFunc(out, func(a, b Candidate) int { return cmp.Compare(b.Score, a.Score) })
	return out[:min(len(out), MaxCandidates)], nil
}

// IssueCandidates makes the refs of the candidates the data message lists
// refs of this wake, so a reply may target them.
func (g *Guard) IssueCandidates(ctx context.Context, candidates []Candidate) error {
	for _, c := range candidates {
		id, err := strconv.ParseInt(strings.TrimPrefix(c.Ref, "p"), 10, 64)
		if err != nil {
			return err
		}
		row, ok, err := g.opts.Store.SeenPostByID(ctx, g.opts.Template.ID, id)
		if err != nil {
			return err
		}
		if ok {
			g.register(row)
		}
	}
	return nil
}
