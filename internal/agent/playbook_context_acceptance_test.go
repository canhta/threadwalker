package agent_test

// Independent acceptance suite for the playbook wake context. It uses the
// public runtime/context seam and the published SQLite playbook schema.

import (
	"context"
	"encoding/json"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

var playbookContextAt = time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

type playbookContextEnvelope struct {
	Playbook []store.PlaybookEntry `json:"playbook"`
}

func playbookContextRuntime(t *testing.T, mutate ...func(*agent.Options)) (*store.Store, agent.Options, *agent.Runtime) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "playbook-context.db"))
	if err != nil {
		t.Fatalf("opening playbook context store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	tmpl := agent.OfflineTemplate()
	opts := agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return playbookContextAt }}
	for _, change := range mutate {
		change(&opts)
	}
	r, err := agent.New(opts)
	if err != nil {
		t.Fatalf("building runtime: %v", err)
	}
	// Ensure the agent row without running startup seeding, so these tests
	// control the exact playbook rows the context must deliver.
	if err := st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatalf("ensuring context agent: %v", err)
	}
	return st, opts, r
}

func playbookContextMessages(t *testing.T, r *agent.Runtime) []byte {
	t.Helper()
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatalf("building context: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
		t.Fatalf("context messages = %+v", messages)
	}
	return []byte(messages[1].Content)
}

func decodePlaybookContext(t *testing.T, raw []byte) playbookContextEnvelope {
	t.Helper()
	var envelope playbookContextEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decoding context %s: %v", raw, err)
	}
	return envelope
}

