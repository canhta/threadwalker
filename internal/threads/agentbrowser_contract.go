package threads

// This file is the parser boundary for the agent-browser backend. The backend
// reads bounded rendered page text and accepts only the invented, versioned
// agent-browser-v1 fixture document. It does not parse live Threads markup:
// pages without the contract fail closed as unknown. Post text is data only.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
)

const (
	agentBrowserContract  = "agent-browser-v1"
	agentBrowserMarker    = "twab1 "
	agentBrowserMaxPosts  = 50
	agentBrowserMaxText   = 8 << 10
	agentBrowserMaxMarker = 16 << 10
	agentBrowserMaxFields = 8
)

// parseAgentBrowserDocument validates one bounded rendered-text document. Only
// the leading marker block is authoritative; rendered labels after it are
// ignored, and a marker line outside that block cannot be injected.
func parseAgentBrowserDocument(content, expectedKind, baseURL string) (Page, error) {
	operation, ok := operationForExpectedKind(expectedKind)
	if !ok {
		return Page{}, snapshotError("parse", ErrUnknownPage)
	}
	if len(content) == 0 || len(content) > snapshotMaxBytes {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}
	base, err := parseSnapshotBase(baseURL)
	if err != nil {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}
	if !hasAgentBrowserMarker(content) {
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}
	document, err := decodeAgentBrowserDocument(content)
	if err != nil {
		if errors.Is(err, ErrUnknownPage) {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}
	if document.auth != "" {
		switch document.auth {
		case "logged_out":
			return Page{}, snapshotError(operation, ErrLoggedOut)
		case "captcha":
			return Page{}, snapshotError(operation, ErrCaptcha)
		default:
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
	}
	kind := document.kind
	switch kind {
	case "feed", "search", "post", "metrics", "action_metrics", "compose", "submitted":
	default:
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}
	if expectedKind != "" && kind != expectedKind {
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}
	page := Page{Kind: kind}
	switch kind {
	case "feed", "search":
		if document.form != nil || document.metrics != nil || document.threadURL != "" {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.Posts, err = resolveAgentBrowserPosts(document.posts, base)
		if err != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
	case "post":
		if len(document.posts) != 1 || document.metrics != nil || document.threadURL != "" {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.Posts, err = resolveAgentBrowserPosts(document.posts, base)
		if err != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if document.form != nil {
			form, formErr := resolveAgentBrowserForm(document.form, base)
			if formErr != nil {
				return Page{}, snapshotError(operation, ErrMalformedPage)
			}
			page.Form = &form
		}
	case "metrics", "action_metrics":
		if len(document.posts) > 0 || document.form != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if kind == "metrics" && document.threadURL != "" {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		if kind == "action_metrics" {
			if document.threadURL == "" {
				return Page{}, snapshotError(operation, ErrMalformedPage)
			}
			page.ThreadURL, err = resolveSameOriginURL(document.threadURL, base)
			if err != nil {
				return Page{}, snapshotError(operation, ErrMalformedPage)
			}
		}
		if document.metrics == nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.Metrics = document.metrics
	case "compose":
		if len(document.posts) > 0 || document.metrics != nil || document.threadURL != "" || document.form == nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		form, formErr := resolveAgentBrowserForm(document.form, base)
		if formErr != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.Form = &form
	case "submitted":
		if len(document.posts) > 0 || document.metrics != nil || document.form != nil || document.threadURL == "" {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
		page.ThreadURL, err = resolveSameOriginURL(document.threadURL, base)
		if err != nil {
			return Page{}, snapshotError(operation, ErrMalformedPage)
		}
	}
	return page, nil
}

type agentBrowserDocument struct {
	kind      string
	posts     []agentBrowserPost
	metrics   *Metrics
	threadURL string
	form      *Form
	auth      string
}

type agentBrowserPost struct {
	id, username, url, text string
	reply, addressed        bool
}

func decodeAgentBrowserDocument(content string) (agentBrowserDocument, error) {
	var document agentBrowserDocument
	seenContract, seenPage := false, false
	metrics := map[string]string{}
	seenIDs := map[string]struct{}{}
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, agentBrowserMarker) {
			// Rendered UI labels (for example a submit button) follow the
			// marker block. Only the leading marker block is authoritative;
			// page text after it is ignored so user content cannot add markers.
			break
		}
		fields, err := decodeAgentBrowserFields(strings.TrimPrefix(line, agentBrowserMarker))
		if err != nil {
			return agentBrowserDocument{}, ErrMalformedPage
		}
		key, err := agentBrowserString(fields, 0)
		if err != nil {
			return agentBrowserDocument{}, ErrMalformedPage
		}
		switch key {
		case "contract":
			if seenContract || len(fields) != 2 {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			value, err := agentBrowserString(fields, 1)
			if err != nil || value != agentBrowserContract {
				return agentBrowserDocument{}, ErrUnknownPage
			}
			seenContract = true
		case "page":
			if !seenContract || seenPage || len(fields) != 2 {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			value, err := agentBrowserString(fields, 1)
			if err != nil || value == "" {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			document.kind = value
			seenPage = true
		case "post":
			if !seenPage || len(fields) != 7 || len(document.posts) >= agentBrowserMaxPosts {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			post := agentBrowserPost{}
			if post.id, err = agentBrowserString(fields, 1); err != nil || strings.TrimSpace(post.id) == "" || post.id != strings.TrimSpace(post.id) {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			if post.username, err = agentBrowserString(fields, 2); err != nil || strings.TrimSpace(post.username) == "" || post.username != strings.TrimSpace(post.username) {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			if post.url, err = agentBrowserString(fields, 3); err != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			if post.text, err = agentBrowserString(fields, 4); err != nil || strings.TrimSpace(post.text) == "" || len(post.text) > agentBrowserMaxText {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			if err = json.Unmarshal(fields[5], &post.reply); err != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			if err = json.Unmarshal(fields[6], &post.addressed); err != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			if _, duplicate := seenIDs[post.id]; duplicate {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			seenIDs[post.id] = struct{}{}
			document.posts = append(document.posts, post)
		case "metric":
			if !seenPage || len(fields) != 3 {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			name, err := agentBrowserString(fields, 1)
			if err != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			value, err := agentBrowserString(fields, 2)
			if err != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			switch name {
			case "views", "likes", "replies", "reposts":
			default:
				return agentBrowserDocument{}, ErrMalformedPage
			}
			if _, duplicate := metrics[name]; duplicate {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			metrics[name] = value
		case "thread":
			if !seenPage || len(fields) != 2 || document.threadURL != "" {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			if document.threadURL, err = agentBrowserString(fields, 1); err != nil || strings.TrimSpace(document.threadURL) == "" {
				return agentBrowserDocument{}, ErrMalformedPage
			}
		case "form":
			if !seenPage || len(fields) != 3 || document.form != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			action, err := agentBrowserString(fields, 1)
			if err != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			method, err := agentBrowserString(fields, 2)
			if err != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			document.form = &Form{Action: action, Method: method, Composer: true, Submit: true}
		case "auth":
			if !seenContract || seenPage || len(fields) != 2 || document.auth != "" {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			if document.auth, err = agentBrowserString(fields, 1); err != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
		default:
			return agentBrowserDocument{}, ErrMalformedPage
		}
	}
	if !seenContract {
		return agentBrowserDocument{}, ErrUnknownPage
	}
	if document.auth != "" {
		return document, nil
	}
	if !seenPage {
		return agentBrowserDocument{}, ErrMalformedPage
	}
	if len(metrics) > 0 {
		if len(metrics) != 4 {
			return agentBrowserDocument{}, ErrMalformedPage
		}
		values := Metrics{}
		for name, raw := range metrics {
			value, err := parseSnapshotMetricValue(raw)
			if err != nil {
				return agentBrowserDocument{}, ErrMalformedPage
			}
			switch name {
			case "views":
				values.Views = value
			case "likes":
				values.Likes = value
			case "replies":
				values.Replies = value
			case "reposts":
				values.Reposts = value
			}
		}
		document.metrics = &values
	}
	return document, nil
}

func hasAgentBrowserMarker(content string) bool {
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		return strings.HasPrefix(line, agentBrowserMarker)
	}
	return false
}

func decodeAgentBrowserFields(payload string) ([]json.RawMessage, error) {
	if len(payload) == 0 || len(payload) > agentBrowserMaxMarker {
		return nil, ErrMalformedPage
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(payload)))
	var fields []json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return nil, ErrMalformedPage
	}
	if len(fields) == 0 || len(fields) > agentBrowserMaxFields {
		return nil, ErrMalformedPage
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, ErrMalformedPage
	}
	return fields, nil
}

func agentBrowserString(fields []json.RawMessage, index int) (string, error) {
	if index >= len(fields) {
		return "", ErrMalformedPage
	}
	var value string
	if err := json.Unmarshal(fields[index], &value); err != nil {
		return "", ErrMalformedPage
	}
	return value, nil
}

func resolveAgentBrowserPosts(posts []agentBrowserPost, base *url.URL) ([]Post, error) {
	out := make([]Post, 0, len(posts))
	for _, post := range posts {
		postURL, err := resolveSameOriginURL(post.url, base)
		if err != nil {
			return nil, err
		}
		out = append(out, Post{ID: post.id, Username: post.username, Text: post.text, URL: postURL, IsReply: post.reply, AddressedToAgent: post.addressed})
	}
	return out, nil
}

func resolveAgentBrowserForm(form *Form, base *url.URL) (Form, error) {
	if form == nil {
		return Form{}, ErrMalformedPage
	}
	return parseSnapshotForm(form, base)
}
