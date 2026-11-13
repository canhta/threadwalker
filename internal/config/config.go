package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	// The owner's zone is a template field; embedding zone data keeps it
	// resolvable on minimal hosts without a system tzdata package.
	_ "time/tzdata"
)

// EmbeddingKeyEnv names the environment variable holding the embedding key.
const EmbeddingKeyEnv = "OPENAI_API_KEY"

// Secret holds a sensitive value that redacts itself when formatted.
type Secret struct {
	value string
}

// NewSecret constructs a Secret from a raw value.
func NewSecret(v string) Secret {
	return Secret{value: v}
}

// Expose returns the underlying secret value for authorized callers.
func (s Secret) Expose() string {
	return s.value
}

// String returns a redacted placeholder so secrets never leak into logs.
func (s Secret) String() string {
	return "[REDACTED]"
}

// GoString returns a redacted placeholder for %#v formatting.
func (s Secret) GoString() string {
	return "config.Secret([REDACTED])"
}

// Template represents an agent template loaded from YAML.
type Template struct {
	ID           string
	Goal         string
	Persona      PersonaConfig
	Guard        GuardConfig
	SeedPlaybook []string
	LLM          LLMConfig
	// Review is the optional independent judge of published actions.
	Review LLMReview
	// Critic is the model that reviews every post and reply before it is reserved.
	// Its Model is empty without a critic.llm block; the critic then uses LLM.
	Critic LLMConfig
	// Writer is the optional tool-free model that drafts every post and reply
	// from the agent's brief, with its own reasoning effort. Empty Model: no writer.
	Writer LLMConfig
	// PostWriter, when set, replaces Writer for the draft of a post or series;
	// replies and ideas keep Writer. It requires Writer.
	PostWriter LLMConfig
	// EmbeddingAPIKey is the OpenAI key for memory embeddings, read from
	// EmbeddingKeyEnv whatever LLM the template chats with.
	EmbeddingAPIKey Secret
	Limits          LimitsConfig
	// MinEffect is the smallest mean lift (log scale) worth acting on: the
	// playbook establishes a hypothesis only above it.
	MinEffect float64
	Schedule  ScheduleConfig
	KPI       KPIConfig
	Study     StudyConfig
	Inbox     InboxConfig
	Trends    TrendsConfig
	Targets   TargetsConfig
	Threads   ThreadsConfig
	Telegram  TelegramConfig
	DryRun    bool
}

// KPIConfig is the pressure on the agent. Without a kpi block every field is
// zero: no scoreboard targets, never behind, and the wake cadence is the
// longer on-target interval.
type KPIConfig struct {
	EngagementsPerDay      int // likes, replies, reposts and quotes on own actions, per 24 hours
	FollowersPerWeek       int
	MaxHoursWithoutPublish int
	// OnTargetMinutes is the wait before the next wake when the agent is on
	// target; when behind it waits limits.min_minutes_between_wakes.
	OnTargetMinutes int
}

// Enabled reports whether the template declared a kpi block.
func (k KPIConfig) Enabled() bool { return k.EngagementsPerDay > 0 }

// ScheduleConfig holds the agent's own local-time phases. An empty DreamAt or
// StudySlots disables that phase; normal wakes are unaffected.
type ScheduleConfig struct {
	// Timezone is the operator's IANA zone for dream and study times. It is
	// independent of the Telegram display zone and defaults to Asia/Ho_Chi_Minh.
	Timezone   string
	DreamAt    string   // "HH:MM" local time; empty disables dreaming.
	StudySlots []string // "HH:MM" local times; empty disables study wakes.
	// QuietFrom and QuietUntil ("HH:MM" local, both or neither) bound the night
	// in which no agent wake is scheduled; a window may cross midnight.
	QuietFrom, QuietUntil string
}

// TrendsConfig holds deterministic ranking keywords for fetch_trends. Omitted
// lists derive keywords from the persona topics and avoid_topics.
type TrendsConfig struct {
	// Sources picks the wake's fetch_trends sources from StudySourceNames.
	// Empty keeps the built-in Hacker News, Product Hunt and Google Trends VN.
	Sources         []string
	IncludeKeywords []string
	AvoidKeywords   []string
}

// TargetsConfig is the window a post must sit in to be a reply target: young,
// with room in its replies, enough likes per hour and, when MinLikes is above
// 0, enough likes. Posts outside it are flagged, score 0 and are never hidden.
type TargetsConfig struct {
	MaxAgeMinutes   int
	MaxReplies      int
	MinLikesPerHour float64
	MinLikes        int
}

