package eval

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
)

// Brief is one write recorded from a live wake: what the agent handed the
// writer, the text it answered, and the most-liked post that wake read.
// Text of other people's posts is empty unless the export kept it.
type Brief struct {
	Wake     int64    `json:"wake"`
	Kind     string   `json:"kind"`
	Angle    string   `json:"angle,omitempty"`
	Fact     string   `json:"fact,omitempty"`
	Hook     string   `json:"hook,omitempty"`
	Rough    []string `json:"rough,omitempty"`
	Parent   string   `json:"parent,omitempty"`
	Recent   []string `json:"recent,omitempty"`
	TopLiked *TopPost `json:"top_liked,omitempty"`
	Recorded []string `json:"recorded,omitempty"` // the last draft the wake's editor judged
}

// TopPost is the most-liked post a wake read.
type TopPost struct {
	Username string `json:"username,omitempty"`
	Likes    int64  `json:"likes"`
	Text     string `json:"text,omitempty"`
}

// BriefsCase replays recorded briefs through the current template's writer and
// critic. It is live only: the models are the template's own.
type BriefsCase struct {
	TemplateDir string  `json:"template_dir"`
	TemplateID  string  `json:"template_id"`
	Briefs      []Brief `json:"briefs"`
	resolvedDir string
}

// resolveDir joins a suite-relative template directory; an absolute one stands.
func resolveDir(suiteDir, dir string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(suiteDir, dir)
}

func validBriefs(b *BriefsCase) bool {
	if b.TemplateDir == "" || !safeID(b.TemplateID) || len(b.Briefs) == 0 || len(b.Briefs) > 200 {
		return false
	}
	for _, x := range b.Briefs {
		if (x.Kind != quality.Post && x.Kind != quality.Reply) || len(x.Rough) == 0 || x.Wake < 0 {
			return false
		}
	}
	return true
}

// BriefReviewer is the writer and critic a briefs run uses, what they have
// spent so far, and how to release them. Tests script it; a live run builds the
// template's own.
type BriefReviewer struct {
	agent.Reviewer
	Spent func() float64
	Close func()
}

