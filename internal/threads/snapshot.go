package threads

// This file is the parser boundary for the browser driver. The driver turns the
// documented synthetic-v1 DOM into a small JSON snapshot; this package validates
// that snapshot and returns typed data. Post text is data only: it is never
// interpreted, executed or persisted here.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	snapshotContract = "synthetic-v1"
	snapshotMaxBytes = 1 << 20 // 1 MiB; a snapshot is a control envelope, not a page dump.
	// snapshotMaxMetric is the largest integer a JSON consumer can represent
	// without losing precision. Larger values are rejected as out of contract.
	snapshotMaxMetric = 1 << 53
)

// ErrLoggedOut means the browser showed the logged-out state. Callers must pause
// the agent and notify the owner rather than retrying or guessing.
var ErrLoggedOut = errors.New("logged out")

// ErrCaptcha means the browser showed a captcha. The parser never attempts a
// challenge.
var ErrCaptcha = errors.New("captcha shown")

// ErrUnknownPage means the snapshot does not describe a recognised page kind, or
// describes a page other than the one the caller expected.
var ErrUnknownPage = errors.New("unknown page")

// ErrMalformedPage means the snapshot violates the synthetic-v1 contract.
var ErrMalformedPage = errors.New("malformed page snapshot")

// Post is one post read from Threads. Text is untrusted third-party data and
// must be wrapped before being shown to the model.
type Post struct {
	ID               string `json:"id"`
	Username         string `json:"username"`
	Text             string `json:"text"`
	URL              string `json:"url"`
	IsReply          bool   `json:"is_reply,omitempty"`
	AddressedToAgent bool   `json:"addressed_to_agent,omitempty"`
}

// Metrics is the account's own counters for one wake.
type Metrics struct {
	Views   int64 `json:"views"`
	Likes   int64 `json:"likes"`
	Replies int64 `json:"replies"`
	Reposts int64 `json:"reposts"`
}

// Form describes a validated write form on a compose or post page.
type Form struct {
	Action   string `json:"action"`
	Method   string `json:"method"`
	Composer bool   `json:"composer"`
	Submit   bool   `json:"submit"`
}

// Page is a typed, validated browser snapshot.
type Page struct {
	Kind      string   `json:"kind"`
	Posts     []Post   `json:"posts,omitempty"`
	Metrics   *Metrics `json:"metrics,omitempty"`
	ThreadURL string   `json:"thread_url,omitempty"`
	Form      *Form    `json:"form,omitempty"`
}

type snapshotPost struct {
	ID               *string `json:"id"`
	Username         *string `json:"username"`
	Text             *string `json:"text"`
	URL              *string `json:"url"`
	IsReply          bool    `json:"is_reply,omitempty"`
	AddressedToAgent bool    `json:"addressed_to_agent,omitempty"`
}

type snapshotDoc struct {
	Contract  *string           `json:"contract"`
	Page      *string           `json:"page"`
	Posts     *[]snapshotPost   `json:"posts,omitempty"`
	Metrics   map[string]string `json:"metrics,omitempty"`
	ThreadURL *string           `json:"thread_url,omitempty"`
	Form      *Form             `json:"form,omitempty"`
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
	case "search":
		return "search", true
	case "post":
		return "read_post", true
	case "metrics":
		return "read_own_metrics", true
	case "compose":
		return "compose", true
	case "submitted":
		return "submit", true
	case "logged_out", "captcha":
		return "auth", true
	default:
		return "parse", false
	}
}

