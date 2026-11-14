package threads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Capture file names, in testdata/live.
const (
	CaptureMe              = "me.json"
	CaptureOwnPost         = "own_post.json"
	CaptureOwnPostMetric   = "own_post_insights.json"
	CaptureOwnReply        = "own_reply.json"
	CaptureOwnReplyMetric  = "own_reply_insights.json"
	CaptureOwnReplyReplies = "own_reply_replies.json"
	CaptureMentions        = "mentions.json"
	CaptureAccount         = "account_insights.json"
	CaptureFollowers       = "followers.json"
)

// Capture makes the read-only calls that matter for the API contract and
// returns each response redacted by Redactor, keyed by file name. It writes
// nothing and never sees more than the token source gives the API.
func (a *API) Capture(ctx context.Context) (map[string][]byte, error) {
	red := NewRedactor()
	out := map[string][]byte{}
	get := func(file, path string, q url.Values) (json.RawMessage, error) {
		var raw json.RawMessage
		if err := a.do(ctx, http.MethodGet, path, q, &raw); err != nil {
			return nil, fmt.Errorf("capturing %s: %w", file, err)
		}
		return raw, nil
	}
	put := func(file string, raw json.RawMessage) error {
		b, err := red.Redact(raw)
		if err != nil {
			return fmt.Errorf("redacting %s: %w", file, err)
		}
		out[file] = b
		return nil
	}
	// Redaction learns the account's username from the first response, so /me goes first.
	raw, err := get(CaptureMe, "/me", url.Values{"fields": {"id,username,threads_profile_picture_url"}})
	if err != nil {
		return nil, err
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(raw, &me); err != nil || me.Username == "" {
		return nil, errors.New("capturing me.json: no username in the response")
	}
	red.Own = me.Username
	if err := put(CaptureMe, raw); err != nil {
		return nil, err
	}
	first := func(path string, want func(apiPost) bool) (string, error) {
		raw, err := get(path, path, url.Values{"fields": {postFields}, "limit": {"25"}})
		if err != nil {
			return "", err
		}
		var l struct{ Data []apiPost }
		if err := json.Unmarshal(raw, &l); err != nil {
			return "", fmt.Errorf("capturing %s: %w", path, ErrAPI)
		}
		for _, p := range l.Data {
			if want(p) {
				return p.ID, nil
			}
		}
		return "", fmt.Errorf("capturing %s: the account has no matching post to capture", path)
	}
	postID, err := first("/"+a.o.UserID+"/threads", func(p apiPost) bool { return !p.IsReply })
	if err != nil {
		return nil, err
	}
	replyID, err := first("/me/replies", func(p apiPost) bool { return p.IsReply })
	if err != nil {
		return nil, err
	}
	for _, c := range []struct {
		post, insights, replies, id string
	}{{CaptureOwnPost, CaptureOwnPostMetric, "", postID}, {CaptureOwnReply, CaptureOwnReplyMetric, CaptureOwnReplyReplies, replyID}} {
		id := url.PathEscape(c.id)
		calls := []struct {
			file, path string
			q          url.Values
		}{
			{c.post, "/" + id, url.Values{"fields": {postFields}}},
			{c.insights, "/" + id + "/insights", url.Values{"metric": {postMetrics}}},
		}
		if c.replies != "" {
			calls = append(calls, struct {
				file, path string
				q          url.Values
			}{c.replies, "/" + id + "/replies", url.Values{"fields": {postFields}, "limit": {strconv.Itoa(replyCountCap)}}})
		}
		for _, call := range calls {
			if raw, err = get(call.file, call.path, call.q); err == nil {
				err = put(call.file, raw)
			}
			if err != nil {
				return nil, err
			}
		}
	}
	insights := "/" + a.o.UserID + "/threads_insights"
	for _, call := range []struct {
		file, path string
		q          url.Values
	}{
		{CaptureMentions, "/" + a.o.UserID + "/mentions", url.Values{"fields": {postFields}}},
		{CaptureAccount, insights, url.Values{"metric": {accountMetrics}}},
		{CaptureFollowers, insights, url.Values{"metric": {"followers_count"}}},
	} {
		if raw, err = get(call.file, call.path, call.q); err == nil {
			err = put(call.file, raw)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Redactor makes a captured response safe to commit while keeping its shape:
// field names, value types and timestamp formats stay. One Redactor is shared
// by all files of a capture so a username or id maps to the same stand-in in
// each. Every string is replaced unless its key says it is structure (an
// enum, a timestamp) or an id, username or URL, which are replaced by stable
// synthetic values.
type Redactor struct {
	// Own is the account's username; it is the only one kept.
	Own   string
	users map[string]string
	ids   map[string]string
	codes map[string]string
}

// NewRedactor returns an empty Redactor; set Own before redacting.
func NewRedactor() *Redactor {
	return &Redactor{users: map[string]string{}, ids: map[string]string{}, codes: map[string]string{}}
}

// structural string values that are API vocabulary, not account data.
var keepKeys = map[string]bool{"name": true, "period": true, "title": true, "description": true, "media_type": true, "media_product_type": true, "reply_audience": true, "status": true, "timestamp": true, "end_time": true}

var digits = regexp.MustCompile(`^[0-9]{8,}$`)

// Redact returns the indented, redacted form of one JSON response.
func (r *Redactor) Redact(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(r.walk("", v), "", "  ")
	return append(b, '\n'), err
}

func (r *Redactor) walk(key string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) { // sorted, so stand-ins are numbered the same on every run
			if k == "access_token" || k == "token" {
				delete(x, k)
				continue
			}
			x[k] = r.walk(k, x[k])
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = r.walk(key, e)
		}
		return x
	case string:
		return r.str(key, x)
	}
	return v
}

func (r *Redactor) str(key, s string) string {
	switch {
	case key == "id" || strings.HasSuffix(key, "_id"):
		parts := strings.Split(s, "/")
		for i, p := range parts {
			if digits.MatchString(p) {
				parts[i] = r.synth(r.ids, p, "9")
			}
		}
		return strings.Join(parts, "/")
	case key == "username":
		return r.user(s)
	case strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://"):
		return r.link(s)
	case keepKeys[key]:
		return s
	}
	return placeholder(s)
}

// synth returns the stable stand-in for s: digits of the same length that
// start with lead, or letters-and-digits of the same length for a short code.
func (r *Redactor) synth(m map[string]string, s, lead string) string {
	if v, ok := m[s]; ok {
		return v
	}
	n := strconv.Itoa(len(m) + 1)
	pad := len(s) - len(lead) - len(n)
	if pad < 0 {
		pad = 0
	}
	v := lead + strings.Repeat("0", pad) + n
	m[s] = v
	return v
}

func (r *Redactor) user(s string) string {
	if s == r.Own {
		return s
	}
	if v, ok := r.users[s]; ok {
		return v
	}
	v := "user_" + strconv.Itoa(len(r.users)+1)
	r.users[s] = v
	return v
}

// link drops the query string and fragment, which can carry tokens, and
// replaces the user and post code of a permalink; any other URL keeps only its host.
func (r *Redactor) link(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "https://example.invalid/redacted"
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 3 && strings.HasPrefix(segs[0], "@") && segs[1] == "post" {
		user := r.user(strings.TrimPrefix(segs[0], "@"))
		code := r.synth(r.codes, segs[2], "SYNTH")
		return u.Scheme + "://" + u.Host + "/@" + user + "/post/" + code
	}
	return u.Scheme + "://" + u.Host + "/redacted"
}

// placeholder is fixed text of the same rune length as s.
func placeholder(s string) string {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return ""
	}
	rs := []rune(strings.Repeat("redacted text ", n/14+1))[:n]
	if rs[n-1] == ' ' {
		rs[n-1] = 'x'
	}
	return string(rs)
}
