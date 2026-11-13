package threads

import (
	"context"
	"path"
	"sync"
)

// Operation names key Memory.Pages, Memory.Results and Memory.Errors and
// label Memory.Calls.
const (
	OpReadFeed       = "read_feed"
	OpReadTopic      = "read_topic"
	OpSearch         = "search"
	OpReadPost       = "read_post"
	OpProfile        = "profile"
	OpProfilePosts   = "profile_posts"
	OpReadOwnPosts   = "read_own_posts"
	OpReadReplies    = "read_replies"
	OpQuota          = "publishing_limit"
	OpReadOwnMetrics = "read_own_metrics"
	OpPost           = "post"
	OpReply          = "reply"
	OpClose          = "close"
)

// Call is one operation a Memory served: Arg is the query, URL or reply target
// and Text the submitted text.
type Call struct{ Op, Arg, Text string }

// Memory is the in-memory Backend. It is the `stub` backend and the one fake
// every test and eval configures. Set the exported fields before use; calls
// are recorded and safe for concurrent use.
type Memory struct {
	// Pages answers read_feed, search, read_post and read_replies.
	Pages map[string]Page
	// Replies answers read_replies by post id (the empty id is the mentions);
	// an id it does not hold falls back to Pages.
	Replies map[string]Page
	// Own answers read_own_metrics; Actions answers per-post metrics by media id
	// and reports ErrMetricsUnavailable for an id it does not hold.
	Own Metrics
	// Quota answers publishing_limit.
	Quota   Quota
	Actions map[string]Metrics
	// Profiles answers profile_lookup by username; a username it does not hold
	// is a plain profile with no follower count.
	Profiles map[string]Profile
	// Results is the confirmed thread URL a post or reply returns; its media id
	// is the last path element of that URL.
	Results map[string]string
	// Errors fails an operation, including OpClose.
	Errors map[string]error
	// Hook runs before every operation; a non-nil error fails it.
	Hook func(context.Context, Call) error

	mu    sync.Mutex
	calls []Call
}

var (
	_ Backend = (*Memory)(nil)
)

// NewMemory returns a Memory holding the synthetic pages the stub backend
// serves. It accepts no write: a post or reply has no result.
func NewMemory() *Memory {
	const base = "https://www.threads.com/@synthetic_author/post/"
	post := func(slug, text string) Post {
		return Post{ID: "id-" + slug, URL: base + slug, Username: "synthetic_author", Text: text}
	}
	withoutID := func(p Post) Post { p.ID = ""; return p } // feed posts carry no API id
	return &Memory{
		Pages: map[string]Page{
			OpReadFeed:     {Kind: "feed", Posts: []Post{withoutID(post("thread", "Synthetic fixture: a small Go tool reduced a repetitive task. Bài mẫu đây."))}},
			OpSearch:       {Kind: "search", Posts: []Post{post("search", "Synthetic fixture: measuring the useful part of an experiment. Bài mẫu đây.")}},
			OpProfilePosts: {Kind: "profile_posts", Posts: []Post{post("profile", "Synthetic fixture: a recent post of a public profile. Bài mẫu đây.")}},
			OpReadReplies:  {Kind: "replies", Posts: []Post{post("reply", "Synthetic fixture: a reply to an own post. Bài mẫu đây.")}},
			OpReadPost:     {Kind: "post", Posts: []Post{post("thread", "Synthetic fixture: a tiny tool can make a concrete difference. Bài mẫu đây.")}},
		},
		Actions: map[string]Metrics{},
		Results: map[string]string{},
		Errors:  map[string]error{},
	}
}

// Calls returns the operations served so far, oldest first.
func (m *Memory) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Call(nil), m.calls...)
}

// Count returns how many times op was served.
func (m *Memory) Count(op string) int {
	n := 0
	for _, c := range m.Calls() {
		if c.Op == op {
			n++
		}
	}
	return n
}

// Last returns the newest call of op, or the zero Call.
func (m *Memory) Last(op string) Call {
	calls := m.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Op == op {
			return calls[i]
		}
	}
	return Call{}
}

