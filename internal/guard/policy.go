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
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/trends"
)

type actionIntent struct {
	Text        string   `json:"text"`
	Target      string   `json:"target"`
	Parts       []string `json:"parts"`
	Hook        string   `json:"hook"`
	Trend       string   `json:"trend"`
	Rationale   string   `json:"rationale"`
	AnswersNote int64    `json:"answers_note"`
	Angle       string   `json:"angle"`
	Fact        string   `json:"fact"`
	LessonIDs   []int64  `json:"lesson_ids"`
	// TargetURL, TargetUsername and TargetID are what a reply's target resolved
	// to: the stored post's permalink, author and API id (empty for a post only
	// a browser read showed).
	TargetURL      string `json:"-"`
	TargetUsername string `json:"-"`
	TargetID       string `json:"-"`
	thread         string
	criticJSON     string
	detail         string
	// refTrace and tries are the operator's view of how the call resolved its
	// ref and which drafts the editor judged; they never reach the model.
	refTrace *store.RefTrace
	tries    []store.TryTrace
	brief    *store.BriefTrace
	browser  bool
	// inbox is true when the target is a reply to the account noted by NoteInbox.
	inbox bool
	// root is the canonical URL of the root post of the conversation a reply
	// joins: the inbox item's root, else the replied-to post itself.
	root string
	// posting is what this wake's reads showed about the item the action rests on.
	posting trends.PostingContext
}

// texts is every text the action publishes: the post or reply, then the later parts of a series.
func (i *actionIntent) texts() []string { return append([]string{i.Text}, i.Parts...) }

var mentions = regexp.MustCompile(`@[\p{L}\p{N}_.]+`)
var links = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://|(?:https?|ftp|mailto|data|javascript|file|tel):|www\.|//[\p{L}\p{N}]|[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?(?:\.[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?)+)`)

// postRefForm is the ref of a stored post: p followed by its durable id.
var postRefForm = regexp.MustCompile(`^[pP]([0-9]+)$`)

// NotePost records a post a read returned under its durable id and returns its
// ref (p12); the same permalink keeps its id across wakes. The post becomes
// something the agent may reply to or anchor a post on, and its text is kept
// so the critic can read it and a reply may quote its numbers. It returns ""
// for a post without a usable permalink.
func (g *Guard) NotePost(ctx context.Context, p threads.Post) string {
	return g.notePost(ctx, p, false, "")
}

// NoteInbox is NotePost for a reply to the account: a conversation with the
// same person is not capped by limits.replies_per_user_7d. root is the
// canonical URL of the root post of its conversation; empty means the reply
// starts its own conversation.
func (g *Guard) NoteInbox(ctx context.Context, p threads.Post, root string) string {
	return g.notePost(ctx, p, true, root)
}

func (g *Guard) notePost(ctx context.Context, p threads.Post, inbox bool, root string) string {
	thread, err := store.CanonicalThread(p.URL)
	if err != nil {
		return ""
	}
	author, _ := threadAuthor(thread)
	if author == "" {
		author, _ = store.NormalizeUsername(p.Username)
	}
	row := store.SeenPost{URL: thread, MediaID: p.ID, Author: author, Text: p.Text, Likes: p.Likes, Replies: p.Replies, AgeMinutes: p.AgeMinutes, ReadAt: g.opts.Now().UTC()}
	id, err := g.opts.Store.NoteSeenPost(ctx, g.opts.Template.ID, row)
	if err != nil {
		return ""
	}
	stored, ok, err := g.opts.Store.SeenPostByID(ctx, g.opts.Template.ID, id)
	if err != nil || !ok {
		return ""
	}
	ref := g.register(stored)
	if inbox {
		if root == "" {
			root = thread
		}
		g.mu.Lock()
		g.inbox[thread] = root
		delete(g.trendText, thread) // an inbox reply is not an idea source
		g.mu.Unlock()
	}
	return ref
}

