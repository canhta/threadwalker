package guard

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/playbook"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

type actionIntent struct {
	Text           string   `json:"text"`
	TargetURL      string   `json:"target_url"`
	TargetID       string   `json:"target_id"`
	TargetUsername string   `json:"target_username"`
	Parts          []string `json:"parts"`
	Hook           string   `json:"hook"`
	TrendRef       string   `json:"trend_ref"`
	Rationale      string   `json:"rationale"`
	HypothesisIDs  []int64  `json:"hypothesis_ids"`
	Explore        bool     `json:"explore"`
	AnswersNote    int64    `json:"answers_note"`
	Angle          string   `json:"angle"`
	Fact           string   `json:"fact"`
	thread         string
	criticJSON     string
	detail         string
	// refTrace and tries are the operator's view of how the call resolved its
	// ref and which drafts the editor judged; they never reach the model.
	refTrace *store.RefTrace
	tries    []store.TryTrace
	browser  bool
	// inbox is true when the target is a reply to the account noted by NoteInbox.
	inbox bool
	// posting is harness-computed from stored observations.
	posting trends.PostingContext
}

// texts is every text the action publishes: the post or reply, then the later parts of a series.
func (i *actionIntent) texts() []string { return append([]string{i.Text}, i.Parts...) }

var mentions = regexp.MustCompile(`@[\p{L}\p{N}_.]+`)
var links = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://|(?:https?|ftp|mailto|data|javascript|file|tel):|www\.|//[\p{L}\p{N}]|[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?(?:\.[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?)+)`)

// NoteRead records the API post id and permalink the agent saw together in a
// read result this wake; a feed post has an empty id. The set is memory-only
// and dies with the guard; a reply may target only a pair in it, so a scored
// permalink cannot be paired with a different post id. The post's text is
// kept so a reply may quote its numbers.
func (g *Guard) NoteRead(id, raw, text string) {
	thread, err := store.CanonicalThread(raw)
	if err != nil {
		return
	}
	g.mu.Lock()
	g.read[thread] = id
	g.readText[thread] = text
	g.mu.Unlock()
}

// NoteInbox is NoteRead for a reply to the account: the agent may answer it by
// id, a conversation with the same person is not capped by
// limits.replies_per_user_7d, and the returned short ref names it.
func (g *Guard) NoteInbox(id, raw, text string) string {
	thread, err := store.CanonicalThread(raw)
	if err != nil {
		return ""
	}
	g.NoteRead(id, raw, text)
	g.mu.Lock()
	g.inbox[thread] = true
	g.mu.Unlock()
	refs := g.NoteTrend(raw)
	if len(refs) == 0 {
		return ""
	}
	return refs[0]
}

// IsOwn reports whether username is one of the operator-declared own accounts.
func (g *Guard) IsOwn(username string) bool {
	user, err := store.NormalizeUsername(username)
	return err == nil && g.own[user]
}

// NoteTrend records the URL or title of an item a trend, search or feed read
// returned this wake and returns the short ref ("r1", "r2", ...) of each, in
// noting order; noting the same item again returns the same ref. The set is
// memory-only; a post may anchor only to an item in it. Models pass the short
// ref because they miscopy long opaque URLs.
func (g *Guard) NoteTrend(refs ...string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	short := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref = strings.TrimSpace(ref); ref != "" {
			g.trends[ref] = true
			r, ok := g.shortRef[ref]
			if !ok {
				r = "r" + strconv.Itoa(len(g.shortRef)+1)
				g.shortRef[ref], g.refItem[r] = r, ref
			}
			short = append(short, r)
		}
	}
	return short
}

// NoteTrendText is NoteTrend for an item whose text the critic may read.
func (g *Guard) NoteTrendText(ref, text string) string {
	short := g.NoteTrend(ref)
	if len(short) == 0 {
		return ""
	}
	g.mu.Lock()
	g.trendText[strings.TrimSpace(ref)] = text
	g.mu.Unlock()
	return short[0]
}

// factKey identifies an item by its thread when it is a post, so a permalink
// copied with another host spelling or a query finds the facts of its read.
func factKey(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "/post/") {
		if canonical, err := store.CanonicalThread(ref); err == nil {
			return canonical
		}
	}
	return ref
}

