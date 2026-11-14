package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/stdx"
)

// decoder accumulates every field error of one template file, so an operator
// sees all of them at once. Each section method decodes and validates one
// template section in place.
type decoder struct {
	path string
	doc  yamlDoc
	errs []error
}

func (d *decoder) fail(field, msg string) {
	d.errs = append(d.errs, fmt.Errorf("template %s: field %s: %s", d.path, field, msg))
}

func (d *decoder) has(field string) bool { return d.doc.keys[field] }

func (d *decoder) str(field string) string {
	val := strings.TrimSpace(d.doc.scalars[field])
	if val == "" {
		d.fail(field, "required field is missing")
	}
	return val
}

func (d *decoder) list(field string) []string {
	if list, ok := d.doc.lists[field]; ok && len(list) > 0 {
		return list
	}
	d.fail(field, "required field is missing")
	return nil
}

func (d *decoder) posInt(field string) int {
	raw, ok := d.doc.scalars[field]
	if !ok || strings.TrimSpace(raw) == "" {
		d.fail(field, "required field is missing")
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		d.fail(field, fmt.Sprintf("must be an integer: %v", err))
		return 0
	}
	if n <= 0 {
		d.fail(field, fmt.Sprintf("must be positive, got %d", n))
	}
	return n
}

func (d *decoder) float(field string, allowZero bool) float64 {
	raw, ok := d.doc.scalars[field]
	if !ok || strings.TrimSpace(raw) == "" {
		d.fail(field, "required field is missing")
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		d.fail(field, fmt.Sprintf("must be a number: %v", err))
		return 0
	}
	if allowZero && f < 0 {
		d.fail(field, fmt.Sprintf("must be non-negative, got %v", f))
	} else if !allowZero && f <= 0 {
		d.fail(field, fmt.Sprintf("must be positive, got %v", f))
	}
	return f
}

// nonEmpty fails when any element is blank.
func (d *decoder) nonEmpty(field string, items []string, what string) {
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			d.fail(field, what+" must be nonempty")
		}
	}
}

func (d *decoder) root(t *Template) {
	t.ID = d.str("id")
	t.Goal = d.str("goal")
	if raw, ok := d.doc.scalars["dry_run"]; !ok || strings.TrimSpace(raw) == "" {
		d.fail("dry_run", "required field is missing")
	} else if b, err := strconv.ParseBool(strings.TrimSpace(raw)); err != nil {
		d.fail("dry_run", fmt.Sprintf("must be a boolean: %v", err))
	} else {
		t.DryRun = b
	}
}

func (d *decoder) guard(t *Template) {
	if d.has("guard.stop_phrases") {
		t.Guard.StopPhrases = d.list("guard.stop_phrases")
		d.nonEmpty("guard.stop_phrases", t.Guard.StopPhrases, "phrases")
	}
	if d.has("guard.blocked_phrases") {
		t.Guard.BlockedPhrases = d.list("guard.blocked_phrases")
		d.nonEmpty("guard.blocked_phrases", t.Guard.BlockedPhrases, "phrases")
	}
	t.Guard.TrendAnchor = d.boolean("guard.trend_anchor")
	t.Guard.ReplyMaxRunes = d.boundedInt("guard.reply_max_runes", 0, 0, stdx.MaxThreadsPostRunes)
	if d.has("guard.self_stat_units") {
		t.Guard.SelfStatUnits = d.list("guard.self_stat_units")
		d.nonEmpty("guard.self_stat_units", t.Guard.SelfStatUnits, "units")
	}
}

