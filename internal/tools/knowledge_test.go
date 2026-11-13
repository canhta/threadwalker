package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/knowledge"
	"github.com/canhta/threadz/internal/store"
)

func TestKnowledgeReadInEveryPhase(t *testing.T) {
	all, _ := knowledge.All()
	for _, phase := range []string{guard.PhaseAgent, guard.PhaseDream, guard.PhaseStudy} {
		t.Run(phase, func(t *testing.T) {
			d := phaseDispatcher(t, phase)
			var got []knowledge.Entry
			if err := json.Unmarshal([]byte(dispatch(t, d, "knowledge_read", `{}`)), &got); err != nil || len(got) != len(all) {
				t.Fatalf("unfiltered read: %d entries, %v", len(got), err)
			}
			got = nil
			if err := json.Unmarshal([]byte(dispatch(t, d, "knowledge_read", `{"scope":"vietnam","evidence":"practitioner"}`)), &got); err != nil || len(got) == 0 {
				t.Fatalf("filtered read: %d entries, %v", len(got), err)
			}
			for _, e := range got {
				if e.Scope != "vietnam" || e.Evidence != "practitioner" || e.SourceURL == "" {
					t.Fatalf("filter leaked %+v", e)
				}
			}
			if _, err := d.Dispatch(context.Background(), call("knowledge_read", `{"scope":"mars"}`)); err == nil {
				t.Fatal("unknown scope accepted")
			}
		})
	}
}

func TestKnowledgeIDRoundTripsOntoHypothesis(t *testing.T) {
	d := phaseDispatcher(t, guard.PhaseDream)
	var entry store.PlaybookEntry
	if err := json.Unmarshal([]byte(dispatch(t, d, "playbook_propose", `{"text":"tag first","knowledge_id":"one-topic-tag"}`)), &entry); err != nil || entry.KnowledgeID != "one-topic-tag" {
		t.Fatalf("propose = %+v, %v", entry, err)
	}
	if out := dispatch(t, d, "playbook_read", `{}`); !strings.Contains(out, `"knowledge_id":"one-topic-tag"`) {
		t.Fatalf("playbook_read lost the citation: %s", out)
	}
	if _, err := d.Dispatch(context.Background(), call("playbook_propose", `{"text":"x","knowledge_id":"made-up"}`)); err == nil {
		t.Fatal("unknown knowledge_id accepted")
	}
}

func TestCandidateStudyOnlyAndNeverChangesCuratedFile(t *testing.T) {
	before, _ := knowledge.All()
	for _, phase := range []string{guard.PhaseAgent, guard.PhaseDream} {
		d := phaseDispatcher(t, phase)
		if _, err := d.Dispatch(context.Background(), call("knowledge_candidate", `{"technique":"t","url":"https://x.test/a"}`)); err == nil {
			t.Fatalf("candidate allowed in %s", phase)
		}
	}
	d := phaseDispatcher(t, guard.PhaseStudy)
	dispatch(t, d, "knowledge_candidate", `{"technique":"post a thread recap on Fridays","url":"https://x.test/a"}`)
	for name, args := range map[string]string{
		"duplicate url": `{"technique":"again","url":"https://x.test/a"}`,
		"bad url":       `{"technique":"t","url":"not a url"}`,
	} {
		if _, err := d.Dispatch(context.Background(), call("knowledge_candidate", args)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	after, _ := knowledge.All()
	if len(after) != len(before) {
		t.Fatalf("curated file changed: %d -> %d entries", len(before), len(after))
	}
	var seen []knowledge.Entry
	_ = json.Unmarshal([]byte(dispatch(t, d, "knowledge_read", `{}`)), &seen)
	for _, e := range seen {
		if strings.Contains(e.Claim, "Fridays") {
			t.Fatal("candidate appeared in knowledge_read")
		}
	}
}
