package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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
	wakeID, err := st.BeginPhaseWake(ctx, tmpl.ID, store.WakeDream, "2026-01-01", time.Date(2026, 1, 1, 23, 40, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	g, err := guard.New(guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Phase: phase, Now: func() time.Time { return time.Date(2026, 1, 1, 23, 40, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return st, g, wakeID
}

func TestGuardBlocksThreadsAndOwnerWritesInTheDream(t *testing.T) {
	for _, phase := range []string{guard.PhaseDream} {
		t.Run(phase, func(t *testing.T) {
			st, g, wakeID := openPhaseGuard(t, phase)
			writeCalls := map[string]string{
				"post":                `{"text":"synthetic take"}`,
				"reply":               `{"text":"synthetic reply","target":"p1"}`,
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
			for _, tool := range []string{"fetch_trends", "find_posts", "read_post", "read_replies"} {
				var ran bool
				_, err := g.Execute(context.Background(), guard.Attempt{Tool: tool, Arguments: allowedArgs(tool)}, func() (string, error) {
					ran = true
					return "ok", nil
				})
				if !errors.Is(err, guard.ErrBlocked) || ran {
					t.Fatalf("%s must not run in %s: err=%v ran=%v", tool, phase, err, ran)
				}
			}
		})
	}
}

func allowedArgs(tool string) string {
	switch tool {
	case "find_posts":
		return `{"source":"search","query":"synthetic"}`
	case "read_post":
		return `{"id":"1"}`
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
		for _, phase := range []string{guard.PhaseDream} {
			if spec.AllowedIn(phase) && spec.Effect != guard.EffectRead && spec.Effect != guard.EffectOwnState {
				t.Fatalf("%s (%s) allowed in %s", spec.Name, spec.Effect, phase)
			}
		}
	}
	web, ok := guard.Spec("web_search")
	if !ok || web.AllowedIn(guard.PhaseDream) || !web.AllowedIn("") || !web.AllowedIn(guard.PhaseAgent) {
		t.Fatal("web_search must be agent only")
	}
	for _, tool := range []string{"post", "reply", "notify_owner", "ask_owner", "mark_do_not_contact"} {
		spec, _ := guard.Spec(tool)
		if spec.AllowedIn(guard.PhaseDream) || !spec.AllowedIn("") {
			t.Fatalf("%s phases %v", tool, spec.Phases)
		}
	}
	var dream []string
	for _, spec := range guard.Specs() {
		if spec.AllowedIn(guard.PhaseDream) {
			dream = append(dream, spec.Name)
		}
	}
	if strings.Join(dream, ",") != "lesson_add,lesson_confirm,lesson_retire" {
		t.Fatalf("the dream's tools are %v", dream)
	}
	if _, ok := guard.Spec("untrusted-name-canary"); ok {
		t.Fatal("unlisted tool has a spec")
	}
}

// Code-owned columns are never tool input: every tool refuses them, valid
// arguments or not.
func TestCodeOwnedFieldsAreRefusedAsToolInput(t *testing.T) {
	valid := func(spec guard.ToolSpec) map[string]any {
		args := map[string]any{}
		for _, a := range spec.Args {
			switch a.Kind {
			case guard.ArgText:
				args[a.Name] = "x"
				if a.Enum != nil {
					args[a.Name] = a.Enum[0]
				}
			case guard.ArgID:
				args[a.Name] = 1
			case guard.ArgBool:
				args[a.Name] = true
			case guard.ArgTextList:
				args[a.Name] = []string{"x"}
			case guard.ArgIDList:
				args[a.Name] = []int{1}
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
	find, _ := guard.Spec("find_posts")
	post, _ := guard.Spec("post")
	for _, tc := range []struct {
		spec guard.ToolSpec
		raw  string
		ok   bool
	}{
		{reply, `{"text":"x","target":"p1"}`, true},
		{reply, `{"text":"x"}`, false},
		{reply, `{"text":" ","target":"p1"}`, false},
		{reply, `{"text":null,"target":"p1"}`, false},
		{reply, `{"text":"x","target":"p1","answers_note":0}`, false},
		{reply, `{"text":"x","target":"p1","answers_note":null}`, true},
		{reply, `{"text":"x","target":"p1","extra":1}`, false},
		{reply, `[]`, false},
		{reply, `null`, false},
		{reply, `{`, false},
		{reply, `{"text":"x","target":"p1","lesson_ids":[1,2]}`, true},
		{reply, `{"text":"x","target":"p1","lesson_ids":[]}`, true},
		{reply, `{"text":"x","target":"p1","lesson_ids":[0]}`, false},
		{reply, `{"text":"x","target":"p1","lesson_ids":["1"]}`, false},
		{find, `{"source":"search","query":"q"}`, true},
		{find, `{"source":"search","query":"q","recent":true}`, true},
		{find, `{"source":"search","query":"q","recent":"yes"}`, false},
		{find, `{"source":"search","query":"q","limit":5}`, false},
		{find, `{"source":"feed"}`, true},
		{find, `{"source":"home"}`, false},
		{find, `{"query":"q"}`, false},
		{post, `{"text":"x","hook":"number","trend":"t1"}`, true},
		{post, `{"text":"x","hook":"shout"}`, false},
		// An optional argument sent empty counts as not sent.
		{post, `{"text":"x","lesson_ids":[],"topic_tag":"","parts":null}`, true},
		{reply, `{"target":""}`, false},
	} {
		if _, err := tc.spec.Parse(tc.raw); (err == nil) != tc.ok {
			t.Fatalf("%s %s: err=%v", tc.spec.Name, tc.raw, err)
		}
	}
	args, err := reply.Parse(`{"text":"hi","target":"p1","answers_note":3}`)
	if err != nil || args.Text("text") != "hi" || args.Int("answers_note") != 3 || len(args.Texts()) != 2 {
		t.Fatalf("args %+v err %v", args, err)
	}
	read, _ := guard.Spec("read_own_metrics")
	if args, err = read.Parse(`{"action_id":7}`); err != nil || args.Int("action_id") != 7 {
		t.Fatalf("action_id %+v %v", args, err)
	}
	if _, err = read.Parse(`{"action_id":0}`); err == nil {
		t.Fatal("zero action_id accepted")
	}
}
