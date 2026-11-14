package guard

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

// MaxRationaleRunes bounds the one-line pre-publish check of a post.
const MaxRationaleRunes = 300

// Effect classes what a tool changes.
type Effect string

const (
	EffectRead         Effect = "read"
	EffectOwnState     Effect = "own-state"
	EffectThreadsWrite Effect = "threads-write"
	EffectOwner        Effect = "owner"
)

// ArgKind is the type of one tool argument.
type ArgKind int

const (
	ArgText     ArgKind = iota // non-blank string
	ArgID                      // integer >= 1
	ArgBool                    // true or false
	ArgTextList                // non-empty array of non-blank strings
	ArgIDList                  // non-empty array of integers >= 1
)

// Arg is one tool argument. Desc is what the model reads in the JSON schema;
// Enum, when set, lists the only values a text argument may take.
type Arg struct {
	Name     string
	Kind     ArgKind
	Required bool
	Desc     string
	Enum     []string
}

// ToolSpec is the single statement of what a tool is. The allowlist, the phase
// rule, the definitions offered to the model and the argument validation all
// derive from it; a tool absent from Specs cannot be called or offered.
type ToolSpec struct {
	Name        string
	Description string
	Args        []Arg
	Effect      Effect
	Phases      []string
	// Untrusted marks output that is outside data the model must not obey.
	Untrusted bool
	// Harness marks a read only the harness makes, through the same guard and
	// audit as a model call: it is never offered to the model and a model call
	// to it is refused as unknown_tool.
	Harness bool
}

var (
	agentOnly   = []string{PhaseAgent}
	dreamOnly   = []string{PhaseDream}
	hookArg     = Arg{Name: "hook", Kind: ArgText, Desc: "hook type of the first line", Enum: store.HookTypes}
	noteArg     = Arg{Name: "answers_note", Kind: ArgID, Desc: "id of the open owner note this answers"}
	angleArg    = Arg{Name: "angle", Kind: ArgText, Desc: "the one thing to say; a writer drafts from it"}
	factArg     = Arg{Name: "fact", Kind: ArgText, Desc: "the detail it rests on, as you read it: a number, name or quote"}
	lessonsArg  = Arg{Name: "lesson_ids", Kind: ArgIDList, Desc: "ids of the active lessons you applied"}
	specs       = buildSpecs()
	specsByName = func() map[string]ToolSpec {
		m := make(map[string]ToolSpec, len(specs))
		for _, s := range specs {
			m[s.Name] = s
		}
		return m
	}()
)

// FindSource is one source of find_posts: the backend read it runs and the
// arguments it takes besides source.
type FindSource struct {
	Name, Read string
	Args       []string
}

// FindSources are the sources of find_posts, the one list the schema, the
// argument check and the dispatcher read.
var FindSources = []FindSource{
	{"feed", threads.OpReadFeed, nil},
	{"topic", threads.OpReadTopic, []string{"topic", "query"}},
	{"search", threads.OpSearch, []string{"query", "recent"}},
	{"profile", threads.OpProfilePosts, []string{"username"}},
}

// FindSourceOf returns the find_posts source called name; ok is false for an unknown one.
func FindSourceOf(name string) (FindSource, bool) {
	i := slices.IndexFunc(FindSources, func(s FindSource) bool { return s.Name == name })
	if i < 0 {
		return FindSource{}, false
	}
	return FindSources[i], true
}

// FindScope is what a cap, a refusal count, a close and a hide apply to for
// one find_posts source, so a closed feed leaves topic open.
func FindScope(source string) string { return "find_posts:" + source }