// liveBriefReviewer builds the template's own writer and critic over a scratch
// database that records their spend. The template's daily spend cap is set to
// the run's cap so the client itself refuses further requests.
func liveBriefReviewer(ctx context.Context, tmpl config.Template, capUSD float64) (*BriefReviewer, error) {
	dir, err := os.MkdirTemp("", "threadz-briefs-")
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, filepath.Join(dir, "briefs.db"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	now := time.Now()
	tmpl.Limits.MaxUSDPerDay = capUSD
	var wake int64
	if err = st.EnsureAgent(ctx, tmpl.ID); err == nil {
		wake, err = st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	}
	if err != nil {
		st.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	rv := agent.NewReviewer(agent.ReviewerOptions{Store: st, Template: tmpl, Now: time.Now, Redactor: config.NewRedactor(tmpl)}, wake)
	return &BriefReviewer{Reviewer: rv, Close: func() { st.Close(); os.RemoveAll(dir) }, Spent: func() float64 {
		u, _ := st.LLMUsageForDay(context.Background(), tmpl.ID, tmpl.Limits.Day(time.Now()))
		return u.CostUSD
	}}, nil
}

// runBriefsCase replays each recorded brief through quality.Revise, as the
// guard does, and prints a plain text report of each to opts.BriefsOut.
// It publishes nothing; the model calls are the template's own and cost money.
func runBriefsCase(ctx context.Context, c Case, p Profile, repeat int, opts Options, live *reservation) (r CaseResult) {
	r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, EndReason: "completed", Reason: "briefs_setup_failed", ToolCalls: []string{}}
	started := time.Now()
	defer func() { r.DurationMS = float64(time.Since(started).Nanoseconds()) / 1e6 }()
	b := c.Briefs
	_, _, capUSD := effectiveLimits(opts, c.Limits)
	rv := opts.BriefReviewer
	if rv == nil {
		tmpl, err := loadBriefTemplate(b, opts.Getenv)
		if err != nil {
			return r
		}
		if rv, err = liveBriefReviewer(ctx, *tmpl, capUSD); err != nil {
			return r
		}
	}
	if rv.Close != nil {
		defer rv.Close()
	}
	out := opts.BriefsOut
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintf(out, "##### %s (template %s, %d briefs)\n", c.ID, b.TemplateID, len(b.Briefs))
	r.Reason = "replayed"
	spent := func() float64 {
		if rv.Spent == nil {
			return 0
		}
		return rv.Spent()
	}
	for i, x := range b.Briefs {
		if (live != nil && live.used >= live.cap) || spent() >= capUSD {
			r.EndReason, r.Reason = "budget", "spend_cap_reached"
			fmt.Fprintf(out, "\nstopped before brief %d: spend cap reached\n", i+1)
			break
		}
		in := quality.WriterInput{Kind: x.Kind, Angle: x.Angle, Fact: x.Fact, Parent: x.Parent, Draft: x.Rough, Recent: x.Recent}
		tries, ok, err := quality.Revise(ctx, rv.Writer, rv.Critic, nil, in, 2)
		fmt.Fprint(out, FormatBrief(x, tries, ok, err))
		if err != nil {
			r.EndReason, r.Reason = "provider_error", "replay_failed"
		}
	}
	r.CostUSD = spent()
	if live != nil {
		live.used += r.CostUSD
		live.overrun = live.overrun || live.used > live.cap
	}
	fmt.Fprintf(out, "\nspend: $%.4f\n", r.CostUSD)
	r.Passed = r.EndReason == "completed"
	return r
}

func loadBriefTemplate(b *BriefsCase, getenv func(string) string) (*config.Template, error) {
	tmpls, err := config.Load(b.resolvedDir, config.Options{Getenv: getenv})
	if err != nil {
		return nil, err
	}
	for i := range tmpls {
		if tmpls[i].ID == b.TemplateID {
			if tmpls[i].Writer.Model == "" {
				return nil, fmt.Errorf("template %s has no writer", b.TemplateID)
			}
			return &tmpls[i], nil
		}
	}
	return nil, fmt.Errorf("template %s not found", b.TemplateID)
}

// FormatBrief is the plain text report of one brief: the brief, the parent, the
// most-liked post the wake read, then each draft with the critic's verdict.
func FormatBrief(b Brief, tries []quality.Try, ok bool, err error) string {
	var w strings.Builder
	block := func(label, text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		fmt.Fprintf(&w, "%s:\n  %s\n", label, strings.ReplaceAll(text, "\n", "\n  "))
	}
	fmt.Fprintf(&w, "\n=== wake %d %s\n", b.Wake, b.Kind)
	if b.Kind == quality.Reply {
		block("Parent post", b.Parent)
	} else {
		block("Trend", b.Parent)
	}
	if t := b.TopLiked; t != nil {
		block(fmt.Sprintf("Top-liked post read that wake (%d likes, @%s)", t.Likes, t.Username), t.Text)
	}
	block("Angle", b.Angle)
	block("Fact", b.Fact)
	block("Hook", b.Hook)
	block("Rough text", strings.Join(b.Rough, "\n--\n"))
	block("Posted then", strings.Join(b.Recorded, "\n--\n"))
	for i, t := range tries {
		v := t.Verdict
		s := v.Scores
		verdict := "REJECTED"
		if v.Pass {
			verdict = "PASSED"
		}
		fmt.Fprintf(&w, "--- draft %d\n", i+1)
		block("Text", strings.Join(t.Texts, "\n--\n"))
		fmt.Fprintf(&w, "Critic: %s hook=%d vi=%d specific=%d self=%d fit=%d novelty=%d clear=%d\n", verdict, s.Hook, s.NaturalVietnamese, s.Specificity, s.ForcedSelfReference, s.TargetFit, s.Novelty, s.Clarity)
		for _, p := range v.Problems {
			fmt.Fprintf(&w, "  problem: %s\n", p)
		}
	}
	switch {
	case err != nil:
		fmt.Fprintf(&w, "error: %v\n", err)
	case ok:
		w.WriteString("Result: would publish the last draft\n")
	default:
		w.WriteString("Result: would be refused\n")
	}
	return w.String()
}
