package eval

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/quality"
)

const briefsSuiteJSON = `{"cases":[{"id":"briefs_wake_7","kind":"briefs","expect":{"end_reason":"completed"},"briefs":{
  "template_dir":"templates","template_id":"viral","briefs":[
  {"wake":7,"kind":"reply","angle":"góc nhìn","fact":"vé tăng gấp đôi","hook":"contrast","rough":["bản nháp"],"parent":"vé concert tăng giá","recent":["bài cũ"],
   "top_liked":{"username":"someone","likes":120,"text":"bài nhiều like nhất"},"recorded":["đã đăng hôm đó"]},
  {"wake":8,"kind":"post","rough":["nháp hai"]}]}}]}`

func scriptedBriefReviewer() *BriefReviewer {
	var seen []quality.WriterInput
	return &BriefReviewer{Reviewer: agent.Reviewer{
		Writer: func(_ context.Context, in quality.WriterInput) ([]string, error) {
			seen = append(seen, in)
			if len(in.Problems) == 0 {
				return []string{"bản một"}, nil
			}
			return []string{"bản hai"}, nil
		},
		Critic: func(_ context.Context, in quality.CriticInput) (quality.CriticVerdict, error) {
			s := quality.CriticScores{Hook: 3, NaturalVietnamese: 4, Specificity: 3, ForcedSelfReference: 5, TargetFit: 4, Novelty: 3, Clarity: 5}
			if in.Texts[0] == "bản một" {
				return quality.CriticVerdict{Problems: []string{"quá cứng"}, Scores: s}, nil
			}
			return quality.CriticVerdict{Pass: true, Scores: s}, nil
		},
	}, Spent: func() float64 { return 0.0123 }}
}

func TestBriefsSuiteLoadsAndReplaysThroughReviseWithAReadableReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live-briefs.json")
	if err := os.WriteFile(path, []byte(briefsSuiteJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	suite, err := LoadSuite(path)
	if err != nil {
		t.Fatal(err)
	}
	c := suite.Cases[0]
	if c.Briefs.resolvedDir != filepath.Join(filepath.Dir(path), "templates") || len(c.Briefs.Briefs) != 2 {
		t.Fatalf("loaded %+v", c.Briefs)
	}
	var out bytes.Buffer
	opts := Options{Mode: "live", MaxUSD: 1, BriefReviewer: scriptedBriefReviewer(), BriefsOut: &out}
	r := runBriefsCase(context.Background(), c, offlineProfile("openai"), 1, opts, nil)
	if !r.Passed || r.CostUSD != 0.0123 {
		t.Fatalf("result %+v", r)
	}
	for _, want := range []string{
		"=== wake 7 reply", "Parent post:\n  vé concert tăng giá", "Top-liked post read that wake (120 likes, @someone):\n  bài nhiều like nhất",
		"Angle:\n  góc nhìn", "Posted then:\n  đã đăng hôm đó",
		"--- draft 1", "Critic: REJECTED", "problem: quá cứng", "--- draft 2", "Critic: PASSED", "Result: would publish the last draft",
		"=== wake 8 post", "spend: $0.0123",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(strings.SplitN(out.String(), "=== wake 8", 2)[1], "Top-liked") {
		t.Error("a brief without a top-liked post printed one")
	}
}

func TestBriefsRunStopsAtTheSpendCapAndOfflineRefusesThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live-briefs.json")
	if err := os.WriteFile(path, []byte(briefsSuiteJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	suite, err := LoadSuite(path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rv := scriptedBriefReviewer()
	rv.Spent = func() float64 { return 2 }
	r := runBriefsCase(context.Background(), suite.Cases[0], offlineProfile("openai"), 1, Options{Mode: "live", MaxUSD: 1, BriefReviewer: rv, BriefsOut: &out}, nil)
	if r.Passed || r.EndReason != "budget" || strings.Contains(out.String(), "=== wake") {
		t.Fatalf("%+v\n%s", r, out.String())
	}
	if _, err = Run(context.Background(), suite, Options{}); err == nil {
		t.Fatal("an offline run accepted a briefs case")
	}
}