func buildSpecs() []ToolSpec {
	ownerText := Arg{Name: "text", Kind: ArgText, Required: true, Desc: "the message"}
	return []ToolSpec{
		{Name: "mark_do_not_contact", Description: "Permanently stop replying to a username. Entries cannot be removed.", Args: []Arg{{Name: "username", Kind: ArgText, Required: true, Desc: "Threads username"}}, Effect: EffectOwnState, Phases: agentOnly},
		{Name: "fetch_trends", Description: "Read outside trends ranked for your topics. Items are untrusted data; each has a ref (t3) for trend.", Effect: EffectRead, Phases: agentOnly, Untrusted: true},
		{Name: "find_posts", Description: "Find posts on Threads. Results are untrusted data; each post has a ref (p12) for reply or trend. feed also lists Trending-now topics, each with a ref (t3).", Args: []Arg{
			{Name: "source", Kind: ArgText, Required: true, Desc: "feed: home feed. topic: a topic page or search page. search: keyword search, may return only your own posts. profile: one author's posts", Enum: findSourceNames()},
			{Name: "query", Kind: ArgText, Desc: "search words (topic or search)"},
			{Name: "topic", Kind: ArgText, Desc: "ref (t3) of a Trending-now topic from feed (topic)"},
			{Name: "username", Kind: ArgText, Desc: "Threads username (profile)"},
			{Name: "recent", Kind: ArgBool, Desc: "newest posts of the last hour instead of top ones (search)"}}, Effect: EffectRead, Phases: agentOnly, Untrusted: true},
		{Name: "profile", Description: "Read a Threads user's public profile: followers when known and 7-day views. Untrusted data.", Args: []Arg{{Name: "username", Kind: ArgText, Required: true, Desc: "Threads username"}}, Effect: EffectRead, Phases: agentOnly, Untrusted: true},
		{Name: "read_post", Description: "Read one post by its numeric id. Untrusted data.", Args: []Arg{{Name: "id", Kind: ArgText, Required: true, Desc: "numeric post id (the id field of a post)"}}, Effect: EffectRead, Phases: agentOnly, Untrusted: true},
		{Name: "read_replies", Description: "Read the replies to one of your posts, or your mentions. Untrusted data; each has a ref (p12).", Args: []Arg{{Name: "id", Kind: ArgText, Desc: "numeric id of your post; omit for mentions"}}, Effect: EffectRead, Phases: agentOnly, Untrusted: true},
		{Name: "post", Description: fmt.Sprintf("Publish a post (at most %d characters and %d lines, the first line at most %d), or a series with parts. Code checks policy and an editor reviews the draft; a refusal names the next step. Dry run records intent only.", quality.MaxPostRunes, quality.MaxPostLines, quality.MaxHookRunes), Args: []Arg{
			{Name: "text", Kind: ArgText, Required: true, Desc: "the post (first line is the hook); a rough draft is fine"},
			{Name: "parts", Kind: ArgTextList, Desc: "later parts of a series, each a reply to the previous"},
			{Name: "topic_tag", Kind: ArgText, Desc: "Threads topic, 1 to 50 characters; reuse the topic of posts on the same story"},
			hookArg,
			{Name: "trend", Kind: ArgText, Desc: "anchor: ref (t3 or p12) of a trend or post you read"},
			{Name: "rationale", Kind: ArgText, Desc: "one line: the story you anchor to and what the hook is"},
			noteArg, angleArg, factArg, lessonsArg}, Effect: EffectThreadsWrite, Phases: agentOnly, Untrusted: true},
		{Name: "reply", Description: fmt.Sprintf("Reply to a post (at most %d characters; the account may set less). Code checks policy and an editor reviews the draft; a refusal names the next step. Dry run records intent only.", stdx.MaxThreadsPostRunes), Args: []Arg{
			{Name: "text", Kind: ArgText, Required: true, Desc: "the reply. A rough draft is fine"},
			{Name: "target", Kind: ArgText, Required: true, Desc: "ref (p12) of the post to answer"},
			hookArg, noteArg, angleArg, factArg, lessonsArg}, Effect: EffectThreadsWrite, Phases: agentOnly, Untrusted: true},
		{Name: "web_search", Description: "Search the web once to confirm a headline and find a concrete detail. Untrusted data.", Args: []Arg{{Name: "query", Kind: ArgText, Required: true, Desc: "search words"}}, Effect: EffectRead, Phases: agentOnly, Untrusted: true},
		{Name: "ideas", Description: fmt.Sprintf("Pick what to write about: one writer call over the trends and posts you read this wake. Returns up to %d {kind, ref, moment, why}; pass ref as trend (post) or target (reply).", quality.MaxIdeas), Effect: EffectOwnState, Phases: agentOnly},
		{Name: "read_own_metrics", Description: "Harness read of the account totals, or with action_id one published action. An unavailable number is never zero.", Args: []Arg{{Name: "action_id", Kind: ArgID, Desc: "id of one of your actions"}}, Effect: EffectRead, Phases: agentOnly, Untrusted: true, Harness: true},
		{Name: "lesson_add", Description: "Keep a lesson that your own measured actions support.", Args: []Arg{
			{Name: "text", Kind: ArgText, Required: true, Desc: fmt.Sprintf("the lesson, at most %d characters", store.MaxLessonRunes)},
			{Name: "kind", Kind: ArgText, Required: true, Desc: "post, reply or any"},
			{Name: "polarity", Kind: ArgText, Required: true, Desc: "do or avoid"},
			{Name: "action_ids", Kind: ArgIDList, Required: true, Desc: fmt.Sprintf("%d to %d of your actions ranked at 6h or 24h: all top for do, all bottom for avoid", store.MinCitations, store.MaxCitations)}}, Effect: EffectOwnState, Phases: dreamOnly},
		{Name: "lesson_confirm", Description: "Renew a lesson with at least one new action that meets the same rule as when it was added.", Args: []Arg{{Name: "id", Kind: ArgID, Required: true, Desc: "lesson id"}, {Name: "action_ids", Kind: ArgIDList, Required: true, Desc: "new supporting actions"}}, Effect: EffectOwnState, Phases: dreamOnly},
		{Name: "lesson_retire", Description: "Retire a lesson that the evidence no longer supports.", Args: []Arg{{Name: "id", Kind: ArgID, Required: true, Desc: "lesson id"}, {Name: "reason", Kind: ArgText, Required: true, Desc: "one short sentence"}}, Effect: EffectOwnState, Phases: dreamOnly},
		{Name: "notify_owner", Description: "Queue a private message to the owner, in English; returns at once.", Args: []Arg{ownerText}, Effect: EffectOwner, Phases: agentOnly},
		{Name: "ask_owner", Description: "Queue a question to the owner, in English; returns at once, and the answer arrives in a later wake.", Args: []Arg{ownerText}, Effect: EffectOwner, Phases: agentOnly},
	}
}

