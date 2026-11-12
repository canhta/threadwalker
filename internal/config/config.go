package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
	// The owner's zone is a template field; embedding zone data keeps it
	// resolvable on minimal hosts without a system tzdata package.
	_ "time/tzdata"
)

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

// Template represents an agent template loaded from YAML (spec section 16).
type Template struct {
	ID           string
	Goal         string
	Persona      PersonaConfig
	Guard        GuardConfig
	SeedPlaybook []string
	LLM          LLMConfig
	Limits       LimitsConfig
	Threads      ThreadsConfig
	Telegram     TelegramConfig
	DryRun       bool
}

// GuardConfig configures harness-only stop-request matching. Omitted phrases
// select the built-in Vietnamese and English defaults.
type GuardConfig struct{ StopPhrases []string }

type PersonaConfig struct {
	Name             string
	Language         string
	BioMustInclude   string
	Identity         string
	Voice            string
	ResearchFocus    []string
	StyleRules       []string
	Topics           []string
	AvoidTopics      []string
	GoodPosts        []string
	GoodReplies      []string
	BadExamples      []string
	ForbiddenPhrases []string
}

type LLMConfig struct {
	Protocol           string
	BaseURL            string
	Model              string
	APIKeyEnv          string
	APIKey             Secret
	PricePerMTokInUSD  float64
	PricePerMTokOutUSD float64
}

type LimitsConfig struct {
	PostsPerDay              int
	RepliesPerDay            int
	RepliesPerUser7d         int
	MinSecondsBetweenActions int
	MaxStepsPerWake          int
	MaxUSDPerDay             float64
	MinMinutesBetweenWakes   int
}

type ThreadsConfig struct {
	Username         string // Operator-declared own account identity, never inferred.
	Backend          string // stub (default), browser or agent_browser
	BaseURL          string
	ExecutablePath   string // Chrome executable for either browser backend.
	AgentBrowserPath string // Pinned agent-browser CLI binary; agent_browser only.
	PageProfile      string // fixture (default) or threads for live read mapping.
	ProfileDir       string
	NoSandbox        bool // Explicit operator choice for constrained local hosts.
}

type TelegramConfig struct {
	DigestHour     int // UTC; defaults to 18 on loaded templates.
	Enabled        bool
	BotTokenEnv    string
	BotToken       Secret
	OwnerChatIDEnv string
	OwnerChatID    Secret
	// Timezone is the owner's IANA zone for displayed times; loaded default
	// Asia/Ho_Chi_Minh. Digest scheduling itself stays UTC.
	Timezone string
	// Messages overrides owner-facing copy by message kind. Values are
	// text/template strings rendered with persona name/language and exact facts,
	// so a persona's voice lives in its template, never in Go.
	Messages map[string]string
}

// TelegramMessageNames is the canonical set a template may override.
var TelegramMessageNames = []string{"digest", "status", "pause", "resume", "kill", "dnc", "help", "note_ack", "question_hint", "notice"}

// LoadDir loads and validates all *.yaml templates in dir.
func LoadDir(dir string, getenv func(string) string) ([]Template, error) {
	return LoadDirWithCatalog(dir, getenv, nil)
}

// LoadDirWithCatalog loads templates with an optional catalog for named LLM profiles.
func LoadDirWithCatalog(dir string, getenv func(string) string, catalog *Catalog) ([]Template, error) {
	return loadDir(dir, getenv, catalog, true)
}

