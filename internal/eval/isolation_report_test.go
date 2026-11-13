package eval

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/canhta/threadz/internal/agent"
)

func TestAgentScopedReportMetadataRoundTripsAndRejectsPayloadIDs(t *testing.T) {
	r := reportFixture()
	r.Results[0].Agents = []agent.IsolationAgentEvidence{{AgentID: "synthetic-a", TokensIn: 10, CostUSD: 0.01}, {AgentID: "synthetic-b", BudgetStops: 2}}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := WriteReport(path, r); err != nil {
		t.Fatal(err)
	}
	read, err := ReadReport(path)
	if err != nil || len(read.Results[0].Agents) != 2 || read.Results[0].Agents[1].BudgetStops != 2 {
		t.Fatalf("scope roundtrip: %+v %v", read, err)
	}
	for _, bad := range []agent.IsolationAgentEvidence{{AgentID: "payload\nusername"}, {AgentID: "synthetic-a", CostUSD: math.NaN()}, {AgentID: "synthetic-a", TokensIn: -1}, {AgentID: "synthetic-a", KilledSkips: -1}} {
		r.Results[0].Agents = []agent.IsolationAgentEvidence{bad}
		if err = WriteReport(path, r); err == nil {
			t.Fatal("invalid agent metadata accepted")
		}
	}
	r.Results[0].Agents = []agent.IsolationAgentEvidence{{AgentID: "duplicate"}, {AgentID: "duplicate"}}
	if err = WriteReport(path, r); err == nil {
		t.Fatal("ambiguous agent scope accepted")
	}
}