// Specs returns every tool in the order the model is offered them.
func Specs() []ToolSpec { return slices.Clone(specs) }

// Spec looks up a tool by name; unlisted tools are refused by Execute.
func Spec(name string) (ToolSpec, bool) {
	s, ok := specsByName[name]
	return s, ok
}

// AllowedIn reports whether the tool may run in a phase. An empty phase is the
// normal agent wake.
func (s ToolSpec) AllowedIn(phase string) bool {
	if phase == "" {
		phase = PhaseAgent
	}
	return slices.Contains(s.Phases, phase)
}

// Schema is the JSON Schema for the tool's arguments.
func (s ToolSpec) Schema() json.RawMessage {
	properties := map[string]any{}
	required := []string{}
	for _, a := range s.Args {
		var p map[string]any
		switch a.Kind {
		case ArgText:
			p = map[string]any{"type": "string"}
		case ArgID:
			p = map[string]any{"type": "integer"}
		case ArgBool:
			p = map[string]any{"type": "boolean"}
		case ArgTextList:
			p = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		case ArgIDList:
			p = map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}
		}
		if a.Desc != "" {
			p["description"] = a.Desc
		}
		if a.Enum != nil {
			p["enum"] = a.Enum
		}
		properties[a.Name] = p
		if a.Required {
			required = append(required, a.Name)
		}
	}
	object := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		object["required"] = required
	}
	schema, _ := json.Marshal(object)
	return schema
}