func (d *decoder) persona(t *Template) {
	p := &t.Persona
	p.Name = d.str("persona.name")
	p.Language = d.str("persona.language")
	p.BioMustInclude = d.str("persona.bio_must_include")
	p.Identity = d.str("persona.identity")
	p.Voice = d.str("persona.voice")
	p.Audience = d.str("persona.audience")
	p.CriticRules = d.list("persona.critic_rules")
	p.ResearchFocus = d.list("persona.research_focus")
	p.StyleRules = d.list("persona.style_rules")
	p.Topics = d.list("persona.topics")
	p.AvoidTopics = d.list("persona.avoid_topics")
	p.GoodPosts = d.list("persona.good_posts")
	p.GoodReplies = d.list("persona.good_replies")
	p.BadExamples = d.list("persona.bad_examples")
	p.StyleAvoid = d.list("persona.style_avoid")
	folded := map[string]bool{}
	for _, phrase := range t.Guard.BlockedPhrases {
		folded[FoldCase(strings.TrimSpace(phrase))] = true
	}
	for _, phrase := range p.StyleAvoid {
		if folded[FoldCase(strings.TrimSpace(phrase))] {
			d.fail("persona.style_avoid", fmt.Sprintf("%q is also under guard.blocked_phrases; a phrase is either a hard block or a style preference", phrase))
		}
	}
}

// llm reads the inline llm block, the only way a template configures its LLM.
func (d *decoder) llm(t *Template) {
	d.llmBlock("llm", &t.LLM)
}

// critic reads the optional critic.llm block; without it the critic uses the agent's own llm.
func (d *decoder) critic(t *Template) {
	if d.has("critic.llm") {
		d.llmBlock("critic.llm", &t.Critic)
	}
}

// writer reads the optional writer.llm block. Unlike the critic it has no
// fallback: without the block the agent's own text is published as written.
func (d *decoder) writer(t *Template) {
	if d.has("writer.llm") {
		d.llmBlock("writer.llm", &t.Writer)
	}
}

func (d *decoder) llmBlock(prefix string, l *LLMConfig) {
	if d.has(prefix + ".profile") {
		d.fail(prefix+".profile", "catalog profiles are not supported; declare the llm block inline")
	}
	l.Protocol = strings.TrimSpace(d.doc.scalars[prefix+".protocol"])
	if !d.has(prefix + ".protocol") {
		l.Protocol = "openai"
	}
	if l.Protocol != "openai" && l.Protocol != "anthropic" {
		d.fail(prefix+".protocol", "must be openai or anthropic")
	}
	l.BaseURL = d.str(prefix + ".base_url")
	l.Model = d.str(prefix + ".model")
	l.APIKeyEnv = d.str(prefix + ".api_key_env")
	l.PricePerMTokInUSD = d.float(prefix+".price_per_mtok_in_usd", true)
	l.PricePerMTokOutUSD = d.float(prefix+".price_per_mtok_out_usd", true)
	if d.has(prefix + ".cached_input_per_mtok") {
		c := d.float(prefix+".cached_input_per_mtok", true)
		l.PricePerMTokCachedInUSD = &c
	}
	l.ReasoningEffort = strings.TrimSpace(d.doc.scalars[prefix+".reasoning_effort"])
}

func (d *decoder) limits(t *Template) {
	l := &t.Limits
	l.PostsPerDay = d.posInt("limits.posts_per_day")
	l.RepliesPerDay = d.posInt("limits.replies_per_day")
	l.RepliesPerUser7d = d.posInt("limits.replies_per_user_7d")
	l.RepliesPerConversation = d.boundedInt("limits.replies_per_conversation", 2, 0, 100)
	l.ConversationHotLikes = d.boundedInt("limits.conversation_hot_likes", 5, 1, 1000000)
	l.ConversationHotReplies = d.boundedInt("limits.conversation_hot_replies", 3, 1, 1000000)
	l.BrowserRepliesPerDay = 5
	if d.has("limits.browser_replies_per_day") {
		l.BrowserRepliesPerDay = d.posInt("limits.browser_replies_per_day")
	}
	l.RepliesPerHour = d.boundedInt("limits.replies_per_hour", 6, 0, 1000)
	l.BreakerMinLikes = d.boundedInt("limits.breaker_min_likes", 2, 0, 1000000)
	l.BreakerActions = d.boundedInt("limits.breaker_actions", 15, 1, 100)
	l.BreakerHours = d.boundedInt("limits.breaker_hours", 6, 1, 168)
	l.MinSecondsBetweenActions = d.posInt("limits.min_seconds_between_actions")
	if d.has("limits.min_minutes_between_posts") {
		l.MinMinutesBetweenPosts = d.posInt("limits.min_minutes_between_posts")
	}
	l.MaxStepsPerWake = d.posInt("limits.max_steps_per_wake")
	l.MaxUSDPerDay = d.float("limits.max_usd_per_day", false)
	l.MinMinutesBetweenWakes = d.posInt("limits.min_minutes_between_wakes")
	if l.MinMinutesBetweenWakes > 1440 {
		d.fail("limits.min_minutes_between_wakes", "must be at most 1440")
	}
	l.MaxUSDPerDream = 0.30
	if d.has("limits.max_usd_per_dream") {
		l.MaxUSDPerDream = d.float("limits.max_usd_per_dream", false)
	}
	l.MaxStepsPerDream = 8
	if d.has("limits.max_steps_per_dream") {
		l.MaxStepsPerDream = d.posInt("limits.max_steps_per_dream")
	}
	l.CriticRejectionsPerWake = d.boundedInt("limits.critic_rejections_per_wake", 2, 1, 20)
}

