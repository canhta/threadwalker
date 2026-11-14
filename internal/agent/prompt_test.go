package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/llm"
)

// The system prompt is the cacheable prefix: the same template sends the same
// bytes on every step of a wake and on every wake, whatever the state, the
// tools refused or the time.
func TestSystemPromptIsByteIdenticalAcrossStepsAndWakes(t *testing.T) {
	r, st, fixture := kpiRuntime(t, time.Hour, 12)
	var systems []string
	fixture.Observe = func(m []llm.Message) {
		if strings.HasPrefix(m[0].Content, "You are Synthetic fixture") { // the agent's steps; the writer and critic have their own prompts
			systems = append(systems, m[0].Content)
		}
	}
	if _, err := r.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The first wake published and spent; now the numbers change and an owner note is open.
	if _, err := st.DB().Exec(`UPDATE metrics SET likes=0`); err != nil {
		t.Fatal(err)
	}
	if err := st.OwnerNote(context.Background(), []string{"synthetic-offline"}, "k", "answer the AP story", kpiNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	next, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	systems = append(systems, next[0].Content)
	if len(systems) < 3 {
		t.Fatalf("only %d model requests seen", len(systems))
	}
	for i, s := range systems {
		if s != systems[0] {
			t.Fatalf("request %d sent a different system prompt:\n%s\n---\n%s", i, systems[0], s)
		}
	}
}