// DefaultTargets applies to every field a template omits.
var DefaultTargets = TargetsConfig{MaxAgeMinutes: 240, MaxReplies: 30, MinLikesPerHour: 5}

// StudySourceNames is the canonical bounded set of study-phase sources. Reddit
// needs an app-only OAuth client; every other source needs no credentials.
var StudySourceNames = []string{
	"google_news_vn", "google_news_global", "google_news_topics",
	"vnexpress", "genk", "tinhte", "vietnamnet",
	"the_verge", "techcrunch", "arstechnica",
	"hacker_news", "product_hunt", "google_trends",
	"reddit_localllama", "reddit_openai", "reddit_singularity",
	"github_trending", "youtube",
}

// DefaultStudySources keeps credential-free sources on by default. Reddit is
// opt-in because unauthenticated access is blocked.
var DefaultStudySources = []string{
	"google_news_vn", "google_news_global", "google_news_topics",
	"vnexpress", "genk", "tinhte", "vietnamnet",
	"the_verge", "techcrunch", "arstechnica",
	"hacker_news", "product_hunt", "google_trends",
	"github_trending", "youtube",
}

// DefaultYouTubeChannels are public, no-login channel feeds verified locally.
var DefaultYouTubeChannels = []string{
	"UCXZCJLdBC09xxGZ6gcdrc6A", // OpenAI
	"UCP7jMXSY2xbc3KCAE0MHQ-A", // Google DeepMind
	"UCSHZKyawb77ixDdsGog4iWA", // Lex Fridman
	"UCddiUEpeqJcYeBxX1IVBKvQ", // The Verge
}

// InboxConfig tunes the conversation inbox (replies to the account's own
// actions). PollMinutes is how often code looks for new ones between wakes; 0
// disables the polling, while every wake still lists the inbox.
// MinMinutesAfterWake is the least time between the start of a wake and an
// early wake the polling asks for.
type InboxConfig struct {
	PollMinutes         int
	MinMinutesAfterWake int
}

// StudyConfig selects study sources per agent. Empty Sources selects
// DefaultStudySources. Reddit is optional and reads its app credentials from
// environment variables only when both are set.
type StudyConfig struct {
	Sources               []string
	YouTubeChannels       []string
	RedditClientIDEnv     string
	RedditClientSecretEnv string
	RedditClientID        Secret
	RedditClientSecret    Secret
	// Threads-native sampling during study. MaxThreadsReads bounds total
	// page reads (one feed plus keyword searches), ReadPauseMS paces them and
	// ThreadsKeywords defaults to the first persona keywords.
	MaxThreadsReads int
	ThreadsKeywords []string
	ReadPauseMS     int
	// Opportunistic study triggers on a deterministic story-score spike.
	MaxStudiesPerDay         int
	MinMinutesBetweenStudies int
	SpikeMultiplier          float64
	SpikeMinScore            int
	// WebSearch is an optional, bounded OpenAI Responses web-search call made
	// only in study. It stays disabled unless the template enables it.
	WebSearch           bool
	WebSearchModel      string
	WebSearchBaseURL    string
	WebSearchMaxResults int
	WebSearchMaxOutput  int
}

// GuardConfig holds the hard, code-enforced text limits the agent cannot change.
// Omitted StopPhrases select the built-in Vietnamese and English defaults.
// BlockedPhrases are safety phrases only: a post or reply containing one is refused.
//
// SelfStatUnits are the units after which a number in a post or reply is a claim
// about the agent itself and must match its facts sheet; empty disables the check.
//
// TrendAnchor makes a post carry a hook type, a one-line rationale and a trend_ref
// that names an item read this wake, with a first line of at most 80 characters.
type GuardConfig struct {
	StopPhrases, BlockedPhrases, SelfStatUnits []string
	TrendAnchor                                bool
	// ReplyMaxRunes caps a published reply's length in characters; code
	// counts it exactly so the writer gets a number, not an editor's guess.
	// 0 leaves only the 500-character platform cap.
	ReplyMaxRunes int
}

type PersonaConfig struct {
	Name           string
	Language       string
	BioMustInclude string
	Identity       string
	Voice          string
	// Audience is who the account writes for, one paragraph; the writer, critic and ideas calls read it.
	Audience string
	// CriticRules are the numbered conditions under which the critic fails a draft.
	CriticRules   []string
	ResearchFocus []string
	StyleRules    []string
	Topics        []string
	AvoidTopics   []string
	GoodPosts     []string
	GoodReplies   []string
	BadExamples   []string
	// StyleAvoid is the starting voice's list of phrases to steer clear of. It
	// is shown to the agent and the post-mortem judge and never blocks a post.
	StyleAvoid []string
}

