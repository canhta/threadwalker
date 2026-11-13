package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

func openPhaseGuard(t *testing.T, phase string) (*store.Store, *guard.Guard, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "phase.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tmpl := config.Template{ID: "phase-agent", DryRun: true, Limits: config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 5, RepliesPerUser7d: 1, MinSecondsBetweenActions: 1, MaxStepsPerWake: 10, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 1}}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	kind := store.WakeDream
	if phase == guard.PhaseStudy {
		kind = store.WakeStudy
	}
	wakeID, err := st.BeginPhaseWake(ctx, tmpl.ID, kind, "2026-01-01", time.Date(2026, 1, 1, 23, 40, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	g, err := guard.New(guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Phase: phase, Now: func() time.Time { return time.Date(2026, 1, 1, 23, 40, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return st, g, wakeID
}

func TestGuardBlocksThreadsAndOwnerWritesInDreamAndStudy(t *testing.T) {
	for _, phase := range []string{guard.PhaseDream, guard.PhaseStudy} {
		t.Run(phase, func(t *testing.T) {
			st, g, wakeID := openPhaseGuard(t, phase)
			writeCalls := map[string]string{
				"post":                `{"text":"synthetic take","hypothesis_ids":[]}`,
				"reply":               `{"text":"synthetic reply","target_id": "tid", "target_url":"https://threads.net/@someone/post/1","target_username":"someone","hypothesis_ids":[]}`,
				"notify_owner":        `{"text":"synthetic"}`,
				"ask_owner":           `{"text":"synthetic"}`,
				"mark_do_not_contact": `{"username":"someone"}`,
			}
			for tool, args := range writeCalls {
				var ran bool
				_, err := g.Execute(context.Background(), guard.Attempt{Tool: tool, Arguments: args}, func() (string, error) {
					ran = true
					return "effect", nil
				})
				if !errors.Is(err, guard.ErrBlocked) || ran {
					t.Fatalf("%s in %s: err=%v ran=%v", tool, phase, err, ran)
				}
			}
			var blocked string
			if err := st.DB().QueryRow(`SELECT blocked_by FROM tool_calls WHERE wake_id=? ORDER BY id DESC LIMIT 1`, wakeID).Scan(&blocked); err != nil || blocked != "phase_read_only" {
				t.Fatalf("blocked_by=%q err=%v", blocked, err)
			}
			for _, tool := range []string{"note_write", "note_search", "note_read", "playbook_read", "playbook_propose", "fetch_trends", "read_feed", "search", "read_post", "read_own_metrics"} {
				var ran bool
				_, err := g.Execute(context.Background(), guard.Attempt{Tool: tool, Arguments: allowedArgs(tool)}, func() (string, error) {
					ran = true
					return "ok", nil
				})
				if !ran {
					t.Fatalf("%s should run in %s: err=%v", tool, phase, err)
				}
			}
		})
	}
}

func allowedArgs(tool string) string {
	switch tool {
	case "note_write", "playbook_propose":
		return `{"text":"synthetic note"}`
	case "note_search":
		return `{"query":"synthetic"}`
	case "note_read":
		return `{"ref":"note:1"}`
	case "search":
		return `{"query":"synthetic"}`
	case "read_post":
		return `{"url":"https://threads.net/@someone/post/1"}`
	default:
		return `{}`
	}
}

func TestSpecPhases(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range guard.Specs() {
		if seen[spec.Name] {
			t.Fatalf("%s listed twice", spec.Name)
		}
		seen[spec.Name] = true
		if len(spec.Phases) == 0 {
			t.Fatalf("%s allowed in no phase", spec.Name)
		}
		for _, phase := range []string{guard.PhaseDream, guard.PhaseStudy} {
			if spec.AllowedIn(phase) && spec.Effect != guard.EffectRead && spec.Effect != guard.EffectOwnState {
				t.Fatalf("%s (%s) allowed in %s", spec.Name, spec.Effect, phase)
			}
		}
	}
	web, ok := guard.Spec("web_search")
	if !ok || web.AllowedIn(guard.PhaseDream) || !web.AllowedIn("") || !web.AllowedIn(guard.PhaseStudy) || !web.AllowedIn(guard.PhaseAgent) {
		t.Fatal("web_search must be agent and study only")
	}
	for _, tool := range []string{"post", "reply", "notify_owner", "ask_owner", "mark_do_not_contact"} {
		spec, _ := guard.Spec(tool)
		if spec.AllowedIn(guard.PhaseStudy) || spec.AllowedIn(guard.PhaseDream) || !spec.AllowedIn("") {
			t.Fatalf("%s phases %v", tool, spec.Phases)
		}
	}
	if _, ok := guard.Spec("untrusted-name-canary"); ok {
		t.Fatal("unlisted tool has a spec")
	}
}

// Labels and lifts are code-owned columns: no tool declares them and every tool
// refuses them as input, valid arguments or not.
func TestOutcomeFieldsAreRefusedAsToolInput(t *testing.T) {
	valid := func(spec guard.ToolSpec) map[string]any {
		args := map[string]any{}
		for _, a := range spec.Args {
			switch a.Kind {
			case guard.ArgText:
				args[a.Name] = "x"
			case guard.ArgTime:
				args[a.Name] = "2026-01-01T00:00:00Z"
			case guard.ArgLimit, guard.ArgID:
				args[a.Name] = 1
			case guard.ArgIDList:
				args[a.Name] = []int{1}
			case guard.ArgBool:
				args[a.Name] = true
			case guard.ArgTextList:
				args[a.Name] = []string{"x"}
			}
		}
		return args
	}
	for _, spec := range guard.Specs() {
		raw, _ := json.Marshal(valid(spec))
		if _, err := spec.Parse(string(raw)); err != nil {
			t.Fatalf("%s: baseline arguments rejected: %v", spec.Name, err)
		}
		for _, field := range []string{"label", "reach_lift", "resonance_lift", "applause_lift", "followers_per_1k", "model_version"} {
			args := valid(spec)
			args[field] = "viral"
			raw, _ := json.Marshal(args)
			if _, err := spec.Parse(string(raw)); err == nil {
				t.Errorf("%s accepted %s", spec.Name, field)
			}
		}
	}
}

func TestSpecParse(t *testing.T) {
	reply, _ := guard.Spec("reply")
	search, _ := guard.Spec("note_search")
	for _, tc := range []struct {
		spec guard.ToolSpec
		raw  string
		ok   bool
	}{
		{reply, `{"text":"x","target_id": "tid", "target_url":"u","target_username":"n"}`, true},
		{reply, `{"text":"x","target_id": "tid", "target_url":"u","target_username":"n","hypothesis_ids":[1,2]}`, true},
		{reply, `{"text":"x"}`, false},
		{reply, `{"text":" ","target_id": "tid", "target_url":"u","target_username":"n"}`, false},
		{reply, `{"text":null,"target_id": "tid", "target_url":"u","target_username":"n"}`, false},
		{reply, `{"text":"x","target_id": "tid", "target_url":"u","target_username":"n","hypothesis_ids":[0]}`, false},
		{reply, `{"text":"x","target_id": "tid", "target_url":"u","target_username":"n","hypothesis_ids":null}`, false},
		{reply, `{"text":"x","target_id": "tid", "target_url":"u","target_username":"n","extra":1}`, false},
		{reply, `[]`, false},
		{reply, `null`, false},
		{reply, `{`, false},
		{search, `{"query":"q"}`, true},
		{search, `{"query":"q","limit":100,"since":"2026-11-11T00:00:00Z"}`, true},
		{search, `{"query":"q","limit":0}`, false},
		{search, `{"query":"q","limit":101}`, false},
		{search, `{"query":"q","limit":"5"}`, false},
		{search, `{"query":"q","until":"2026-11-11"}`, false},
	} {
		if _, err := tc.spec.Parse(tc.raw); (err == nil) != tc.ok {
			t.Fatalf("%s %s: err=%v", tc.spec.Name, tc.raw, err)
		}
	}
	args, err := reply.Parse(`{"text":"hi","target_id": "tid", "target_url":"u","target_username":"n","hypothesis_ids":[3]}`)
	if err != nil || args.Text("text") != "hi" || len(args.IDs("hypothesis_ids")) != 1 || len(args.Texts()) != 4 {
		t.Fatalf("args %+v err %v", args, err)
	}
	args, _ = search.Parse(`{"query":"q"}`)
	if args.Int("limit") != 10 {
		t.Fatalf("default limit %d", args.Int("limit"))
	}
	read, _ := guard.Spec("read_own_metrics")
	if args, err = read.Parse(`{"action_id":7}`); err != nil || args.Int("action_id") != 7 {
		t.Fatalf("action_id %+v %v", args, err)
	}
	if _, err = read.Parse(`{"action_id":0}`); err == nil {
		t.Fatal("zero action_id accepted")
	}
}