// ParseSnapshot validates one synthetic-v1 JSON DOM snapshot and returns typed
// data. expectedKind is the page kind the caller navigated to; an empty string
// accepts any recognised kind. Auth markers are recognised even when a read kind
// was expected. Relative URLs resolve against baseURL, and every resolved URL
// must stay on that origin.
//
// All returned errors are static operation/reason codes. They never include
// raw URLs, usernames, post text or JSON parser diagnostics.
func ParseSnapshot(raw []byte, expectedKind string, baseURL string) (Page, error) {
	operation, ok := operationForExpectedKind(expectedKind)
	if !ok {
		return Page{}, snapshotError("parse", ErrUnknownPage)
	}
	if len(raw) == 0 || len(raw) > snapshotMaxBytes {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}

	base, err := parseSnapshotBase(baseURL)
	if err != nil {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}

	// encoding/json accepts duplicate object names by keeping the last value.
	// Reject them first so an ambiguous snapshot cannot silently win.
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}

	var doc snapshotDoc
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}

	if doc.Contract == nil || strings.TrimSpace(*doc.Contract) == "" {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}
	if *doc.Contract != snapshotContract {
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}
	if doc.Page == nil || strings.TrimSpace(*doc.Page) == "" {
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}
	kind := *doc.Page

	switch kind {
	case "logged_out":
		return Page{}, snapshotError(operation, ErrLoggedOut)
	case "captcha":
		return Page{}, snapshotError(operation, ErrCaptcha)
	case "feed", "search", "post", "metrics", "compose", "submitted":
		// Known page kind; checked below.
	default:
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}

	if expectedKind != "" && kind != expectedKind {
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}

	page := Page{Kind: kind}

	switch kind {
	case "feed", "search":
		if doc.Posts == nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.Posts, err = parseSnapshotPosts(*doc.Posts, base)
		if err != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if doc.Form != nil || len(doc.Metrics) > 0 || hasSnapshotThreadURL(doc.ThreadURL) {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}

	case "post":
		if doc.Posts == nil || len(*doc.Posts) != 1 {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.Posts, err = parseSnapshotPosts(*doc.Posts, base)
		if err != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if len(doc.Metrics) > 0 || hasSnapshotThreadURL(doc.ThreadURL) {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if doc.Form != nil {
			form, err := parseSnapshotForm(doc.Form, base)
			if err != nil {
				return Page{}, snapshotError(operation, ErrMalformedPage)
			}
			page.Form = &form
		}

	case "metrics":
		if doc.Posts != nil && len(*doc.Posts) > 0 {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if doc.Form != nil || hasSnapshotThreadURL(doc.ThreadURL) {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		metrics, err := parseSnapshotMetrics(doc.Metrics)
		if err != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.Metrics = &metrics

	case "compose":
		if doc.Posts != nil && len(*doc.Posts) > 0 {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if len(doc.Metrics) > 0 || hasSnapshotThreadURL(doc.ThreadURL) {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if doc.Form == nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		form, err := parseSnapshotForm(doc.Form, base)
		if err != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.Form = &form

	case "submitted":
		if doc.Posts != nil && len(*doc.Posts) > 0 {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if len(doc.Metrics) > 0 || doc.Form != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if !hasSnapshotThreadURL(doc.ThreadURL) {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		threadURL, err := resolveSameOriginURL(*doc.ThreadURL, base)
		if err != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.ThreadURL = threadURL
	}

	return page, nil
}

func parseSnapshotPosts(payload []snapshotPost, base *url.URL) ([]Post, error) {
	posts := make([]Post, 0, len(payload))
	seenIDs := make(map[string]struct{}, len(payload))
	for _, item := range payload {
		if item.ID == nil || item.Username == nil || item.Text == nil || item.URL == nil {
			return nil, ErrMalformedPage
		}
		id := *item.ID
		username := *item.Username
		text := *item.Text
		if strings.TrimSpace(id) == "" || strings.TrimSpace(username) == "" || strings.TrimSpace(text) == "" {
			return nil, ErrMalformedPage
		}
		if id != strings.TrimSpace(id) || username != strings.TrimSpace(username) {
			return nil, ErrMalformedPage
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return nil, ErrMalformedPage
		}
		seenIDs[id] = struct{}{}
		postURL, err := resolveSameOriginURL(*item.URL, base)
		if err != nil {
			return nil, ErrMalformedPage
		}
		posts = append(posts, Post{
			ID:               id,
			Username:         username,
			Text:             text,
			URL:              postURL,
			IsReply:          item.IsReply,
			AddressedToAgent: item.AddressedToAgent,
		})
	}
	return posts, nil
}

func parseSnapshotMetrics(raw map[string]string) (Metrics, error) {
	if len(raw) != 4 {
		return Metrics{}, ErrMalformedPage
	}
	values := make(map[string]int64, 4)
	for _, name := range []string{"views", "likes", "replies", "reposts"} {
		text, ok := raw[name]
		if !ok {
			return Metrics{}, ErrMalformedPage
		}
		value, err := parseSnapshotMetricValue(text)
		if err != nil {
			return Metrics{}, ErrMalformedPage
		}
		values[name] = value
	}
	for name := range raw {
		switch name {
		case "views", "likes", "replies", "reposts":
		default:
			return Metrics{}, ErrMalformedPage
		}
	}
	return Metrics{
		Views:   values["views"],
		Likes:   values["likes"],
		Replies: values["replies"],
		Reposts: values["reposts"],
	}, nil
}

func parseSnapshotMetricValue(text string) (int64, error) {
	if text == "" {
		return 0, ErrMalformedPage
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, ErrMalformedPage
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value < 0 || value > snapshotMaxMetric {
		return 0, ErrMalformedPage
	}
	return value, nil
}

func parseSnapshotForm(form *Form, base *url.URL) (Form, error) {
	if form == nil || strings.TrimSpace(form.Action) == "" {
		return Form{}, ErrMalformedPage
	}
	if !strings.EqualFold(strings.TrimSpace(form.Method), "POST") {
		return Form{}, ErrMalformedPage
	}
	if !form.Composer || !form.Submit {
		return Form{}, ErrMalformedPage
	}
	action, err := resolveSameOriginURL(form.Action, base)
	if err != nil {
		return Form{}, ErrMalformedPage
	}
	return Form{Action: action, Method: "POST", Composer: true, Submit: true}, nil
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

// resolveSameOriginURL resolves a relative reference against the configured base
// and rejects userinfo, non-http(s), opaque and cross-origin results.
func resolveSameOriginURL(raw string, base *url.URL) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrMalformedPage
	}
	if strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || r == '\\' }) >= 0 {
		return "", ErrMalformedPage
	}
	ref, err := url.Parse(raw)
	if err != nil || ref.Opaque != "" {
		return "", ErrMalformedPage
	}
	resolved := base.ResolveReference(ref)
	if !strings.EqualFold(resolved.Scheme, "http") && !strings.EqualFold(resolved.Scheme, "https") {
		return "", ErrMalformedPage
	}
	if resolved.Host == "" || resolved.User != nil {
		return "", ErrMalformedPage
	}
	if snapshotOrigin(resolved) != snapshotOrigin(base) {
		return "", ErrMalformedPage
	}
	return resolved.String(), nil
}

func snapshotOrigin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

func hasSnapshotThreadURL(value *string) bool {
	return value != nil && strings.TrimSpace(*value) != ""
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	return ErrMalformedPage
}

// rejectDuplicateJSONKeys walks the whole document and fails on any repeated
// object name, including nested post and metric objects.
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrMalformedPage
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrMalformedPage
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrMalformedPage
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token() // closing '}'
		return err
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token() // closing ']'
		return err
	default:
		return ErrMalformedPage
	}
}