type LLMConfig struct {
	Protocol           string
	BaseURL            string
	Model              string
	APIKeyEnv          string
	APIKey             Secret
	PricePerMTokInUSD  float64
	PricePerMTokOutUSD float64
	// ReasoningEffort is an optional provider-specific hint, sent when nonempty: openai
	// reasoning_effort, or anthropic adaptive thinking with output_config.effort. Values are operator inputs, not an allowlist.
	ReasoningEffort string
}

// LLMReview configures the post-mortem judge; it is off without a review.llm block.
type LLMReview struct {
	Enabled bool
	LLM     LLMConfig
}

type LimitsConfig struct {
	PostsPerDay      int
	RepliesPerDay    int
	RepliesPerUser7d int
	// BrowserRepliesPerDay caps replies to feed posts, which go through the browser.
	BrowserRepliesPerDay     int
	MinSecondsBetweenActions int
	// MinMinutesBetweenPosts spaces posts (not replies) apart; 0 is off.
	MinMinutesBetweenPosts int
	MaxStepsPerWake        int
	MaxUSDPerDay           float64
	MinMinutesBetweenWakes int
	// Dreaming has its own spend and step caps and is excluded from MaxUSDPerDay.
	MaxUSDPerDream   float64
	MaxStepsPerDream int
	// ExplorePercent is the share of the last ExploreWindow own actions that may
	// be marked explore.
	ExplorePercent int
	// CriticRejectionsPerWake is how many drafts the critic may reject in one
	// wake before further writes are refused (critic_limit).
	CriticRejectionsPerWake int
}

type ThreadsConfig struct {
	Username         string // Operator-declared own account identity, never inferred.
	Backend          string // stub (default) or api
	AccessTokenEnv   string // api only; default THREADS_ACCESS_TOKEN
	UserIDEnv        string // api only; default THREADS_USER_ID
	AccessToken      Secret // api only: the long-lived token seed
	UserID           string // api only
	BaseURL          string
	ExecutablePath   string // Chrome executable the feed read uses.
	AgentBrowserPath string // Pinned agent-browser CLI binary; api only.
	ProfileDir       string
	NoSandbox        bool // Explicit operator choice for constrained local hosts.
}

type TelegramConfig struct {
	// ReportHour is the local hour (in Timezone) of the daily owner report;
	// defaults to 21 on loaded templates.
	ReportHour     int
	Enabled        bool
	BotTokenEnv    string
	BotToken       Secret
	OwnerChatIDEnv string
	OwnerChatID    Secret
	// Timezone is the owner's IANA zone for displayed times and the report
	// hour; loaded default Asia/Ho_Chi_Minh.
	Timezone string
}

// Options selects how Load resolves a template directory.
type Options struct {
	// Getenv resolves secret environment variables.
	Getenv func(string) string
	// Public validates declarations without reading any environment credential:
	// every Secret field stays empty, so callers must project only public
	// metadata and never pass the templates to rendering.
	Public bool
}

// Load reads, decodes and validates every *.yaml template in dir, then
// validates the set as a whole.
func Load(dir string, opts Options) ([]Template, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading templates directory %s: %w", dir, err)
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".yaml") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no .yaml templates found in %s", dir)
	}

	var tmpls []Template
	var errs []error
	for _, p := range paths {
		tmpl, err := loadFile(p, opts)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tmpls = append(tmpls, tmpl)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := ValidateTemplates(tmpls); err != nil {
		return nil, err
	}
	return tmpls, nil
}

func loadFile(path string, opts Options) (Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Template{}, fmt.Errorf("reading template %s: %w", path, err)
	}
	doc, err := parseSimpleYAML(string(data))
	if err != nil {
		return Template{}, fmt.Errorf("parsing template %s: %w", path, err)
	}

	d := &decoder{path: path, doc: doc}
	var tmpl Template
	d.root(&tmpl)
	d.guard(&tmpl)
	d.persona(&tmpl)
	d.llm(&tmpl)
	d.review(&tmpl)
	d.critic(&tmpl)
	d.writer(&tmpl)
	d.limits(&tmpl)
	d.learning(&tmpl)
	d.kpi(&tmpl)
	d.schedule(&tmpl)
	d.trends(&tmpl)
	d.targets(&tmpl)
	d.study(&tmpl)
	d.inbox(&tmpl)
	d.threads(&tmpl)
	d.telegram(&tmpl)
	d.secrets(&tmpl, opts)

	if len(d.errs) > 0 {
		return Template{}, errors.Join(d.errs...)
	}
	return tmpl, nil
}