func (m *Memory) serve(ctx context.Context, c Call) error {
	m.mu.Lock()
	m.calls = append(m.calls, c)
	m.mu.Unlock()
	if m.Hook != nil {
		if err := m.Hook(ctx, c); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.Errors[c.Op]
}

func (m *Memory) page(ctx context.Context, c Call) (Page, error) {
	if err := m.serve(ctx, c); err != nil {
		return Page{}, err
	}
	if p, ok := m.Replies[c.Arg]; ok && c.Op == OpReadReplies {
		return p, nil
	}
	return m.Pages[c.Op], nil
}

func (m *Memory) ReadFeed(ctx context.Context) (Page, error) {
	return m.page(ctx, Call{Op: OpReadFeed})
}
func (m *Memory) ReadTopic(ctx context.Context, topicURL, query string) (Page, error) {
	return m.page(ctx, Call{Op: OpReadTopic, Arg: topicURL + query})
}
func (m *Memory) Search(ctx context.Context, query string, _ bool) (Page, error) {
	return m.page(ctx, Call{Op: OpSearch, Arg: query})
}
func (m *Memory) ProfileLookup(ctx context.Context, username string) (Profile, error) {
	if err := m.serve(ctx, Call{Op: OpProfile, Arg: username}); err != nil {
		return Profile{}, err
	}
	if p, ok := m.Profiles[username]; ok {
		return p, nil
	}
	return Profile{Username: username}, nil
}
func (m *Memory) ReadOwnPosts(ctx context.Context) (Page, error) {
	return m.page(ctx, Call{Op: OpReadOwnPosts})
}
func (m *Memory) ProfilePosts(ctx context.Context, username string) (Page, error) {
	return m.page(ctx, Call{Op: OpProfilePosts, Arg: username})
}
func (m *Memory) ReadPost(ctx context.Context, url string) (Page, error) {
	return m.page(ctx, Call{Op: OpReadPost, Arg: url})
}
func (m *Memory) ReadReplies(ctx context.Context, id string) (Page, error) {
	return m.page(ctx, Call{Op: OpReadReplies, Arg: id})
}
func (m *Memory) PublishingLimit(ctx context.Context) (Quota, error) {
	if err := m.serve(ctx, Call{Op: OpQuota}); err != nil {
		return Quota{}, err
	}
	return m.Quota, nil
}
func (m *Memory) ReadOwnMetrics(ctx context.Context) (Metrics, error) {
	if err := m.serve(ctx, Call{Op: OpReadOwnMetrics}); err != nil {
		return Metrics{}, err
	}
	return m.Own, nil
}
func (m *Memory) ReadActionMetrics(ctx context.Context, id string) (Metrics, error) {
	if err := m.serve(ctx, Call{Op: OpReadOwnMetrics, Arg: id}); err != nil {
		return Metrics{}, err
	}
	v, ok := m.Actions[id]
	if !ok {
		return Metrics{}, ErrMetricsUnavailable
	}
	return v, nil
}
func (m *Memory) Post(ctx context.Context, text string) (Published, error) {
	if err := m.serve(ctx, Call{Op: OpPost, Text: text}); err != nil {
		return Published{}, err
	}
	return m.published(OpPost), nil
}
func (m *Memory) Reply(ctx context.Context, target, text string) (Published, error) {
	if err := m.serve(ctx, Call{Op: OpReply, Arg: target, Text: text}); err != nil {
		return Published{}, err
	}
	return m.published(OpReply), nil
}
func (m *Memory) published(op string) Published {
	url := m.Results[op]
	if url == "" {
		return Published{}
	}
	return Published{URL: url, ID: SyntheticID(url)}
}

// SyntheticID is the media id Memory gives a confirmed URL and the id a seeded
// fixture action stores: the last path element.
func SyntheticID(url string) string { return path.Base(url) }
func (m *Memory) Close(ctx context.Context) error {
	return m.serve(ctx, Call{Op: OpClose})
}
