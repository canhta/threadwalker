package threads

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// snapshotMaxMetric is the largest integer a JSON consumer can represent
// without losing precision. Larger values are rejected as malformed.
const snapshotMaxMetric = 1 << 53

// ErrLoggedOut means the browser showed the logged-out state. Callers must pause
// the agent and notify the owner rather than retrying or guessing.
var ErrLoggedOut = errors.New("logged out")

// ErrCaptcha means the browser showed a captcha. The parser never attempts a
// challenge.
var ErrCaptcha = errors.New("captcha shown")

// ErrUnknownPage means the snapshot does not describe a recognised page kind, or
// describes a page other than the one the caller expected.
var ErrUnknownPage = errors.New("unknown page")

// ErrMalformedPage means the snapshot violates the measured page mapping.
var ErrMalformedPage = errors.New("malformed page snapshot")

// Post is one post read from Threads. Text is untrusted third-party data and
// must be wrapped before being shown to the model.
type Post struct {
	// Ref is the guard's short ref for URL; the model passes it instead of copying the URL.
	Ref      string `json:"ref,omitempty"`
	ID       string `json:"id"`
	Username string `json:"username"`
	Text     string `json:"text"`
	URL      string `json:"url"`
	// Topic is the Threads topic (community) the post is filed under, from a browser read.
	Topic string `json:"topic,omitempty"`
	// Timestamp is the API's publish time in UTC; zero for a feed post.
	Timestamp        time.Time `json:"timestamp,omitzero"`
	IsReply          bool      `json:"is_reply,omitempty"`
	AddressedToAgent bool      `json:"addressed_to_agent,omitempty"`
	// Likes, Replies, Reposts and AgeMinutes come from a browser read. Nil means
	// the page did not show the figure; Threads shows no number at zero, so a
	// counter button without a number is 0.
	Likes      *int64 `json:"likes,omitempty"`
	Replies    *int64 `json:"replies,omitempty"`
	Reposts    *int64 `json:"reposts,omitempty"`
	AgeMinutes *int64 `json:"age_minutes,omitempty"`
}

// Trend is one item of the Trending-now panel: a topic with its AI summary and
// post count. URL opens the topic page (read_topic). Every field is untrusted.
// PostCount is Posts parsed to a number; nil when Posts is absent or unreadable.
type Trend struct {
	Ref       string `json:"ref,omitempty"`
	Title     string `json:"title"`
	Summary   string `json:"summary,omitempty"`
	Posts     string `json:"posts,omitempty"`
	PostCount *int64 `json:"post_count,omitempty"`
	URL       string `json:"url"`
}

// Metrics holds Threads Insights counters. Quotes, Shares and Followers are nil
// when the API did not return them: absent is never zero. Followers is set only
// for the account read.
type Metrics struct {
	Views     int64  `json:"views"`
	Likes     int64  `json:"likes"`
	Replies   int64  `json:"replies"`
	Reposts   int64  `json:"reposts"`
	Quotes    *int64 `json:"quotes,omitempty"`
	Shares    *int64 `json:"shares,omitempty"`
	Followers *int64 `json:"followers_count,omitempty"`
}

// Profile is a public profile read by username. Followers is nil when the API
// did not return it: absent is never zero.
type Profile struct {
	Username  string `json:"username"`
	Followers *int64 `json:"followers_count,omitempty"`
	Views7d   *int64 `json:"views_7d,omitempty"`
	Verified  bool   `json:"is_verified,omitempty"`
}

// Published is a confirmed publication: its permalink and its API media id.
type Published struct {
	URL string `json:"url"`
	ID  string `json:"id"`
}

// Form describes a validated reply form on a post page.
type Form struct {
	Action   string `json:"action"`
	Method   string `json:"method"`
	Composer bool   `json:"composer"`
	Submit   bool   `json:"submit"`
}

// Page is a typed, validated page of posts.
type Page struct {
	Kind  string `json:"kind"`
	Posts []Post `json:"posts,omitempty"`
	// Trends is the Trending-now panel of a feed read.
	Trends []Trend `json:"trends,omitempty"`
	// ThreadURL is the permalink of a submitted reply; Form is a post page's reply form.
	ThreadURL string `json:"thread_url,omitempty"`
	Form      *Form  `json:"form,omitempty"`
	// LimitedAccess explains an empty result the Meta app's access level may
	// cause; it is set only on such a result.
	LimitedAccess string `json:"limited_access,omitempty"`
	// Skipped counts API posts left out because their timestamp format is unknown.
	Skipped int `json:"-"`
}

type snapshotOperationError struct {
	operation string
	err       error
}

func (e *snapshotOperationError) Error() string {
	return "threads: " + e.operation + ": " + e.err.Error()
}

func (e *snapshotOperationError) Unwrap() error { return e.err }

func snapshotError(operation string, err error) error {
	return &snapshotOperationError{operation: operation, err: err}
}

// operationForExpectedKind maps a caller's page-kind expectation to a stable,
// safe operation code. Arbitrary caller strings never enter an error message.
func operationForExpectedKind(kind string) (string, bool) {
	switch kind {
	case "":
		return "parse", true
	case "feed":
		return "read_feed", true
	case "topic":
		return "read_topic", true
	case "post":
		return "read_post", true
	case "submitted":
		return "submit", true
	case "logged_out", "captcha":
		return "auth", true
	default:
		return "parse", false
	}
}

func parseSnapshotBase(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, ErrMalformedPage
	}
	if strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || r == '\\' }) >= 0 {
		return nil, ErrMalformedPage
	}
	base, err := url.Parse(raw)
	if err != nil || !base.IsAbs() || base.Opaque != "" || base.User != nil || base.Host == "" {
		return nil, ErrMalformedPage
	}
	if !strings.EqualFold(base.Scheme, "http") && !strings.EqualFold(base.Scheme, "https") {
		return nil, ErrMalformedPage
	}
	return base, nil
}
