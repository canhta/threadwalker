package tools

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

// Inbox bounds: replies to the account's own actions of the last InboxWindow
// are read for at most InboxParents actions, plus the mentions, and at most
// InboxChecks replies are looked into for an answer of ours.
const (
	InboxWindow  = 72 * time.Hour
	InboxParents = 15
	InboxItems   = 10
	InboxChecks  = 12
)

// InboxReply is a reply or mention that waits for an answer and what of ours
// it answers (empty for a mention). Text is untrusted.
type InboxReply struct {
	Post   threads.Post
	At     time.Time
	Parent string
	// Root is the canonical URL of the root post of the conversation: the root
	// of the reply of ours it answers, else the reply itself.
	Root string
}

// InboxRead is everything the inbox read needs, so the wake and the scheduler's
// poll between wakes run the same code. Observe, when set, sees every replies
// page (the wake uses it for stop requests).
type InboxRead struct {
	Store    *store.Store
	Threads  threads.Backend
	AgentID  string
	Now      time.Time
	IsOwn    func(username string) bool
	Observe  func(context.Context, threads.Page) error
	MaxItems int
	// Cap is the template's per-conversation reply cap; replies of a
	// conversation at the cap are not listed.
	Cap store.ConversationCap
}

// ReadInbox lists the replies and mentions that wait for an answer, newest
// first. It skips own replies, replies older than InboxWindow, authors on the
// do-not-contact list, and replies the agent already answered (a stored reply
// action targets them, or the account itself replied under them) and replies of
// conversations already at limits.replies_per_conversation. The int counts the
// replies skipped because their timestamp was missing or in an unknown format.
// It fails only when every read failed.
func ReadInbox(ctx context.Context, in InboxRead) ([]InboxReply, int, error) {
	since := in.Now.Add(-InboxWindow)
	parents, err := in.Store.PublishedSince(ctx, in.AgentID, since, InboxParents)
	if err != nil {
		return nil, 0, err
	}
	targets, err := in.Store.ReplyTargets(ctx, in.AgentID)
	if err != nil {
		return nil, 0, err
	}
	answered := map[string]bool{}
	for _, target := range targets {
		if c, err := store.CanonicalThread(target); err == nil {
			answered[c] = true
		}
	}
	seen := map[string]bool{}
	var cands []InboxReply
	failed, reads, skipped := 0, 0, 0
	capped := map[string]bool{}
	atCap := func(root string) (bool, error) {
		full, ok := capped[root]
		if !ok {
			_, full, err = in.Store.ConversationReplies(ctx, in.AgentID, root, in.Cap)
			capped[root] = full
		}
		return full, err
	}
	collect := func(id, parent, parentRoot string) {
		reads++
		page, readErr := in.Threads.ReadReplies(ctx, id)
		if readErr != nil {
			failed++
			return
		}
		if in.Observe != nil && in.Observe(ctx, page) != nil {
			return
		}
		skipped += page.Skipped
		for _, p := range page.Posts {
			at := p.Timestamp
			if at.IsZero() {
				skipped++
				continue
			}
			if p.ID == "" || seen[p.ID] || at.Before(since) || in.IsOwn(p.Username) {
				continue
			}
			canonical, err := store.CanonicalThread(p.URL)
			if err != nil || answered[canonical] {
				continue
			}
			if blocked, err := in.Store.IsDoNotContact(ctx, in.AgentID, p.Username); err != nil || blocked {
				continue
			}
			root := cmp.Or(parentRoot, canonical)
			if full, err := atCap(root); err != nil || full {
				continue
			}
			seen[p.ID] = true
			cands = append(cands, InboxReply{p, at, parent, root})
		}
	}
	for _, parent := range parents {
		collect(parent.MediaID, parent.Text, parent.Root)
	}
	collect("", "", "")
	if failed == reads {
		return nil, skipped, errors.New("reading inbox: every read failed")
	}
	slices.SortStableFunc(cands, func(a, b InboxReply) int { return cmp.Compare(b.At.UnixNano(), a.At.UnixNano()) })

	var out []InboxReply
	checks := 0
	for _, c := range cands {
		if len(out) == cmp.Or(in.MaxItems, InboxItems) {
			break
		}
		if checks < InboxChecks {
			checks++
			page, err := in.Threads.ReadReplies(ctx, c.Post.ID)
			if err == nil && slices.ContainsFunc(page.Posts, func(p threads.Post) bool { return in.IsOwn(p.Username) }) {
				continue
			}
		}
		out = append(out, c)
	}
	return out, skipped, nil
}

// InboxItem is one inbox reply as the wake's data message shows it. Text and
// ReplyingTo are redacted; Text is untrusted data. Ref and TargetID are the
// target_url and target_id of the reply tool.
type InboxItem struct {
	Ref        string `json:"ref"`
	TargetID   string `json:"target_id"`
	Author     string `json:"author"`
	Text       string `json:"text"`
	ReplyingTo string `json:"replying_to"`
	AgeMinutes int64  `json:"age_minutes"`
}

// Inbox is the harness read, before the model's first request, of the replies
// that wait for an answer. It notes each in the guard as read, so a reply by
// ref passes the reply checks. A failed read gives an empty inbox.
func (d *Dispatcher) Inbox(ctx context.Context) ([]InboxReply, []InboxItem) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.opts.Now()
	replies, _, err := ReadInbox(ctx, InboxRead{Store: d.opts.Store, Threads: d.opts.Threads, AgentID: d.opts.Template.ID, Now: now, IsOwn: d.guard.IsOwn, Cap: guard.ConversationCap(d.opts.Template),
		Observe: func(ctx context.Context, page threads.Page) error { return d.observeStops(ctx, "read_replies", page) }})
	if err != nil {
		return nil, nil
	}
	items := make([]InboxItem, 0, len(replies))
	for _, c := range replies {
		first, _, _ := strings.Cut(strings.TrimSpace(c.Parent), "\n")
		first, _ = stdx.TruncateUTF8(first, 200)
		if first == "" {
			first = "(a mention of the account)"
		}
		items = append(items, InboxItem{
			Ref:        d.guard.NoteInbox(c.Post.ID, c.Post.URL, c.Post.Text, c.Root),
			TargetID:   c.Post.ID,
			Author:     c.Post.Username,
			Text:       d.redactor.String(c.Post.Text),
			ReplyingTo: d.redactor.String(first),
			AgeMinutes: int64(now.Sub(c.At).Minutes()),
		})
	}
	return replies, items
}