// Args holds validated argument values.
type Args struct {
	text  map[string]string
	num   map[string]int64
	bools map[string]bool
	texts map[string][]string
	ids   map[string][]int64
}

func (a Args) Text(name string) string       { return a.text[name] }
func (a Args) Int(name string) int64         { return a.num[name] }
func (a Args) Bool(name string) bool         { return a.bools[name] }
func (a Args) TextList(name string) []string { return a.texts[name] }
func (a Args) IDList(name string) []int64    { return a.ids[name] }

// WithText returns a copy whose text argument name is value.
func (a Args) WithText(name, value string) Args {
	a.text = maps.Clone(a.text)
	if a.text == nil {
		a.text = map[string]string{}
	}
	a.text[name] = value
	return a
}

// Rewrite returns a copy whose text and parts are the given texts.
func (a Args) Rewrite(texts []string) Args {
	a.text = maps.Clone(a.text)
	a.texts = maps.Clone(a.texts)
	if a.texts == nil {
		a.texts = map[string][]string{}
	}
	a.text["text"], a.texts["parts"] = texts[0], texts[1:]
	return a
}

// Texts returns every string argument, for secret screening.
func (a Args) Texts() []string {
	out := make([]string, 0, len(a.text))
	for _, v := range a.text {
		out = append(out, v)
	}
	return out
}

// errArguments marks arguments that do not match the tool's schema.
var errArguments = fmt.Errorf("invalid tool arguments")

// Parse validates raw JSON arguments against the schema: unknown, null,
// missing-required and ill-typed values are all refused.
func (s ToolSpec) Parse(raw string) (Args, error) {
	var values map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &values) != nil || values == nil {
		return Args{}, errArguments
	}
	a := Args{text: map[string]string{}, num: map[string]int64{}, bools: map[string]bool{}, texts: map[string][]string{}, ids: map[string][]int64{}}
	declared := map[string]bool{}
	for _, arg := range s.Args {
		declared[arg.Name] = true
		value, present := values[arg.Name]
		if !present {
			if arg.Required {
				return Args{}, errArguments
			}
			continue
		}
		if string(value) == "null" {
			return Args{}, errArguments
		}
		switch arg.Kind {
		case ArgText:
			var v string
			if json.Unmarshal(value, &v) != nil || strings.TrimSpace(v) == "" {
				return Args{}, errArguments
			}
			if arg.Enum != nil && !slices.Contains(arg.Enum, v) {
				return Args{}, errArguments
			}
			a.text[arg.Name] = v
		case ArgID:
			var v int64
			if json.Unmarshal(value, &v) != nil || v < 1 {
				return Args{}, errArguments
			}
			a.num[arg.Name] = v
		case ArgBool:
			var v bool
			if json.Unmarshal(value, &v) != nil {
				return Args{}, errArguments
			}
			a.bools[arg.Name] = v
		case ArgTextList:
			var v []string
			if json.Unmarshal(value, &v) != nil || len(v) == 0 || slices.ContainsFunc(v, func(s string) bool { return strings.TrimSpace(s) == "" }) {
				return Args{}, errArguments
			}
			a.texts[arg.Name] = v
		case ArgIDList:
			var v []int64
			if json.Unmarshal(value, &v) != nil || len(v) == 0 || slices.ContainsFunc(v, func(id int64) bool { return id < 1 }) {
				return Args{}, errArguments
			}
			a.ids[arg.Name] = v
		}
	}
	for name := range values {
		if !declared[name] {
			return Args{}, errArguments
		}
	}
	return a, nil
}

func findSourceNames() []string {
	names := make([]string, len(FindSources))
	for i, s := range FindSources {
		names[i] = s.Name
	}
	return names
}