func loadDir(dir string, getenv func(string) string, catalog *Catalog, secrets bool) ([]Template, error) {
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
		tmpl, err := loadFile(p, getenv, catalog, secrets)
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

// LoadFile loads a template from path, validates it against spec section 16,
// and resolves secrets using getenv.
func LoadFile(path string, getenv func(string) string) (Template, error) {
	return LoadFileWithCatalog(path, getenv, nil)
}

// LoadFileWithCatalog loads a template and resolves its selected LLM profile.
// A nil catalog preserves inline configuration and rejects profile selection.
func LoadFileWithCatalog(path string, getenv func(string) string, catalog *Catalog) (Template, error) {
	return loadFile(path, getenv, catalog, true)
}

func loadFile(path string, getenv func(string) string, catalog *Catalog, secrets bool) (Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Template{}, fmt.Errorf("reading template %s: %w", path, err)
	}
	doc, err := parseSimpleYAML(string(data))
	if err != nil {
		return Template{}, fmt.Errorf("parsing template %s: %w", path, err)
	}

	var errs []error
	addErr := func(field, msg string) {
		errs = append(errs, fmt.Errorf("template %s: field %s: %s", path, field, msg))
	}

	requireString := func(field string) string {
		val := strings.TrimSpace(doc.scalars[field])
		if val == "" {
			addErr(field, "required field is missing")
		}
		return val
	}

	requireStringList := func(field string) []string {
		if list, ok := doc.lists[field]; ok && len(list) > 0 {
			return list
		}
		addErr(field, "required field is missing")
		return nil
	}

	requirePositiveInt := func(field string) int {
		raw, ok := doc.scalars[field]
		if !ok || strings.TrimSpace(raw) == "" {
			addErr(field, "required field is missing")
			return 0
		}
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			addErr(field, fmt.Sprintf("must be an integer: %v", err))
			return 0
		}
		if n <= 0 {
			addErr(field, fmt.Sprintf("must be positive, got %d", n))
		}
		return n
	}

	requireFloat := func(field string, allowZero bool) float64 {
		raw, ok := doc.scalars[field]
		if !ok || strings.TrimSpace(raw) == "" {
			addErr(field, "required field is missing")
			return 0
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			addErr(field, fmt.Sprintf("must be a number: %v", err))
			return 0
		}
		if allowZero && f < 0 {
			addErr(field, fmt.Sprintf("must be non-negative, got %v", f))
		} else if !allowZero && f <= 0 {
			addErr(field, fmt.Sprintf("must be positive, got %v", f))
		}
		return f
	}

	var tmpl Template
	tmpl.ID = requireString("id")
	if doc.keys["guard.stop_phrases"] {
		tmpl.Guard.StopPhrases = requireStringList("guard.stop_phrases")
		for _, phrase := range tmpl.Guard.StopPhrases {
			if strings.TrimSpace(phrase) == "" {
				addErr("guard.stop_phrases", "phrases must be nonempty")
			}
		}
	}
	tmpl.Goal = requireString("goal")
	tmpl.Persona.Name = requireString("persona.name")
	tmpl.Persona.Language = requireString("persona.language")
	tmpl.Persona.BioMustInclude = requireString("persona.bio_must_include")
	tmpl.Persona.Identity = requireString("persona.identity")
	tmpl.Persona.Voice = requireString("persona.voice")
	tmpl.Persona.ResearchFocus = requireStringList("persona.research_focus")
	tmpl.Persona.StyleRules = requireStringList("persona.style_rules")
	tmpl.Persona.Topics = requireStringList("persona.topics")
	tmpl.Persona.AvoidTopics = requireStringList("persona.avoid_topics")
	tmpl.Persona.GoodPosts = requireStringList("persona.good_posts")
	tmpl.Persona.GoodReplies = requireStringList("persona.good_replies")
	tmpl.Persona.BadExamples = requireStringList("persona.bad_examples")
	tmpl.Persona.ForbiddenPhrases = requireStringList("persona.forbidden_phrases")
	tmpl.SeedPlaybook = requireStringList("seed_playbook")

	hasProfile := doc.keys["llm.profile"]
	if hasProfile {
		profile := requireString("llm.profile")
		ambiguous := false
		for field := range doc.keys {
			if strings.HasPrefix(field, "llm.") && field != "llm.profile" {
				addErr(field, "cannot combine inline LLM fields with llm.profile")
				ambiguous = true
			}
		}
		if catalog == nil {
			addErr("llm.profile", "selecting a profile requires an LLM catalog")
		} else if profile != "" && !ambiguous {
			resolved, err := catalog.resolve(profile, getenv, secrets)
			if err != nil {
				addErr("llm.profile", err.Error())
			} else {
				tmpl.LLM = resolved
			}
		}
	} else {
		tmpl.LLM.Protocol = strings.TrimSpace(doc.scalars["llm.protocol"])
		if !doc.keys["llm.protocol"] {
			tmpl.LLM.Protocol = "openai"
		}
		if tmpl.LLM.Protocol != "openai" && tmpl.LLM.Protocol != "anthropic" {
			addErr("llm.protocol", "must be openai or anthropic")
		}
		tmpl.LLM.BaseURL = requireString("llm.base_url")
		tmpl.LLM.Model = requireString("llm.model")
		tmpl.LLM.APIKeyEnv = requireString("llm.api_key_env")
		tmpl.LLM.PricePerMTokInUSD = requireFloat("llm.price_per_mtok_in_usd", true)
		tmpl.LLM.PricePerMTokOutUSD = requireFloat("llm.price_per_mtok_out_usd", true)
	}

	tmpl.Limits.PostsPerDay = requirePositiveInt("limits.posts_per_day")
	tmpl.Limits.RepliesPerDay = requirePositiveInt("limits.replies_per_day")
	tmpl.Limits.RepliesPerUser7d = requirePositiveInt("limits.replies_per_user_7d")
	tmpl.Limits.MinSecondsBetweenActions = requirePositiveInt("limits.min_seconds_between_actions")
	tmpl.Limits.MaxStepsPerWake = requirePositiveInt("limits.max_steps_per_wake")
	tmpl.Limits.MaxUSDPerDay = requireFloat("limits.max_usd_per_day", false)
	tmpl.Limits.MinMinutesBetweenWakes = requirePositiveInt("limits.min_minutes_between_wakes")
	if tmpl.Limits.MinMinutesBetweenWakes > 1440 {
		addErr("limits.min_minutes_between_wakes", "must be at most 1440")
	}

	tmpl.Threads.ProfileDir = requireString("threads.profile_dir")
	tmpl.Threads.Username = strings.TrimSpace(doc.scalars["threads.username"])
	if tmpl.Threads.Username != "" {
		normalized, err := normalizeOwnUsername(tmpl.Threads.Username)
		if err != nil {
			addErr("threads.username", "must be a valid account username")
		} else {
			tmpl.Threads.Username = normalized
		}
	}
	tmpl.Threads.Backend = "stub"
	if doc.keys["threads.backend"] {
		tmpl.Threads.Backend = requireString("threads.backend")
	}
	switch tmpl.Threads.Backend {
	case "stub":
	case "browser", "agent_browser":
		tmpl.Threads.BaseURL = requireString("threads.base_url")
		tmpl.Threads.ExecutablePath = requireString("threads.executable_path")
		u, err := url.Parse(tmpl.Threads.BaseURL)
		if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
			addErr("threads.base_url", "must be an HTTP(S) origin without credentials, path, query or fragment")
		}
		if tmpl.Threads.Backend == "agent_browser" {
			tmpl.Threads.AgentBrowserPath = requireString("threads.agent_browser_path")
			if !filepath.IsAbs(tmpl.Threads.AgentBrowserPath) || strings.ContainsAny(tmpl.Threads.AgentBrowserPath, "\x00\r\n") {
				addErr("threads.agent_browser_path", "must be an absolute executable path")
			}
		}
	default:
		addErr("threads.backend", "must be stub, browser or agent_browser")
	}
	tmpl.Threads.PageProfile = "fixture"
	if doc.keys["threads.page_profile"] {
		tmpl.Threads.PageProfile = requireString("threads.page_profile")
	}
	switch tmpl.Threads.PageProfile {
	case "fixture":
	case "threads":
		if tmpl.Threads.Backend != "agent_browser" {
			addErr("threads.page_profile", "threads requires backend agent_browser")
		}
		u, err := url.Parse(tmpl.Threads.BaseURL)
		host := ""
		if err == nil && u != nil {
			host = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		}
		if err != nil || u == nil || u.Scheme != "https" || (host != "threads.com" && host != "threads.net") {
			addErr("threads.page_profile", "threads requires an HTTPS threads.com or threads.net base_url")
		}
	default:
		addErr("threads.page_profile", "must be fixture or threads")
	}
	if doc.keys["threads.no_sandbox"] {
		value, err := strconv.ParseBool(doc.scalars["threads.no_sandbox"])
		if err != nil {
			addErr("threads.no_sandbox", "must be a boolean")
		} else {
			tmpl.Threads.NoSandbox = value
		}
	}
	if doc.keys["telegram.enabled"] {
		raw := doc.scalars["telegram.enabled"]
		enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			addErr("telegram.enabled", "must be a boolean")
		} else {
			tmpl.Telegram.Enabled = enabled
		}
	}
	tmpl.Telegram.DigestHour = 18
	if doc.keys["telegram.digest_hour"] {
		hour, err := strconv.Atoi(doc.scalars["telegram.digest_hour"])
		if err != nil || hour < 0 || hour > 23 {
			addErr("telegram.digest_hour", "must be an integer within 0..23 UTC")
		} else {
			tmpl.Telegram.DigestHour = hour
		}
	}
	tmpl.Telegram.Timezone = "Asia/Ho_Chi_Minh"
	if doc.keys["telegram.timezone"] {
		zone := strings.TrimSpace(doc.scalars["telegram.timezone"])
		if _, err := time.LoadLocation(zone); err != nil {
			addErr("telegram.timezone", "must be an IANA timezone name")
		} else {
			tmpl.Telegram.Timezone = zone
		}
	}
	tmpl.Telegram.Messages = map[string]string{}
	knownMessage := map[string]bool{}
	for _, name := range TelegramMessageNames {
		knownMessage[name] = true
	}
	for key := range doc.keys {
		if !strings.HasPrefix(key, "telegram.messages.") {
			continue
		}
		name := strings.TrimPrefix(key, "telegram.messages.")
		if strings.Contains(name, ".") || !knownMessage[name] {
			addErr(key, "unknown owner message name")
			continue
		}
		value := doc.scalars[key]
		if strings.TrimSpace(value) == "" {
			addErr(key, "message template must not be empty")
			continue
		}
		if _, err := template.New(name).Funcs(template.FuncMap{"nl": func() string { return "\n" }}).Parse(value); err != nil {
			addErr(key, "invalid message template")
			continue
		}
		tmpl.Telegram.Messages[name] = value
	}
	if tmpl.Telegram.Enabled {
		tmpl.Telegram.BotTokenEnv = requireString("telegram.bot_token_env")
		tmpl.Telegram.OwnerChatIDEnv = requireString("telegram.owner_chat_id_env")
	}

	if rawDryRun, ok := doc.scalars["dry_run"]; !ok || strings.TrimSpace(rawDryRun) == "" {
		addErr("dry_run", "required field is missing")
	} else {
		b, err := strconv.ParseBool(strings.TrimSpace(rawDryRun))
		if err != nil {
			addErr("dry_run", fmt.Sprintf("must be a boolean: %v", err))
		} else {
			tmpl.DryRun = b
		}
	}
	if tmpl.Threads.PageProfile == "threads" && !tmpl.DryRun {
		addErr("threads.page_profile", "threads live reads require dry_run true until write qualification")
	}

	resolveSecret := func(field, envName string) Secret {
		if envName == "" {
			return Secret{}
		}
		var val string
		if getenv != nil {
			val = getenv(envName)
		}
		if val == "" {
			addErr(field, fmt.Sprintf("environment variable %s is unset or empty", envName))
			return Secret{}
		}
		return NewSecret(val)
	}

	if !hasProfile && secrets {
		tmpl.LLM.APIKey = resolveSecret("llm.api_key_env", tmpl.LLM.APIKeyEnv)
	}
	if tmpl.Telegram.Enabled && secrets {
		tmpl.Telegram.BotToken = resolveSecret("telegram.bot_token_env", tmpl.Telegram.BotTokenEnv)
		tmpl.Telegram.OwnerChatID = resolveSecret("telegram.owner_chat_id_env", tmpl.Telegram.OwnerChatIDEnv)
	}

	if len(errs) > 0 {
		return Template{}, errors.Join(errs...)
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