// kpi reads the optional kpi block; once any key is present all four are required.
func (d *decoder) kpi(t *Template) {
	if !(d.has("kpi.engagements_per_day") || d.has("kpi.followers_per_week") || d.has("kpi.max_hours_without_publish") || d.has("kpi.on_target_minutes_between_wakes")) {
		return
	}
	k := &t.KPI
	k.EngagementsPerDay = d.posInt("kpi.engagements_per_day")
	k.FollowersPerWeek = d.posInt("kpi.followers_per_week")
	k.MaxHoursWithoutPublish = d.posInt("kpi.max_hours_without_publish")
	k.OnTargetMinutes = d.posInt("kpi.on_target_minutes_between_wakes")
	if k.OnTargetMinutes > 1440 {
		d.fail("kpi.on_target_minutes_between_wakes", "must be at most 1440")
	}
	if k.OnTargetMinutes < t.Limits.MinMinutesBetweenWakes {
		d.fail("kpi.on_target_minutes_between_wakes", "must be at least limits.min_minutes_between_wakes")
	}
}

func (d *decoder) timezone(field, def string) string {
	if !d.has(field) {
		return def
	}
	zone := strings.TrimSpace(d.doc.scalars[field])
	if _, err := time.LoadLocation(zone); err != nil {
		d.fail(field, "must be an IANA timezone name")
		return def
	}
	return zone
}

func (d *decoder) clock(field, raw string) string {
	parsed, err := time.Parse("15:04", strings.TrimSpace(raw))
	if err != nil {
		d.fail(field, "must be a 24-hour HH:MM local time")
		return ""
	}
	return parsed.Format("15:04")
}

func (d *decoder) schedule(t *Template) {
	t.Schedule.Timezone = d.timezone("schedule.timezone", "Asia/Ho_Chi_Minh")
	t.Limits.Timezone = t.Schedule.Timezone
	if d.has("schedule.dream_at") {
		t.Schedule.DreamAt = d.clock("schedule.dream_at", d.doc.scalars["schedule.dream_at"])
	}
	if d.has("schedule.quiet_from") != d.has("schedule.quiet_until") {
		d.fail("schedule.quiet_from", "set both quiet_from and quiet_until, or neither")
	} else if d.has("schedule.quiet_from") {
		t.Schedule.QuietFrom = d.clock("schedule.quiet_from", d.doc.scalars["schedule.quiet_from"])
		t.Schedule.QuietUntil = d.clock("schedule.quiet_until", d.doc.scalars["schedule.quiet_until"])
		if t.Schedule.QuietFrom == t.Schedule.QuietUntil && t.Schedule.QuietFrom != "" {
			d.fail("schedule.quiet_until", "must differ from quiet_from")
		}
	}
}

func (d *decoder) trends(t *Template) {
	if d.has("trends.sources") {
		t.Trends.Sources = d.sourceList("trends.sources")
	}
	if d.has("trends.include_keywords") {
		t.Trends.IncludeKeywords = d.list("trends.include_keywords")
		d.nonEmpty("trends.include_keywords", t.Trends.IncludeKeywords, "keywords")
	}
	if d.has("trends.avoid_keywords") {
		t.Trends.AvoidKeywords = d.list("trends.avoid_keywords")
		d.nonEmpty("trends.avoid_keywords", t.Trends.AvoidKeywords, "keywords")
	}
}

