package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	SeedPlaybook []string
	LLM          LLMConfig
	Limits       LimitsConfig
	Threads      ThreadsConfig
	Telegram     TelegramConfig
	DryRun       bool
}

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
	ProfileDir string
}

type TelegramConfig struct {
	BotTokenEnv    string
	BotToken       Secret
	OwnerChatIDEnv string
	OwnerChatID    Secret
}

// LoadDir loads and validates all *.yaml templates in dir.
func LoadDir(dir string, getenv func(string) string) ([]Template, error) {
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
		tmpl, err := LoadFile(p, getenv)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tmpls = append(tmpls, tmpl)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return tmpls, nil
}

// LoadFile loads a template from path, validates it against spec section 16,
// and resolves secrets using getenv.
func LoadFile(path string, getenv func(string) string) (Template, error) {
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

	tmpl.LLM.BaseURL = requireString("llm.base_url")
	tmpl.LLM.Model = requireString("llm.model")
	tmpl.LLM.APIKeyEnv = requireString("llm.api_key_env")
	tmpl.LLM.PricePerMTokInUSD = requireFloat("llm.price_per_mtok_in_usd", true)
	tmpl.LLM.PricePerMTokOutUSD = requireFloat("llm.price_per_mtok_out_usd", true)

	tmpl.Limits.PostsPerDay = requirePositiveInt("limits.posts_per_day")
	tmpl.Limits.RepliesPerDay = requirePositiveInt("limits.replies_per_day")
	tmpl.Limits.RepliesPerUser7d = requirePositiveInt("limits.replies_per_user_7d")
	tmpl.Limits.MinSecondsBetweenActions = requirePositiveInt("limits.min_seconds_between_actions")
	tmpl.Limits.MaxStepsPerWake = requirePositiveInt("limits.max_steps_per_wake")
	tmpl.Limits.MaxUSDPerDay = requireFloat("limits.max_usd_per_day", false)
	tmpl.Limits.MinMinutesBetweenWakes = requirePositiveInt("limits.min_minutes_between_wakes")

	tmpl.Threads.ProfileDir = requireString("threads.profile_dir")
	tmpl.Telegram.BotTokenEnv = requireString("telegram.bot_token_env")
	tmpl.Telegram.OwnerChatIDEnv = requireString("telegram.owner_chat_id_env")

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

	tmpl.LLM.APIKey = resolveSecret("llm.api_key_env", tmpl.LLM.APIKeyEnv)
	tmpl.Telegram.BotToken = resolveSecret("telegram.bot_token_env", tmpl.Telegram.BotTokenEnv)
	tmpl.Telegram.OwnerChatID = resolveSecret("telegram.owner_chat_id_env", tmpl.Telegram.OwnerChatIDEnv)

	if len(errs) > 0 {
		return Template{}, errors.Join(errs...)
	}

	return tmpl, nil
}

type yamlDoc struct {
	scalars map[string]string
	lists   map[string][]string
}

func parseSimpleYAML(input string) (yamlDoc, error) {
	doc := yamlDoc{
		scalars: make(map[string]string),
		lists:   make(map[string][]string),
	}
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")

	var section string
	var listKey string
	var listIndent int
	var blockKey string
	var blockLines []string
	var blockIndent int

	flushBlock := func() {
		if blockKey != "" {
			doc.scalars[blockKey] = strings.TrimSpace(strings.Join(blockLines, " "))
			blockKey = ""
			blockLines = nil
			blockIndent = 0
		}
	}

	for lineNo, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		indent := len(rawLine) - len(strings.TrimLeft(rawLine, " "))

		if blockKey != "" {
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

		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			if listKey == "" || indent <= listIndent {
				return yamlDoc{}, fmt.Errorf("line %d: unexpected list item", lineNo+1)
			}
			itemText := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			itemText = unquoteScalar(stripInlineComment(itemText))
			if itemText != "" {
				doc.lists[listKey] = append(doc.lists[listKey], itemText)
			}
			continue
		}

		listKey = ""
		listIndent = 0

		colon := strings.IndexByte(trimmed, ':')
		if colon <= 0 {
			return yamlDoc{}, fmt.Errorf("line %d: expected key: value", lineNo+1)
		}
		key := strings.TrimSpace(trimmed[:colon])
		rest := strings.TrimSpace(stripInlineComment(trimmed[colon+1:]))

		if indent == 0 {
			section = ""
			if rest == "" {
				section = key
				listKey = key
				listIndent = indent
				continue
			}
			if rest == ">" || rest == "|" {
				blockKey = key
				blockIndent = indent
				continue
			}
			doc.scalars[key] = unquoteScalar(rest)
		} else {
			if section == "" {
				return yamlDoc{}, fmt.Errorf("line %d: unexpected indentation for %q", lineNo+1, key)
			}
			fullKey := section + "." + key
			if rest == "" {
				listKey = fullKey
				listIndent = indent
				continue
			}
			if rest == ">" || rest == "|" {
				blockKey = fullKey
				blockIndent = indent
				continue
			}
			doc.scalars[fullKey] = unquoteScalar(rest)
		}
	}
	flushBlock()
	return doc, nil
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