// register makes a stored post known to this wake under its ref: the reply
// target, trend anchor and critic text lookups all read what it sets. Its
// facts are the target score of its figures aged to now.
func (g *Guard) register(row store.SeenPost) string {
	ref := store.PostRef(row.ID)
	post := threads.Post{Likes: row.Likes, Replies: row.Replies}
	if row.AgeMinutes != nil {
		age := *row.AgeMinutes + int64(max(g.opts.Now().Sub(row.ReadAt), 0)/time.Minute)
		post.AgeMinutes = &age
	}
	facts, err := trends.PostFacts(post, g.opts.Template.Targets)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.posts[ref], g.shortRef[row.URL], g.refItem[ref] = row, ref, row.URL
	g.trendText[row.URL], g.readText[row.URL] = row.Text, row.Text
	if err == nil && facts.Line != "" {
		g.facts[row.URL] = facts
	}
	return ref
}

// post resolves a p ref this wake issued, through its reads, the inbox or the
// open reply candidates; ok is false for any other ref, however valid its id.
func (g *Guard) post(raw string) (store.SeenPost, bool) {
	m := postRefForm.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return store.SeenPost{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	row, ok := g.posts[strings.ToLower(strings.TrimSpace(raw))]
	return row, ok
}

// Issued reports whether ref is a p ref this wake issued.
func (g *Guard) Issued(ref string) bool {
	_, ok := g.post(ref)
	return ok
}

// IsOwn reports whether username is one of the operator-declared own accounts.
func (g *Guard) IsOwn(username string) bool {
	user, err := store.NormalizeUsername(username)
	return err == nil && g.own[user]
}

// NoteTrend records the URL or title of a trend or web source returned this
// wake and returns the ref ("t1", "t2", ...) of each, in noting order; noting
// the same item again returns the same ref. Trend refs restart every wake; the
// set is memory-only and a post may anchor only to an item in it.
func (g *Guard) NoteTrend(refs ...string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	short := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref = strings.TrimSpace(ref); ref != "" {
			r, ok := g.shortRef[ref]
			if !ok {
				g.trendN++
				r = "t" + strconv.Itoa(g.trendN)
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
// or its topic heat. Seen shows them and a reply stores its target's.
func (g *Guard) NoteFacts(ref string, f trends.ItemFacts) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.facts[factKey(ref)] = f
}

// overlayFacts fills the context of an action from what this wake's reads
// showed: the figures of the replied-to post.
func (g *Guard) overlayFacts(pc *trends.PostingContext, intent *actionIntent, tool string) {
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

// Trend returns the item a ref names: a trend noted this wake (t3) or a post
// read in the last 7 days (p12, once resolved or read this wake).
func (g *Guard) Trend(ref string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	item, ok := g.refItem[strings.ToLower(strings.TrimSpace(ref))]
	return item, ok
}

// traceRef records how a given ref resolved, for the operator's view of a call.
func (g *Guard) traceRef(field, given, resolved string) *store.RefTrace {
	g.mu.Lock()
	defer g.mu.Unlock()
	return &store.RefTrace{Field: field, Given: capRunes(given), Resolved: capRunes(resolved), Noted: len(g.shortRef)}
}

// capRunes keeps a stored ref or text field within store.MaxArgField bytes.
func capRunes(s string) string {
	s, _ = stdx.TruncateUTF8(s, store.MaxArgField)
	return s
}

// threadAuthor extracts the author from a canonical https://threads.<tld>/@author/post/<id> URL.
func threadAuthor(thread string) (author, reason string) {
	u, err := url.Parse(thread)
	if _, ok := stdx.ThreadsHost(u.Hostname()); err != nil || !ok {
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

// lengthBreak names the length limit text breaks, as "N of MAX unit", or is
// empty. The platform caps every text; a reply is also held to the template's
// guard.reply_max_runes and a post to the post limits the quality scorer shares.
func lengthBreak(tool, text string, replyMax int) string {
	n := utf8.RuneCountInString(text)
	limit, maxLines := quality.Limit(tool, replyMax)
	if lines := quality.Lines(text); maxLines > 0 && lines > maxLines {
		return strconv.Itoa(lines) + " of " + strconv.Itoa(maxLines) + " lines"
	}
	if n > limit {
		return strconv.Itoa(n) + " of " + strconv.Itoa(limit) + " characters"
	}
	return ""
}

// content checks a post or reply. A draft the writer will rewrite skips the
// length checks; the rewritten text is checked in full (recheck).
func (g *Guard) content(ctx context.Context, a Attempt, draft bool) (*actionIntent, string, error) {
	if spec, ok := Spec(a.Tool); !ok || spec.Effect != EffectThreadsWrite {
		return nil, "", nil
	}
	var intent actionIntent
	if !utf8.ValidString(a.Arguments) || json.Unmarshal([]byte(a.Arguments), &intent) != nil || strings.TrimSpace(intent.Text) == "" {
		return nil, "invalid_tool_arguments", nil
	}
	if !store.DistinctLessonIDs(intent.LessonIDs) {
		return nil, "invalid_lesson_ids", nil
	}
	if len(intent.Parts) >= store.MaxSeriesParts {
		return nil, "too_many_parts", nil
	}
	if a.Tool == "post" && g.opts.Template.Guard.TrendAnchor {
		first, _, _ := strings.Cut(strings.TrimSpace(intent.Text), "\n")
		if n := utf8.RuneCountInString(first); n > quality.MaxHookRunes && !draft {
			intent.detail = strconv.Itoa(n) + " of " + strconv.Itoa(quality.MaxHookRunes) + " characters in the first line"
			return &intent, "text_length", nil
		}
		if intent.Hook == "" || strings.TrimSpace(intent.Rationale) == "" || strings.ContainsAny(intent.Rationale, "\r\n") || utf8.RuneCountInString(intent.Rationale) > MaxRationaleRunes {
			return nil, "post_brief", nil
		}
		ref, ok := g.Trend(intent.Trend)
		intent.refTrace = g.traceRef("trend", intent.Trend, ref)
		if !ok {
			return &intent, "no_trend_anchor", nil
		}
		intent.Trend = ref
	}
	for _, text := range intent.texts() {
		if strings.TrimSpace(text) == "" {
			return nil, "invalid_tool_arguments", nil
		}
		if detail := lengthBreak(a.Tool, text, g.opts.Template.Guard.ReplyMaxRunes); detail != "" && !draft {
			intent.detail = detail
			return &intent, "text_length", nil
		}
		if reason, detail := contentBlock(g.opts.Template.Guard.BlockedPhrases, text); reason != "" {
			intent.detail = detail
			return &intent, reason, nil
		}
		if g.ownInteraction(text) {
			return nil, "own_account", nil
		}
	}
	if a.Tool == "reply" {
		row, ok := g.post(intent.Target)
		intent.refTrace = g.traceRef("target", intent.Target, row.URL)
		if !ok {
			return &intent, "unknown_target", nil
		}
		intent.TargetURL, intent.TargetUsername, intent.TargetID = row.URL, row.Author, row.MediaID
		if a.Target != nil {
			*a.Target = ResolvedTarget{URL: row.URL, MediaID: row.MediaID}
		}
		username := row.Author
		if g.own[username] || g.ownURL(intent.TargetURL) {
			return nil, "own_account", nil
		}
		if username == "" {
			return nil, "reply_target_host", nil
		}
		thread, err := store.CanonicalThread(intent.TargetURL)
		if err != nil {
			return nil, "reply_target_host", nil
		}
		intent.thread = thread
		if _, reason := threadAuthor(thread); reason != "" {
			return nil, "reply_target_host", nil
		}
		// A reply without a post id answers a post only a browser read showed.
		intent.browser = intent.TargetID == ""
		g.mu.Lock()
		var inboxRoot string
		inboxRoot, intent.inbox = g.inbox[thread]
		intent.root = cmp.Or(inboxRoot, thread)
		outside := g.facts[thread].Outside
		g.mu.Unlock()
		if outside != "" && !intent.inbox {
			intent.detail = outside
			return &intent, reasonTargetOutsideWindow, nil
		}
		// An author mention may contain dots; check URL syntax separately
		// from mention identity so mailto/HTTP links retain the URL rule ID.
		if links.MatchString(mentions.ReplaceAllString(intent.Text, "")) {
			return nil, "reply_url", nil
		}
		for _, mention := range mentions.FindAllString(intent.Text, -1) {
			normalized, err := store.NormalizeUsername(mention)
			if err != nil || normalized != username {
				return nil, "reply_mention", nil
			}
		}
	}
	l := g.opts.Template.Limits
	if l.PostsPerDay <= 0 || l.RepliesPerDay <= 0 || l.RepliesPerUser7d <= 0 || l.MinSecondsBetweenActions <= 0 {
		return nil, "invalid_policy", nil
	}
	return &intent, "", nil
}

// reserve runs after the attempted-call audit is committed. One writer
// transaction serializes check+intent+contact writes across guards, stores and
// processes.
// reserve runs every limit check and, unless checkOnly, records the intent. A
// checkOnly pass runs before the writer and critic so a write that a limit
// would refuse costs no model calls; it writes nothing.
func (g *Guard) reserve(ctx context.Context, a Attempt, intent *actionIntent, auditID int64, now time.Time, checkOnly bool) (id int64, reason string, err error) {
	agent := g.opts.Template.ID
	// The breaker reads and queues outside the reservation transaction; its
	// refusal is audited inside it like any other.
	paused := ""
	if a.Tool == "reply" {
		if paused, err = g.replyBreaker(ctx, intent, now); err != nil {
			return 0, "", err
		}
	}
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
		if reason == "" {
			reason = paused
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
			var fit bool
			if fit, err = tx.LessonsFit(ctx, agent, a.Tool, intent.LessonIDs); err != nil {
				return err
			}
			if !fit {
				reason = "invalid_lesson_ids"
			}
		}
		if checkOnly {
			return nil
		}
		if reason == "" {
			if id, err = tx.InsertIntent(ctx, store.NewIntent{AgentID: agent, Kind: a.Tool, Text: intent.Text, TargetURL: intent.TargetURL, TargetUsername: intent.TargetUsername, Hook: intent.Hook, Rationale: intent.Rationale, Trend: strings.TrimSpace(intent.Trend), AnswersNote: intent.AnswersNote, Angle: intent.Angle, Fact: intent.Fact, LessonIDs: intent.LessonIDs, CriticJSON: intent.criticJSON, RootURL: intent.root, Parts: intent.Parts, TargetScoreJSON: intent.posting.TargetScoreJSON, ParentReplies: intent.posting.ParentReplies, ParentAgeMinutes: intent.posting.ParentAgeMinutes, DryRun: g.opts.Template.DryRun, Browser: intent.browser, At: now}); err != nil {
				return err
			}
			if a.Tool == "reply" {
				if err = tx.AddReply(ctx, agent, intent.TargetUsername); err != nil {
					return err
				}
			}
			// After a pacing wait the first pass left the row marked refused.
			if err = tx.ClearBlocked(ctx, auditID); err != nil {
				return err
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
		return "note_not_open", nil
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
	ref := strings.TrimSpace(intent.Trend)
	hook := hookWords(intent.Text)
	for _, p := range recent {
		if (ref != "" && p.Trend == ref) || similarHooks(hook, hookWords(p.Text)) {
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
// Limit: word overlap, not meaning; compare embeddings if paraphrases slip through.
func similarHooks(a, b map[string]bool) bool {
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return shared >= 2 && shared*2 >= min(len(a), len(b))
}

// ConversationCap is the template's per-conversation reply cap as the store
// rule reads it; the guard and the inbox share it.
func ConversationCap(t config.Template) store.ConversationCap {
	l := t.Limits
	return store.ConversationCap{Max: l.RepliesPerConversation, HotLikes: l.ConversationHotLikes, HotReplies: l.ConversationHotReplies, DryRun: t.DryRun}
}

// NextPostAt is the earliest time min_minutes_between_posts allows a post after
// the newest own post; zero when the limit is off or there is no post.
func NextPostAt(l config.LimitsConfig, latestPost time.Time) time.Time {
	if l.MinMinutesBetweenPosts <= 0 || latestPost.IsZero() {
		return time.Time{}
	}
	return latestPost.Add(time.Duration(l.MinMinutesBetweenPosts) * time.Minute)
}

func (g *Guard) quotas(ctx context.Context, tx store.ReservationTx, actions []store.ReservedAction, kind string, intent *actionIntent, now time.Time) (string, error) {
	l := g.opts.Template.Limits
	dayStart, dayEnd, _ := l.DayBounds(now)
	from := now.Add(-7 * 24 * time.Hour)
	posts, replies, browserReplies, contact := 0, 0, 0, 0
	threadUsed := false
	var latest, latestPost, oldestHour time.Time
	hourReplies := 0
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
		if !at.Before(dayStart) && at.Before(dayEnd) {
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
		if at.After(now.Add(-time.Hour)) {
			if hourReplies == 0 || at.Before(oldestHour) {
				oldestHour = at
			}
			hourReplies++
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
		if !at.Before(dayStart) && at.Before(dayEnd) && canonical == intent.thread {
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
	if kind == "reply" && l.RepliesPerHour > 0 && hourReplies >= l.RepliesPerHour {
		intent.detail = oldestHour.Add(time.Hour).UTC().Format(time.RFC3339)
		return "replies_per_hour", nil
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
	if kind == "reply" {
		n, capped, err := tx.ConversationReplies(ctx, g.opts.Template.ID, intent.root, ConversationCap(g.opts.Template))
		if err != nil {
			return "", err
		}
		if capped {
			intent.detail = strconv.Itoa(n)
			return "replies_per_conversation", nil
		}
	}
	if next := NextPostAt(l, latestPost); kind == "post" && now.Before(next) {
		intent.detail = next.UTC().Format(time.RFC3339)
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

// BreakerPrefix starts the event key of the owner notice a tripped reply
// breaker queues; the newest such notice's time is when the pause began.
const BreakerPrefix = "notice:reply_breaker:"

// ReplyPause is when the last reply breaker trip began and when its pause
// lifts; until is zero when replies are not paused.
func ReplyPause(ctx context.Context, st *store.Store, agent string, l config.LimitsConfig, now time.Time) (began, until time.Time, err error) {
	if l.BreakerMinLikes <= 0 {
		return time.Time{}, time.Time{}, nil
	}
	if began, err = st.LastOwnerEvent(ctx, agent, BreakerPrefix); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if end := began.Add(time.Duration(l.BreakerHours) * time.Hour); !began.IsZero() && now.Before(end) {
		until = end
	}
	return began, until, nil
}

// replyBreaker refuses a reply while replies are paused. Replies pause when
// the newest limits.breaker_actions own actions with a metric sample earned
// fewer than limits.breaker_min_likes likes plus replies in total, judging
// only actions posted after the last pause began. The trip time is the queued
// owner notice, so it survives a restart. It trips only on a full window.
func (g *Guard) replyBreaker(ctx context.Context, intent *actionIntent, now time.Time) (string, error) {
	l := g.opts.Template.Limits
	if l.BreakerMinLikes <= 0 {
		return "", nil
	}
	agent := g.opts.Template.ID
	pause := time.Duration(l.BreakerHours) * time.Hour
	began, lifts, err := ReplyPause(ctx, g.opts.Store, agent, l, now)
	if err != nil {
		return "", err
	}
	if !lifts.IsZero() {
		intent.detail = lifts.UTC().Format(time.RFC3339)
		return "reply_breaker", nil
	}
	count, likes, newest, err := g.opts.Store.MeasuredEngagement(ctx, store.ActionScope{AgentID: agent, DryRun: g.opts.Template.DryRun}, began, l.BreakerActions)
	if err != nil || count < l.BreakerActions || likes >= l.BreakerMinLikes {
		return "", err
	}
	until := now.Add(pause).UTC().Format(time.RFC3339)
	text := "Replies paused until " + until + ": the newest " + strconv.Itoa(count) + " measured actions earned " + strconv.Itoa(likes) + " likes and replies in total. Posts continue."
	if _, err = g.opts.Store.QueueOwner(ctx, agent, "notice", text, BreakerPrefix+strconv.FormatInt(newest, 10), now); err != nil {
		return "", err
	}
	intent.detail = until
	return "reply_breaker", nil
}