func (d *decoder) targets(t *Template) {
	t.Targets = TargetsConfig{
		MaxAgeMinutes:   d.boundedInt("targets.max_age_minutes", DefaultTargets.MaxAgeMinutes, 1, 10080),
		MaxReplies:      d.boundedInt("targets.max_replies", DefaultTargets.MaxReplies, 1, 100000),
		MinLikesPerHour: DefaultTargets.MinLikesPerHour,
		MinLikes:        d.boundedInt("targets.min_likes", DefaultTargets.MinLikes, 0, 1000000),
	}
	if d.has("targets.min_likes_per_hour") {
		t.Targets.MinLikesPerHour = d.float("targets.min_likes_per_hour", false)
	}
}

// boundedInt reads an optional integer that must lie within lo..hi.
func (d *decoder) boundedInt(field string, def, lo, hi int) int {
	if !d.has(field) {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(d.doc.scalars[field]))
	if err != nil || n < lo || n > hi {
		d.fail(field, fmt.Sprintf("must be an integer within %d..%d", lo, hi))
		return def
	}
	return n
}

func (d *decoder) boolean(field string) bool {
	if !d.has(field) {
		return false
	}
	v, err := strconv.ParseBool(strings.TrimSpace(d.doc.scalars[field]))
	if err != nil {
		d.fail(field, "must be a boolean")
	}
	return v
}

// sourceList reads a list of unique names from SourceNames.
func (d *decoder) sourceList(field string) []string {
	known := map[string]bool{}
	for _, name := range SourceNames {
		known[name] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, source := range d.list(field) {
		if !known[source] {
			d.fail(field, "unknown source "+source)
			continue
		}
		if !seen[source] {
			out = append(out, source)
			seen[source] = true
		}
	}
	return out
}

func (d *decoder) inbox(t *Template) {
	t.Inbox.PollMinutes = d.boundedInt("inbox.poll_minutes", 10, 0, 1440)
	t.Inbox.MinMinutesAfterWake = d.boundedInt("inbox.min_minutes_after_wake", 5, 1, 1440)
}

func (d *decoder) webSearch(t *Template) {
	s := &t.WebSearch
	s.Enabled = d.boolean("web_search.enabled")
	s.BaseURL = "https://api.openai.com/v1"
	if d.has("web_search.base_url") {
		raw := strings.TrimSpace(d.doc.scalars["web_search.base_url"])
		u, err := url.Parse(raw)
		if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			d.fail("web_search.base_url", "must be an HTTP(S) endpoint without query or fragment")
		} else {
			s.BaseURL = strings.TrimRight(raw, "/")
		}
	}
	s.Model = strings.TrimSpace(d.doc.scalars["web_search.model"])
	s.MaxResults = 5
	if d.has("web_search.max_results") {
		s.MaxResults = d.posInt("web_search.max_results")
		if s.MaxResults > 10 {
			d.fail("web_search.max_results", "must be at most 10")
		}
	}
	s.MaxOutput = 512
	if d.has("web_search.max_output_tokens") {
		s.MaxOutput = d.posInt("web_search.max_output_tokens")
		if s.MaxOutput > 1024 {
			d.fail("web_search.max_output_tokens", "must be at most 1024")
		}
	}
	if s.Enabled {
		if s.Model == "" {
			s.Model = t.LLM.Model
		}
		if t.LLM.Protocol != "openai" {
			d.fail("web_search.enabled", "requires the openai protocol")
		}
		if s.Model == "" {
			d.fail("web_search.model", "required when web search is enabled")
		}
	}
}