// NoteFacts records the numbers a read showed for an item: its target score,
// or its topic heat. Seen shows them and an action built on the item stores them.
func (g *Guard) NoteFacts(ref string, f trends.ItemFacts) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.facts[factKey(ref)] = f
}

// overlayFacts replaces the stored-observation context of an action with what
// this wake's reads showed: the figures of the replied-to post and the heat of
// the anchored topic. Absent figures keep the observed ones.
func (g *Guard) overlayFacts(pc *trends.PostingContext, intent *actionIntent, tool string) {
	ref, ok := g.Trend(intent.TrendRef)
	if !ok {
		ref = intent.TrendRef
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if f, ok := g.facts[factKey(intent.TargetURL)]; ok && tool == "reply" && f.TargetScoreJSON != "" {
		pc.TargetScoreJSON = f.TargetScoreJSON
		if f.ParentReplies != nil {
			pc.ParentReplies = f.ParentReplies
		}
		if f.ParentAgeMinutes != nil {
			pc.ParentAgeMinutes = f.ParentAgeMinutes
		}
	}
	if f, ok := g.facts[factKey(ref)]; ok && f.TopicHeat != nil {
		pc.TopicHeat = f.TopicHeat
	}
}

// MaxSeen caps what Seen returns.
const MaxSeen = 40

// Seen lists the trend items and feed posts read this wake with their facts,
// best score first, at most MaxSeen. Items without facts follow, sorted by ref.
func (g *Guard) Seen() []quality.Item {
	g.mu.Lock()
	defer g.mu.Unlock()
	items := make([]quality.Item, 0, len(g.trendText))
	for ref, text := range g.trendText {
		f := g.facts[factKey(ref)]
		items = append(items, quality.Item{Ref: ref, Text: text, Facts: f.Line, Score: f.Score})
	}
	slices.SortFunc(items, func(a, b quality.Item) int {
		if a.Score != b.Score {
			return cmp.Compare(b.Score, a.Score)
		}
		return strings.Compare(a.Ref, b.Ref)
	})
	items = items[:min(len(items), MaxSeen)]
	for i := range items {
		items[i].URL, items[i].Ref = items[i].Ref, g.shortRef[items[i].Ref]
	}
	return items
}

// Trend returns the trend item noted this wake that ref names: its short ref,
// its exact text, or a near copy. Models copy
// long opaque URLs (Google News links, encoded queries) with a few characters
// wrong, so a ref also matches when it decodes to a noted ref or sits within a
// few edits of exactly one noted ref of similar length.
func (g *Guard) Trend(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if item, ok := g.refItem[strings.ToLower(ref)]; ok {
		return item, true
	}
	if g.trends[ref] {
		return ref, true
	}
	key := unescapeRef(ref)
	match := ""
	for seen := range g.trends {
		if unescapeRef(seen) == key {
			return seen, true
		}
		if len(key) >= 40 && abs(len(seen)-len(key)) <= maxRefEdits && editDistance(unescapeRef(seen), key) <= maxRefEdits {
			if match != "" {
				return "", false // ambiguous
			}
			match = seen
		}
	}
	return match, match != ""
}

var shortRefForm = regexp.MustCompile(`^(?i:r)[0-9]+$`)

// resolveRef maps a short ref to the item it names. isRef is true when raw has
// the form of a short ref; item is empty when this wake noted no such ref.
func (g *Guard) resolveRef(raw string) (item string, isRef bool) {
	raw = strings.TrimSpace(raw)
	if !shortRefForm.MatchString(raw) {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refItem[strings.ToLower(raw)], true
}

// maxRefEdits is how many characters a copied ref may differ from the original.
const maxRefEdits = 3

func unescapeRef(ref string) string {
	if u, err := url.QueryUnescape(ref); err == nil {
		return u
	}
	return ref
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// editDistance is the Levenshtein distance of a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// maxNearest bounds the candidates a RefTrace names.
const maxNearest = 3

// traceRef records how a given ref resolved. A ref that resolved to nothing
// names the noted items nearest to it, so a mangled copy can be compared with
// the original.
func (g *Guard) traceRef(field, given, resolved string) *store.RefTrace {
	t := &store.RefTrace{Field: field, Given: capRunes(given), Resolved: capRunes(resolved)}
	if resolved != "" {
		return t
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	t.Noted = len(g.shortRef)
	key := unescapeRef(strings.TrimSpace(given))
	for item, ref := range g.shortRef {
		t.Nearest = append(t.Nearest, store.NearRef{Ref: ref, Item: capRunes(item), Edits: editDistance(unescapeRef(item), key)})
	}
	slices.SortFunc(t.Nearest, func(a, b store.NearRef) int {
		return cmp.Or(cmp.Compare(a.Edits, b.Edits), strings.Compare(a.Ref, b.Ref))
	})
	t.Nearest = t.Nearest[:min(len(t.Nearest), maxNearest)]
	return t
}

// capRunes keeps a stored ref or text field within store.MaxArgField bytes.
func capRunes(s string) string {
	s, _ = stdx.TruncateUTF8(s, store.MaxArgField)
	return s
}

func (g *Guard) wasRead(thread, id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	seen, ok := g.read[thread]
	return ok && seen == id
}

// threadAuthor extracts the author from a canonical https://threads.<tld>/@author/post/<id> URL.
func threadAuthor(thread string) (author, reason string) {
	u, err := url.Parse(thread)
	if err != nil || !threadsHosts[u.Hostname()] {
		return "", "reply_target_host"
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "@") || parts[1] != "post" || parts[2] == "" {
		return "", "invalid_tool_arguments"
	}
	author, err = store.NormalizeUsername(parts[0])
	if err != nil {
		return "", "invalid_tool_arguments"
	}
	return author, ""
}

// content checks a post or reply. A draft the writer will rewrite skips the
// length checks; the rewritten text is checked in full (recheck).
func (g *Guard) content(a Attempt, draft bool) (*actionIntent, string) {
	if spec, ok := Spec(a.Tool); !ok || spec.Effect != EffectThreadsWrite {
		return nil, ""
	}
	var intent actionIntent
	if !utf8.ValidString(a.Arguments) || json.Unmarshal([]byte(a.Arguments), &intent) != nil || strings.TrimSpace(intent.Text) == "" {
		return nil, "invalid_tool_arguments"
	}
	if len(intent.Parts) >= store.MaxSeriesParts || (intent.Hook != "" && !slices.Contains(store.HookTypes, intent.Hook)) {
		return nil, "invalid_tool_arguments"
	}
	if a.Tool == "post" && g.opts.Template.Guard.TrendAnchor {
		first, _, _ := strings.Cut(strings.TrimSpace(intent.Text), "\n")
		if n := utf8.RuneCountInString(first); n > MaxHookRunes && !draft {
			intent.detail = strconv.Itoa(n) + " of " + strconv.Itoa(MaxHookRunes) + " in the first line"
			return &intent, "text_length"
		}
		if intent.Hook == "" || strings.TrimSpace(intent.Rationale) == "" || strings.ContainsAny(intent.Rationale, "\r\n") || utf8.RuneCountInString(intent.Rationale) > MaxRationaleRunes {
			return nil, "invalid_tool_arguments"
		}
		ref, ok := g.Trend(intent.TrendRef)
		intent.refTrace = g.traceRef("trend_ref", intent.TrendRef, ref)
		if !ok {
			return &intent, "no_trend_anchor"
		}
		intent.TrendRef = ref
	}
	for _, text := range intent.texts() {
		if strings.TrimSpace(text) == "" {
			return nil, "invalid_tool_arguments"
		}
		if n := utf8.RuneCountInString(text); n > 500 && !draft {
			intent.detail = strconv.Itoa(n) + " of 500"
			return &intent, "text_length"
		}
		if n, max := utf8.RuneCountInString(text), g.opts.Template.Guard.ReplyMaxRunes; a.Tool == "reply" && max > 0 && n > max && !draft {
			intent.detail = strconv.Itoa(n) + " of " + strconv.Itoa(max)
			return &intent, "text_length"
		}
		if reason, detail := contentBlock(g.opts.Template.Guard.BlockedPhrases, text); reason != "" {
			intent.detail = detail
			return &intent, reason
		}
		if g.ownInteraction(text) {
			return nil, "own_account"
		}
	}
	if a.Tool == "reply" {
		username, err := store.NormalizeUsername(intent.TargetUsername)
		if err != nil {
			return nil, "invalid_tool_arguments"
		}
		intent.TargetUsername = username
		if item, isRef := g.resolveRef(intent.TargetURL); isRef {
			intent.refTrace = g.traceRef("target_url", intent.TargetURL, item)
			if item == "" {
				return &intent, "reply_target_unread"
			}
			intent.TargetURL = item
		}
		if a.Target != nil {
			*a.Target = intent.TargetURL
		}
		if g.own[username] || g.ownURL(intent.TargetURL) {
			return nil, "own_account"
		}
		thread, err := store.CanonicalThread(intent.TargetURL)
		if err != nil {
			return nil, "invalid_tool_arguments"
		}
		intent.thread = thread
		author, reason := threadAuthor(thread)
		if reason != "" {
			return nil, reason
		}
		if author != username {
			return nil, "reply_target_mismatch"
		}
		// A reply without a post id answers a feed post, through the browser.
		intent.browser = intent.TargetID == ""
		g.mu.Lock()
		intent.inbox = g.inbox[thread]
		outside := g.facts[thread].Outside
		g.mu.Unlock()
		if !g.wasRead(thread, intent.TargetID) {
			return nil, "reply_target_unread"
		}
		if outside != "" && !intent.inbox {
			intent.detail = outside
			return &intent, reasonTargetOutsideWindow
		}
		// An author mention may contain dots; check URL syntax separately
		// from mention identity so mailto/HTTP links retain the URL rule ID.
		if links.MatchString(mentions.ReplaceAllString(intent.Text, "")) {
			return nil, "reply_url"
		}
		for _, mention := range mentions.FindAllString(intent.Text, -1) {
			normalized, err := store.NormalizeUsername(mention)
			if err != nil || normalized != username {
				return nil, "reply_mention"
			}
		}
	}
	l := g.opts.Template.Limits
	if l.PostsPerDay <= 0 || l.RepliesPerDay <= 0 || l.RepliesPerUser7d <= 0 || l.MinSecondsBetweenActions <= 0 {
		return nil, "invalid_policy"
	}
	return &intent, ""
}

// reserve runs after the attempted-call audit is committed. One writer
// transaction serializes check+intent+contact writes across guards, stores and
// processes.
// reserve runs every limit check and, unless checkOnly, records the intent. A
// checkOnly pass runs before the writer and critic so a write that a limit
// would refuse costs no model calls; it writes nothing.
func (g *Guard) reserve(ctx context.Context, a Attempt, intent *actionIntent, auditID int64, now time.Time, checkOnly bool) (id int64, reason string, err error) {
	agent := g.opts.Template.ID
	err = g.opts.Store.Reserve(ctx, func(tx store.ReservationTx) error {
		running, open, err := tx.AgentWakeOpen(ctx, agent, g.opts.WakeID)
		if err != nil {
			return err
		}
		if !running {
			reason = "agent_not_running"
		} else if !open {
			reason = "wake_not_open"
		}
		var actions []store.ReservedAction
		if reason == "" {
			if actions, err = tx.Actions(ctx, g.opts.Template.DryRun); err != nil {
				return err
			}
			reason = g.isolation(actions, intent)
		}
		if reason == "" {
			if reason, err = g.quotas(ctx, tx, actions, a.Tool, intent, now); err != nil {
				return err
			}
		}
		if reason == "" {
			reason, err = g.ownerNotes(ctx, tx, a.Tool, intent, now)
			if err == nil && reason == "" && a.Tool == "post" {
				reason, err = g.repeatTopic(ctx, tx, intent, now)
			}
			if err != nil {
				return err
			}
		}
		if reason == "" {
			seen := map[int64]bool{}
			for _, hypothesis := range intent.HypothesisIDs {
				if hypothesis <= 0 || seen[hypothesis] || len(intent.HypothesisIDs) > 5 {
					reason = "invalid_hypothesis"
					break
				}
				seen[hypothesis] = true
				valid, err := tx.TestingHypothesis(ctx, agent, hypothesis)
				if err != nil {
					return err
				}
				if !valid {
					reason = "invalid_hypothesis"
					break
				}
			}
		}
		if reason == "" && intent.Explore {
			total, explored, err := tx.ExploreCounts(ctx, agent, store.ExploreWindow)
			if err != nil {
				return err
			}
			// A new explore action is allowed only while explore actions stay within
			// limits.explore_percent of the newest store.ExploreWindow own actions,
			// counting the new one.
			if (explored+1)*100 > g.opts.Template.Limits.ExplorePercent*(total+1) {
				reason = "explore_budget"
			}
		}
		if checkOnly {
			return nil
		}
		if reason == "" {
			ids := intent.HypothesisIDs
			if ids == nil {
				ids = []int64{}
			}
			encoded, _ := json.Marshal(ids)
			if id, err = tx.InsertIntent(ctx, store.NewIntent{AgentID: agent, Kind: a.Tool, Text: intent.Text, TargetURL: intent.TargetURL, TargetUsername: intent.TargetUsername, Hook: intent.Hook, Rationale: intent.Rationale, TrendRef: strings.TrimSpace(intent.TrendRef), AnswersNote: intent.AnswersNote, CriticJSON: intent.criticJSON, Parts: intent.Parts, HypothesisIDsJSON: string(encoded), Explore: intent.Explore, TargetScoreJSON: intent.posting.TargetScoreJSON, DelayMinutes: intent.posting.DelayMinutes, TopicHeat: intent.posting.TopicHeat, ParentReplies: intent.posting.ParentReplies, ParentAgeMinutes: intent.posting.ParentAgeMinutes, DryRun: g.opts.Template.DryRun, Browser: intent.browser, At: now}); err != nil {
				return err
			}
			if err = playbook.Reserve(ctx, tx.Playbook(), agent, id, ids); err != nil {
				return err
			}
			if a.Tool == "reply" {
				if err = tx.AddReply(ctx, agent, intent.TargetUsername); err != nil {
					return err
				}
			}
		}
		if reason != "" {
			return tx.MarkBlocked(ctx, auditID, reason)
		}
		return nil
	})
	if err != nil {
		return 0, "", err
	}
	return id, reason, nil
}

// ownerNotes refuses a post while an owner note is open that it does not name,
// and any action that names a note that is not open.
func (g *Guard) ownerNotes(ctx context.Context, tx store.ReservationTx, kind string, intent *actionIntent, now time.Time) (string, error) {
	open, err := tx.OpenOwnerNotes(ctx, g.opts.Template.ID, now)
	if err != nil {
		return "", err
	}
	if intent.AnswersNote != 0 {
		for _, note := range open {
			if note.ID == intent.AnswersNote {
				return "", nil
			}
		}
		return "invalid_tool_arguments", nil
	}
	if kind == "post" && len(open) > 0 {
		return "owner_note_open", nil
	}
	return "", nil
}

// repeatTopic refuses a post that anchors to a trend one of the last 24 hours'
// posts anchored to, or whose first line shares most of its longer words with theirs.
func (g *Guard) repeatTopic(ctx context.Context, tx store.ReservationTx, intent *actionIntent, now time.Time) (string, error) {
	recent, err := tx.RecentPosts(ctx, g.opts.Template.ID, g.opts.Template.DryRun, now.Add(-24*time.Hour))
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(intent.TrendRef)
	hook := hookWords(intent.Text)
	for _, p := range recent {
		if (ref != "" && p.TrendRef == ref) || similarHooks(hook, hookWords(p.Text)) {
			return "repeat_topic", nil
		}
	}
	return "", nil
}

// hookWords is the set of lowercase words of four or more letters in the first line of a text, links left out.
func hookWords(text string) map[string]bool {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(links.ReplaceAllString(first, " ")), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(w) >= 4 {
			words[w] = true
		}
	}
	return words
}

// similarHooks is true when at least two words, and half of the smaller word set, are shared.
// ponytail: word overlap, not meaning; compare embeddings if paraphrases slip through.
func similarHooks(a, b map[string]bool) bool {
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return shared >= 2 && shared*2 >= min(len(a), len(b))
}

func (g *Guard) quotas(ctx context.Context, tx store.ReservationTx, actions []store.ReservedAction, kind string, intent *actionIntent, now time.Time) (string, error) {
	l := g.opts.Template.Limits
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := now.Add(-7 * 24 * time.Hour)
	posts, replies, browserReplies, contact := 0, 0, 0, 0
	threadUsed := false
	var latest, latestPost time.Time
	counts := map[string]int{}
	for _, row := range actions {
		if row.Agent != g.opts.Template.ID {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, row.PostedAt)
		if err != nil {
			return "", err
		}
		if at.After(latest) {
			latest = at
		}
		if row.Kind == "post" && at.After(latestPost) {
			latestPost = at
		}
		// Future persisted intents are counted conservatively after clock rollback.
		if !at.Before(day) && at.Before(day.Add(24*time.Hour)) {
			if row.Kind == "post" {
				posts++
			} else if row.Kind == "reply" {
				replies++
				if row.Browser {
					browserReplies++
				}
			}
		}
		if row.Kind != "reply" {
			continue
		}
		normalized, err := store.NormalizeUsername(row.TargetUsername)
		if err != nil {
			return "", errors.New("invalid stored contact")
		}
		inWindow := at.After(from) && !at.After(now)
		if normalized == intent.TargetUsername && inWindow {
			contact++
		}
		if inWindow {
			counts[normalized]++
		}
		// The target identifies the replied-to thread; thread_url, when
		// confirmed, identifies our new reply and must not weaken this quota.
		thread := row.ThreadURL
		if row.TargetURL != "" {
			thread = row.TargetURL
		}
		canonical, err := store.CanonicalThread(thread)
		if err != nil {
			return "", err
		}
		if !at.Before(day) && at.Before(day.Add(24*time.Hour)) && canonical == intent.thread {
			threadUsed = true
		}
	}
	// Refresh the materialized counters, preserving every permanent flag/reason.
	// All contact counters are derived from intents with exact Go timestamp parsing.
	if err := tx.ResetReplyCounts(ctx, g.opts.Template.ID); err != nil {
		return "", err
	}
	for user, n := range counts {
		if err := tx.SetReplyCount(ctx, g.opts.Template.ID, user, n); err != nil {
			return "", err
		}
	}
	if kind == "reply" {
		dnc, err := tx.DoNotContact(ctx, g.opts.Template.ID, intent.TargetUsername)
		if err != nil {
			return "", err
		}
		if dnc {
			return "do_not_contact", nil
		}
	}
	if kind == "post" && posts >= l.PostsPerDay {
		return "posts_per_day", nil
	}
	if kind == "reply" && replies >= l.RepliesPerDay {
		return "replies_per_day", nil
	}
	if kind == "reply" && intent.browser && browserReplies >= l.BrowserRepliesPerDay {
		return "browser_replies_per_day", nil
	}
	if kind == "reply" && contact >= l.RepliesPerUser7d && !intent.inbox {
		return "replies_per_user_7d", nil
	}
	if kind == "reply" && threadUsed {
		return "thread_per_day", nil
	}
	if gap := time.Duration(l.MinMinutesBetweenPosts) * time.Minute; kind == "post" && !latestPost.IsZero() && now.Sub(latestPost) < gap {
		intent.detail = latestPost.Add(gap).UTC().Format(time.RFC3339)
		return "min_minutes_between_posts", nil
	}
	if !latest.IsZero() && now.Sub(latest) < time.Duration(l.MinSecondsBetweenActions)*time.Second {
		intent.detail = latest.Add(time.Duration(l.MinSecondsBetweenActions) * time.Second).UTC().Format(time.RFC3339)
		return "min_seconds_between_actions", nil
	}
	return "", nil
}

// ObserveStopRequest is harness ingestion, never a model tool. Inbound text is
// transient data; only a normalized identifier and fixed source code persist.
func (g *Guard) ObserveStopRequest(ctx context.Context, username, text string, addressedToAgent bool) (bool, error) {
	if !addressedToAgent {
		return false, nil
	}
	for _, phrase := range g.stopPhrases {
		if strings.Contains(config.FoldCase(text), config.FoldCase(phrase)) {
			if err := g.opts.Store.MarkDoNotContact(ctx, g.opts.Template.ID, username, "stop_request"); err != nil {
				g.event(ctx, "guard.checked", "error", "stop_request_persist_failed", "")
				return false, &auditError{err}
			}
			g.event(ctx, "guard.checked", "blocked", "stop_request", "")
			return true, nil
		}
	}
	return false, nil
}