// yamlNode retains mapping boundaries, including arbitrary keys containing dots.
// The flat scalar/list views remain available to the template loader.
type yamlNode struct {
	children map[string]*yamlNode
	value    string
	raw      string
	kind     string
}

type yamlDoc struct {
	keys    map[string]bool
	scalars map[string]string
	lists   map[string][]string
	root    *yamlNode
}

func parseSimpleYAML(input string) (yamlDoc, error) {
	doc := yamlDoc{
		keys:    make(map[string]bool),
		scalars: make(map[string]string),
		lists:   make(map[string][]string),
		root:    &yamlNode{kind: "mapping", children: make(map[string]*yamlNode)},
	}
	type frame struct {
		node        *yamlNode
		indent      int
		childIndent int
		path        string
	}
	stack := []frame{{node: doc.root, indent: -1, childIndent: 0}}
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	var previousField string
	var block *yamlNode
	var blockKey string
	var blockIndent int
	var blockLines []string
	flushBlock := func() {
		if block != nil {
			block.value = strings.TrimSpace(strings.Join(blockLines, " "))
			doc.scalars[blockKey] = block.value
			block = nil
			blockLines = nil
		}
	}
	for lineNo, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		indent := len(rawLine) - len(strings.TrimLeft(rawLine, " "))
		if block != nil {
			if trimmed == "" {
				continue
			}
			if indent > blockIndent {
				blockLines = append(blockLines, trimmed)
				continue
			}
			flushBlock()
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.ContainsAny(rawLine[:len(rawLine)-len(strings.TrimLeft(rawLine, " \t"))], "\t") {
			return yamlDoc{}, fmt.Errorf("line %d: tabs are not valid indentation", lineNo+1)
		}
		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		parent := &stack[len(stack)-1]
		isList := strings.HasPrefix(trimmed, "- ") || trimmed == "-"
		if isList {
			if parent.path == "" || indent <= parent.indent || len(parent.node.children) != 0 {
				return yamlDoc{}, fmt.Errorf("line %d: unexpected list item", lineNo+1)
			}
			if parent.childIndent >= 0 && indent != parent.childIndent {
				return yamlDoc{}, fmt.Errorf("line %d: inconsistent indentation for %s", lineNo+1, parent.path)
			}
			parent.childIndent = indent
			parent.node.kind = "list"
			itemText := unquoteScalar(stripInlineComment(strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))))
			if itemText != "" {
				doc.lists[parent.path] = append(doc.lists[parent.path], itemText)
			}
			continue
		}
		colon := yamlKeyColon(trimmed)
		if colon <= 0 {
			return yamlDoc{}, fmt.Errorf("line %d: expected key: value", lineNo+1)
		}
		key := unquoteScalar(strings.TrimSpace(trimmed[:colon]))
		fullKey := key
		if parent.path != "" {
			fullKey = parent.path + "." + key
		}
		if key == "" {
			return yamlDoc{}, fmt.Errorf("line %d: empty mapping key", lineNo+1)
		}
		if parent.node.kind == "list" || (parent.childIndent >= 0 && indent != parent.childIndent) {
			return yamlDoc{}, fmt.Errorf("line %d: unexpected indentation for %s after %s", lineNo+1, fullKey, previousField)
		}
		parent.childIndent = indent
		previousField = fullKey
		if _, exists := parent.node.children[key]; exists {
			return yamlDoc{}, fmt.Errorf("line %d: duplicate field %s", lineNo+1, fullKey)
		}
		rest := strings.TrimSpace(stripInlineComment(trimmed[colon+1:]))
		node := &yamlNode{kind: "scalar", value: unquoteScalar(rest), raw: rest}
		parent.node.children[key] = node
		doc.keys[fullKey] = true
		if rest == "" {
			node.kind = "mapping"
			node.children = make(map[string]*yamlNode)
			stack = append(stack, frame{node: node, indent: indent, childIndent: -1, path: fullKey})
		} else if rest == ">" || rest == "|" {
			block, blockKey, blockIndent = node, fullKey, indent
		} else {
			doc.scalars[fullKey] = node.value
		}
	}
	flushBlock()
	return doc, nil
}

// yamlKeyColon ignores colons inside quoted mapping keys.
func yamlKeyColon(s string) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
		} else if ch == '\'' || ch == '"' {
			quote = ch
		} else if ch == ':' {
			return i
		}
	}
	return -1
}

func stripInlineComment(s string) string {
	var inSingle, inDouble bool
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
		} else if ch == '"' && !inSingle {
			inDouble = !inDouble
		} else if ch == '#' && !inSingle && !inDouble {
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return strings.TrimSpace(s)
}

func unquoteScalar(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