func (d *decoder) threads(t *Template) {
	th := &t.Threads
	th.ProfileDir = d.str("threads.profile_dir")
	if th.Username = d.str("threads.username"); th.Username != "" {
		normalized, err := normalizeOwnUsername(th.Username)
		if err != nil {
			d.fail("threads.username", "must be a valid account username")
		} else {
			th.Username = normalized
		}
	}
	th.Backend = "stub"
	if d.has("threads.backend") {
		th.Backend = d.str("threads.backend")
	}
	switch th.Backend {
	case "stub":
	case "api":
		th.AccessTokenEnv, th.UserIDEnv = "THREADS_ACCESS_TOKEN", "THREADS_USER_ID"
		if d.has("threads.access_token_env") {
			th.AccessTokenEnv = d.str("threads.access_token_env")
		}
		if d.has("threads.user_id_env") {
			th.UserIDEnv = d.str("threads.user_id_env")
		}
		th.BaseURL = d.str("threads.base_url")
		th.ExecutablePath = d.str("threads.executable_path")
		u, err := url.Parse(th.BaseURL)
		_, threadsHost := "", false
		if err == nil && u != nil {
			_, threadsHost = stdx.ThreadsHost(u.Hostname())
		}
		if err != nil || u == nil || u.Scheme != "https" || !threadsHost || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
			d.fail("threads.base_url", "must be an HTTPS threads.com or threads.net origin without credentials, path, query or fragment")
		}
		th.AgentBrowserPath = d.str("threads.agent_browser_path")
		if !filepath.IsAbs(th.AgentBrowserPath) || strings.ContainsAny(th.AgentBrowserPath, "\x00\r\n") {
			d.fail("threads.agent_browser_path", "must be an absolute executable path")
		}
	default:
		d.fail("threads.backend", "must be stub or api")
	}
	if d.has("threads.no_sandbox") {
		v, err := strconv.ParseBool(d.doc.scalars["threads.no_sandbox"])
		if err != nil {
			d.fail("threads.no_sandbox", "must be a boolean")
		} else {
			th.NoSandbox = v
		}
	}
}

func (d *decoder) telegram(t *Template) {
	tg := &t.Telegram
	tg.Enabled = d.boolean("telegram.enabled")
	tg.ReportHour = 21
	if d.has("telegram.report_hour") {
		hour, err := strconv.Atoi(d.doc.scalars["telegram.report_hour"])
		if err != nil || hour < 0 || hour > 23 {
			d.fail("telegram.report_hour", "must be an integer within 0..23")
		} else {
			tg.ReportHour = hour
		}
	}
	tg.Timezone = d.timezone("telegram.timezone", "Asia/Ho_Chi_Minh")
	if tg.Enabled {
		tg.BotTokenEnv = d.str("telegram.bot_token_env")
		tg.OwnerChatIDEnv = d.str("telegram.owner_chat_id_env")
	}
}

// secrets reads credentials from the environment.
func (d *decoder) secrets(t *Template, opts Options) {
	if opts.Public {
		return
	}
	resolve := func(field, envName string) Secret {
		if envName == "" {
			return Secret{}
		}
		var val string
		if opts.Getenv != nil {
			val = opts.Getenv(envName)
		}
		if val == "" {
			d.fail(field, fmt.Sprintf("environment variable %s is unset or empty", envName))
			return Secret{}
		}
		return NewSecret(val)
	}
	t.LLM.APIKey = resolve("llm.api_key_env", t.LLM.APIKeyEnv)
	if t.Critic.Model != "" {
		t.Critic.APIKey = resolve("critic.llm.api_key_env", t.Critic.APIKeyEnv)
	}
	if t.Writer.Model != "" {
		t.Writer.APIKey = resolve("writer.llm.api_key_env", t.Writer.APIKeyEnv)
	}
	if t.Threads.Backend == "api" {
		if opts.Getenv != nil {
			// The token seed is optional; a sealed OAuth token takes precedence.
			t.Threads.AccessToken = NewSecret(opts.Getenv(t.Threads.AccessTokenEnv))
			t.Threads.UserID = opts.Getenv(t.Threads.UserIDEnv)
		}
		if t.Threads.UserID == "" {
			d.fail("threads.user_id_env", fmt.Sprintf("environment variable %s is unset or empty", t.Threads.UserIDEnv))
		}
	}
	if t.Telegram.Enabled {
		t.Telegram.BotToken = resolve("telegram.bot_token_env", t.Telegram.BotTokenEnv)
		t.Telegram.OwnerChatID = resolve("telegram.owner_chat_id_env", t.Telegram.OwnerChatIDEnv)
	}
}