func TestPlaybookContextCarriesOnlySelectedAgentsBoundedEntries(t *testing.T) {
	st, opts, r := playbookContextRuntime(t)
	tmpl := opts.Template
	if _, err := st.InitializePlaybook(context.Background(), tmpl.ID, []string{"own testing one", "own testing two"}, playbookContextAt); err != nil {
		t.Fatalf("initializing own playbook: %v", err)
	}
	if err := st.EnsureAgent(context.Background(), "foreign"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InitializePlaybook(context.Background(), "foreign", []string{"FOREIGN CONTEXT HYPOTHESIS"}, playbookContextAt); err != nil {
		t.Fatalf("initializing foreign playbook: %v", err)
	}

	own, err := st.PlaybookEntries(context.Background(), tmpl.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	// A decided entry must keep its code-owned status and counters in context.
	decided := own[0]
	if _, err := st.DB().Exec(`UPDATE playbook SET status='established',n=31,decided_at=? WHERE id=?`,
		playbookContextAt.Add(time.Hour).Format(time.RFC3339Nano), decided.ID); err != nil {
		t.Fatal(err)
	}

	raw := playbookContextMessages(t, r)
	if strings.Contains(string(raw), "FOREIGN CONTEXT HYPOTHESIS") {
		t.Fatalf("context leaked foreign playbook text: %s", raw)
	}
	envelope := decodePlaybookContext(t, raw)
	if len(envelope.Playbook) != 2 {
		t.Fatalf("context playbook = %+v, want 2 own entries", envelope.Playbook)
	}
	seen := map[int64]store.PlaybookEntry{}
	for _, entry := range envelope.Playbook {
		if entry.AgentID != tmpl.ID {
			t.Fatalf("context entry belongs to %q: %+v", entry.AgentID, entry)
		}
		seen[entry.ID] = entry
	}
	got, ok := seen[decided.ID]
	if !ok || got.Status != "established" || got.N != 31 || got.Revision != 1 || got.DecidedAt == nil {
		t.Fatalf("decided context entry = %+v", got)
	}
	for _, entry := range envelope.Playbook {
		if entry.Status == "testing" && entry.DecidedAt != nil {
			t.Fatalf("testing context entry carries decided_at: %+v", entry)
		}
	}
}

func TestPlaybookContextIsBoundedToLatestHundredOwnEntries(t *testing.T) {
	st, opts, r := playbookContextRuntime(t)
	tmpl := opts.Template
	if err := st.EnsureAgent(context.Background(), tmpl.ID); err != nil {
		t.Fatal(err)
	}
	// Insert more than the context bound directly as code-owned decided rows so
	// the five-testing cap does not obscure the bound.
	for i := 0; i < 120; i++ {
		created := playbookContextAt.Add(time.Duration(i) * time.Second)
		if _, err := st.DB().Exec(`INSERT INTO playbook(agent_id,text,status,revision,n,created_at,decided_at) VALUES(?,?,'established',1,10,?,?)`,
			tmpl.ID, "context bound hypothesis "+string(rune('a'+i%26))+string(rune('a'+i/26)), created.Format(time.RFC3339Nano), created.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("inserting context entry %d: %v", i, err)
		}
	}
	envelope := decodePlaybookContext(t, playbookContextMessages(t, r))
	if len(envelope.Playbook) != 100 {
		t.Fatalf("context playbook entries = %d, want 100", len(envelope.Playbook))
	}
	for _, entry := range envelope.Playbook {
		if entry.AgentID != tmpl.ID || entry.Status != "established" || entry.N != 10 {
			t.Fatalf("bounded context entry = %+v", entry)
		}
	}
}

func TestPlaybookContextRedactsConfiguredSecretsFromEditableText(t *testing.T) {
	const secret = "sk-context-playbook-secret"
	st, opts, r := playbookContextRuntime(t, func(opts *agent.Options) {
		opts.Redactor = config.NewRedactor(config.NewSecret(secret))
	})
	tmpl := opts.Template
	if err := st.EnsureAgent(context.Background(), tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO playbook(agent_id,text,status,revision,n,created_at) VALUES(?,?,'testing',1,0,?)`,
		tmpl.ID, "hypothesis embeds "+secret+" in editable text", playbookContextAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("inserting secret-bearing entry: %v", err)
	}
	raw := playbookContextMessages(t, r)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("context leaked configured secret: %s", raw)
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("context did not mark redaction: %s", raw)
	}
	envelope := decodePlaybookContext(t, raw)
	if len(envelope.Playbook) != 1 || !strings.Contains(envelope.Playbook[0].Text, "[REDACTED]") {
		t.Fatalf("redacted context playbook = %+v", envelope.Playbook)
	}
}

func TestPlaybookContextFailureAndCancellationSurface(t *testing.T) {
	st, _, r := playbookContextRuntime(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Context(cancelled, "agent"); err == nil {
		t.Fatal("cancelled context build succeeded")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Context(context.Background(), "agent"); err == nil {
		t.Fatal("context build against a closed store succeeded")
	}
}

func TestContextRedactsRedditCredentialsFromOwnerAnswersAndPlaybook(t *testing.T) {
	const id, secret = "reddit-id-canary", "reddit-secret-canary"
	st, opts, r := playbookContextRuntime(t, func(opts *agent.Options) {
		opts.Template.Study.RedditClientID = config.NewSecret(id)
		opts.Template.Study.RedditClientSecret = config.NewSecret(secret)
	})
	at := playbookContextAt.Format(time.RFC3339Nano)
	if _, err := st.DB().Exec(`INSERT INTO playbook(agent_id,text,status,revision,n,created_at) VALUES(?,?,'testing',1,0,?)`, opts.Template.ID, "play "+id+" and "+secret, at); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO owner_msgs(agent_id,direction,text,at,kind,event_key,delivery) VALUES(?,'in',?,?,'note','k1','received')`, opts.Template.ID, "answer "+id+" and "+secret, at); err != nil {
		t.Fatal(err)
	}
	raw := string(playbookContextMessages(t, r))
	if strings.Contains(raw, id) || strings.Contains(raw, secret) || strings.Count(raw, "[REDACTED]") < 4 {
		t.Fatalf("context leaked Reddit credentials: %s", raw)
	}
}

func TestWakeContextListsKnowledgeIDsOfTestingHypotheses(t *testing.T) {
	st, opts, r := playbookContextRuntime(t)
	ctx := context.Background()
	id := opts.Template.ID
	for _, p := range []struct{ text, kid string }{{"a", "one-topic-tag"}, {"b", "one-topic-tag"}, {"c", ""}, {"d", "words-searchable"}} {
		if _, err := st.ProposePlaybook(ctx, id, 0, p.text, playbookContextAt, p.kid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB().Exec(`UPDATE playbook SET status='rejected',decided_at=? WHERE text='d'`, playbookContextAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var got struct {
		IDs []string `json:"knowledge_ids"`
	}
	if err := json.Unmarshal(playbookContextMessages(t, r), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.IDs) != 1 || got.IDs[0] != "one-topic-tag" {
		t.Fatalf("knowledge_ids = %v, want [one-topic-tag]", got.IDs)
	}
}

func TestWakeContextSuggestsATestingHypothesisDeterministicallyUnderTheInjectedSource(t *testing.T) {
	suggest := func(seed int64) (map[string]any, []store.PlaybookEntry) {
		st, opts, r := playbookContextRuntime(t, func(o *agent.Options) { o.Rand = rand.New(rand.NewSource(seed)) })
		if _, err := st.InitializePlaybook(context.Background(), opts.Template.ID, []string{"one", "two", "three"}, playbookContextAt); err != nil {
			t.Fatal(err)
		}
		messages, err := r.Context(context.Background(), "agent")
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Playbook  []store.PlaybookEntry `json:"playbook"`
			Suggested map[string]any        `json:"suggested_hypothesis"`
		}
		if err = json.Unmarshal([]byte(messages[1].Content), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Suggested, envelope.Playbook
	}
	picked := map[any]bool{}
	for seed := int64(0); seed < 12; seed++ {
		got, entries := suggest(seed)
		if got == nil || got["reason"] == "" {
			t.Fatalf("seed %d: %v", seed, got)
		}
		if seed == 0 {
			if again, _ := suggest(seed); !reflect.DeepEqual(got, again) {
				t.Fatalf("same source, different suggestion: %v vs %v", got, again)
			}
		}
		found := false
		for _, e := range entries {
			found = found || float64(e.ID) == got["hypothesis_id"]
		}
		if !found {
			t.Fatalf("suggestion %v names no testing hypothesis of %+v", got, entries)
		}
		picked[got["hypothesis_id"]] = true
	}
	if len(picked) != 3 {
		t.Fatalf("with no evidence every hypothesis should be suggested at some seed: %v", picked)
	}
}
