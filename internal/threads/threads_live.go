package threads

import (
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/canhta/threadz/internal/stdx"
)

const (
	threadsSnapshotMax = 1 << 20
	threadsLineMax     = 8 << 10
	threadsNodesMax    = 20000
	threadsPostsMax    = 50
	threadsTextMax     = 8 << 10
)

func isThreadsHost(u *url.URL) bool {
	if u == nil || !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	_, ok := stdx.ThreadsHost(u.Hostname())
	return ok
}

// IsTopicURL reports whether raw opens a Threads topic or search page, the
// only kind of link read_topic can read.
func IsTopicURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && isThreadsHost(u) && u.Path == "/search"
}

type axNode struct {
	role     string
	name     string
	ref      string
	url      string
	children []*axNode
}

func (n *axNode) walk(visit func(*axNode) bool) {
	if n == nil || !visit(n) {
		return
	}
	for _, child := range n.children {
		child.walk(visit)
	}
}

// parseAXTree parses the two-space-indented accessibility snapshot format used
// by agent-browser and Orca. It is bounded and rejects skipped indentation.
func parseAXTree(text string) (*axNode, error) {
	if len(text) == 0 || len(text) > threadsSnapshotMax {
		return nil, ErrMalformedPage
	}
	root := &axNode{role: "root"}
	stack := []*axNode{root}
	nodes := 0
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > threadsLineMax {
			return nil, ErrMalformedPage
		}
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if indent%2 != 0 || !strings.HasPrefix(trimmed, "- ") {
			return nil, ErrMalformedPage
		}
		depth := indent / 2
		if depth+1 > len(stack) {
			return nil, ErrMalformedPage
		}
		node, err := parseAXLine(strings.TrimPrefix(trimmed, "- "))
		if err != nil {
			return nil, ErrMalformedPage
		}
		stack = stack[:depth+1]
		parent := stack[depth]
		parent.children = append(parent.children, node)
		stack = append(stack, node)
		nodes++
		if nodes > threadsNodesMax {
			return nil, ErrMalformedPage
		}
	}
	return root, nil
}

func parseAXLine(rest string) (*axNode, error) {
	node := &axNode{}
	i := 0
	for i < len(rest) && rest[i] != ' ' && rest[i] != '"' && rest[i] != '[' {
		i++
	}
	node.role = rest[:i]
	if node.role == "" {
		return nil, ErrMalformedPage
	}
	rest = strings.TrimLeft(rest[i:], " ")
	if strings.HasPrefix(rest, "\"") {
		name, remaining, err := parseAXQuoted(rest)
		if err != nil {
			return nil, err
		}
		node.name = name
		rest = remaining
	}
	for {
		rest = strings.TrimLeft(rest, " ")
		if !strings.HasPrefix(rest, "[") {
			break
		}
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return nil, ErrMalformedPage
		}
		parseAXAttrs(rest[1:end], node)
		rest = rest[end+1:]
	}
	return node, nil
}

func parseAXQuoted(s string) (string, string, error) {
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '"' {
			value, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return "", "", ErrMalformedPage
			}
			return value, s[i+1:], nil
		}
	}
	return "", "", ErrMalformedPage
}

func parseAXAttrs(raw string, node *axNode) {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ref":
			node.ref = strings.TrimSpace(value)
		case "url":
			node.url = strings.TrimSpace(value)
		}
	}
}

// parseThreadsPage maps one measured Threads accessibility tree to a Page.
func parseThreadsPage(snapshot, pageURL, expectedKind, baseURL string) (Page, error) {
	operation, ok := operationForExpectedKind(expectedKind)
	if !ok {
		return Page{}, snapshotError("parse", ErrUnknownPage)
	}
	base, err := parseSnapshotBase(baseURL)
	if err != nil {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}
	page, err := url.Parse(pageURL)
	if err != nil || !sameOrigin(base, page) {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}
	root, err := parseAXTree(snapshot)
	if err != nil {
		return Page{}, snapshotError(operation, ErrMalformedPage)
	}
	if threadsCaptcha(root) {
		return Page{}, snapshotError(operation, ErrCaptcha)
	}
	authenticated := threadsAuthenticated(root)
	if !authenticated && threadsLoggedOut(root) {
		return Page{}, snapshotError(operation, ErrLoggedOut)
	}

	if expectedKind == "profile" {
		if !authenticated || threadsUserFromPath(page.Path) == "" {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		return Page{Kind: "profile", Followers: threadsFollowers(root)}, nil
	}
	switch {
	case expectedKind == "feed" && authenticated && threadsFeedPath(page.Path):
	case expectedKind == "topic" && authenticated && page.Path == "/search":
	case expectedKind == "post" && authenticated && threadsPostPath(page.Path):
	default:
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}
	posts, err := threadsPosts(root, base)
	if err != nil {
		return Page{}, snapshotError(operation, err)
	}
	if expectedKind == "feed" {
		return Page{Kind: "feed", Posts: posts, Trends: threadsTrends(root, base)}, nil
	}
	if expectedKind == "topic" {
		return Page{Kind: "topic", Posts: posts}, nil
	}
	// A reply goes only to the thread this page shows.
	matched := false
	for _, post := range posts {
		matched = matched || sameThreadPath(post.URL, page)
	}
	if !matched {
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}
	result := Page{Kind: "post", Posts: posts}
	if form, ok := threadsInlineComposer(root, pageURL); ok {
		result.Form = &form
	}
	return result, nil
}

// followersLabel matches a profile's follower count in an accessible name, such
// as "1 follower", "1.2K followers" or "1,2K người theo dõi". "Net followers"
// carries no number and never matches.
var followersLabel = regexp.MustCompile(`(?i)(\d[\d.,]*)\s*([km])?\s*(?:followers?\b|người theo dõi)`)

// threadsFollowers returns the first follower count the page names, or nil.
func threadsFollowers(root *axNode) *int64 {
	var found *int64
	root.walk(func(n *axNode) bool {
		if found == nil {
			found = parseFollowers(n.name)
		}
		return found == nil
	})
	return found
}

// parseFollowers reads the count in a label. A comma or dot is a decimal
// separator only before K or M; elsewhere it groups thousands.
func parseFollowers(label string) *int64 {
	m := followersLabel.FindStringSubmatch(label)
	if m == nil {
		return nil
	}
	var n int64
	if m[2] == "" {
		v, err := strconv.ParseInt(strings.NewReplacer(".", "", ",", "").Replace(m[1]), 10, 64)
		if err != nil || v > snapshotMaxMetric {
			return nil
		}
		n = v
	} else {
		v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
		unit := map[string]float64{"k": 1e3, "m": 1e6}[strings.ToLower(m[2])]
		if err != nil || v*unit > snapshotMaxMetric {
			return nil
		}
		n = int64(math.Round(v * unit))
	}
	return &n
}

// threadsTrendsMax bounds the Trending-now items one read returns.
const threadsTrendsMax = 10

// threadsTrends reads the Trending-now side panel from the same snapshot as the
// feed: links to a search page with serp_type=trends whose text children are
// title, summary, "·" and the post count.
func threadsTrends(root *axNode, base *url.URL) []Trend {
	var out []Trend
	root.walk(func(n *axNode) bool {
		if n.role != "link" || len(out) >= threadsTrendsMax {
			return true
		}
		u, err := url.Parse(n.url)
		if err != nil || !sameOrigin(base, u) || u.Path != "/search" || u.Query().Get("serp_type") != "trends" {
			return true
		}
		var texts []string
		for _, c := range n.children {
			if c.role == "StaticText" && strings.TrimSpace(c.name) != "" {
				texts = append(texts, c.name)
			}
		}
		if len(texts) == 0 {
			return true
		}
		t := Trend{Title: texts[0], URL: u.String()}
		if len(texts) > 1 && texts[1] != "·" {
			t.Summary = texts[1]
		}
		for i, s := range texts {
			if s == "·" && i+1 < len(texts) {
				t.Posts = texts[i+1]
			}
		}
		t.PostCount = parseCount(t.Posts)
		out = append(out, t)
		return true
	})
	return out
}

func threadsFeedPath(path string) bool {
	switch path {
	case "/", "/for_you", "/following/":
		return true
	default:
		return false
	}
}

func threadsUsername(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if r == '.' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return true
}

func threadsID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return true
}

func threadsUserFromPath(path string) string {
	if !strings.HasPrefix(path, "/@") {
		return ""
	}
	value := strings.TrimSuffix(strings.TrimPrefix(path, "/@"), "/")
	if !threadsUsername(value) {
		return ""
	}
	return value
}

func threadsAuthenticated(root *axNode) bool {
	profile, insights := false, false
	root.walk(func(n *axNode) bool {
		if n.role != "link" {
			return true
		}
		u, err := url.Parse(n.url)
		if err != nil {
			return true
		}
		if strings.Contains(strings.ToLower(n.name), "profile") && threadsUserFromPath(u.Path) != "" {
			profile = true
		}
		if strings.Contains(strings.ToLower(n.name), "insights") && strings.TrimSuffix(u.Path, "/") == "/insights" {
			insights = true
		}
		return true
	})
	return profile && insights
}

func threadsLoggedOut(root *axNode) bool {
	found := false
	root.walk(func(n *axNode) bool {
		lower := strings.ToLower(n.name)
		if n.role == "button" || n.role == "link" {
			if strings.Contains(lower, "continue with instagram") || strings.Contains(lower, "log in") || strings.Contains(lower, "login") {
				found = true
			}
		}
		if n.role == "dialog" && strings.Contains(lower, "say more with threads") {
			found = true
		}
		return true
	})
	return found
}

func threadsLoading(root *axNode) bool {
	loading := false
	root.walk(func(n *axNode) bool {
		if n.role == "status" && strings.Contains(strings.ToLower(n.name), "loading") {
			loading = true
			return false
		}
		return true
	})
	return loading
}

func threadsCaptcha(root *axNode) bool {
	found := false
	root.walk(func(n *axNode) bool {
		if n.role != "heading" && n.role != "dialog" {
			return true
		}
		lower := strings.ToLower(n.name)
		if strings.Contains(lower, "captcha") || strings.Contains(lower, "verify you are human") || strings.Contains(lower, "security check") {
			found = true
		}
		return true
	})
	return found
}

func threadsColumn(root *axNode) *axNode {
	var region *axNode
	root.walk(func(n *axNode) bool {
		if region != nil {
			return false
		}
		if n.role == "region" && strings.EqualFold(n.name, "Column body") {
			region = n
			return false
		}
		return true
	})
	return region
}

func threadsPosts(root *axNode, base *url.URL) ([]Post, error) {
	region := threadsColumn(root)
	if region == nil {
		return nil, ErrUnknownPage
	}
	posts := make([]Post, 0, 8)
	seen := map[string]bool{}
	var walk func(*axNode)
	walk = func(node *axNode) {
		if len(posts) >= threadsPostsMax {
			return
		}
		for _, child := range node.children {
			if post, ok := threadsPost(child, base); ok {
				if !seen[post.URL] {
					seen[post.URL] = true
					posts = append(posts, post)
				}
				continue
			}
			walk(child)
		}
	}
	walk(region)
	return posts, nil
}

func threadsPost(node *axNode, base *url.URL) (Post, bool) {
	if node.role != "generic" || len(node.children) == 0 {
		return Post{}, false
	}
	author, permalink, topic := "", "", ""
	var permalinkURL string
	var stamp *axNode
	links := threadsPostLinks(node)
	// A wrapper around several posts is not one post.
	permalinks := map[string]bool{}
	for _, child := range links {
		if u, err := url.Parse(child.url); err == nil && sameOrigin(base, u) {
			if _, _, ok := threadsPostPathParts(u.Path); ok {
				permalinks[u.Path] = true
			}
		}
	}
	if len(permalinks) > 1 {
		return Post{}, false
	}
	for _, child := range links {
		u, err := url.Parse(child.url)
		if err != nil || !sameOrigin(base, u) {
			continue
		}
		if u.Path == "/search" && u.Query().Get("serp_type") == "tags" && topic == "" {
			topic = strings.TrimSpace(child.name)
			continue
		}
		if user, _, ok := threadsPostPathParts(u.Path); ok {
			if permalink == "" {
				permalink = user
				permalinkURL = u.String()
				stamp = child
			}
			break
		}
		if user := threadsUserFromPath(u.Path); user != "" && author == "" && !strings.Contains(strings.ToLower(child.name), "profile picture") {
			if child.name == "" || child.name == user || !strings.Contains(strings.ToLower(child.name), "profile") {
				author = user
			}
		}
	}
	if author == "" || permalink == "" || author != permalink {
		return Post{}, false
	}
	text := threadsPostText(node)
	if text == "" {
		return Post{}, false
	}
	if len(text) > threadsTextMax {
		return Post{}, false
	}
	post := Post{Username: author, Text: text, URL: permalinkURL, Topic: topic, AgeMinutes: threadsAge(stamp)}
	post.Likes, post.Replies, post.Reposts = threadsCounters(node)
	return post, true
}

// threadsPostLinks lists the links of a post block in order: its direct links
// and those inside plain generic wrappers, where Threads puts the author or the
// timestamp of some posts (a pinned tag, a nameless profile button).
func threadsPostLinks(node *axNode) []*axNode {
	var links []*axNode
	for _, child := range node.children {
		switch {
		case child.role == "link" && child.url != "":
			links = append(links, child)
		case child.role == "generic":
			links = append(links, threadsPostLinks(child)...)
		}
	}
	return links
}

func threadsPostText(node *axNode) string {
	parts := make([]string, 0, 8)
	for _, child := range node.children {
		if child.role == "StaticText" && strings.TrimSpace(child.name) != "" && !threadsUIText(child.name) {
			parts = append(parts, child.name)
		}
	}
	out := parts[:0]
	for i := 0; i < len(parts); i++ {
		if i+2 < len(parts) && digitsOnly(parts[i]) && parts[i+1] == "/" && digitsOnly(parts[i+2]) {
			i += 2
			continue
		}
		out = append(out, parts[i])
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// threadsUIText filters small measured post-block labels that are not body
// text. Only exact labels are removed so real prose is never discarded.
func threadsUIText(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "no replies yet", "translate", "see translation", "view activity", "follow":
		return true
	default:
		return false
	}
}

func threadsLoadingText(snapshot string) bool {
	root, err := parseAXTree(snapshot)
	if err != nil {
		return false
	}
	return threadsLoading(root)
}

func threadsPostPathParts(path string) (string, string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "@") || parts[1] != "post" {
		return "", "", false
	}
	username := strings.TrimPrefix(parts[0], "@")
	if !threadsUsername(username) || !threadsID(parts[2]) {
		return "", "", false
	}
	return username, parts[2], true
}

func digitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func threadsPostPath(path string) bool {
	_, _, ok := threadsPostPathParts(path)
	return ok
}

func sameThreadPath(raw string, page *url.URL) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.TrimSuffix(u.Path, "/") == strings.TrimSuffix(page.Path, "/")
}

// threadsInlineComposer recognizes the post page's inline reply composer. The
// measured composer has one textbox inside the post block; its submit control
// ("Reply") only appears after text is entered, so recognition is textbox-based.
func threadsInlineComposer(root *axNode, pageURL string) (Form, bool) {
	if threadsColumn(root) == nil {
		return Form{}, false
	}
	container, ok := threadsComposerContainer(root)
	if !ok || !isThreadsPostBlock(container) {
		return Form{}, false
	}
	if _, ok = threadsComposerTextbox(container); !ok {
		return Form{}, false
	}
	return Form{Action: pageURL, Method: "POST", Composer: true, Submit: true}, true
}

// threadsComposerContainer returns the innermost measured composer container:
// either a dialog with a New thread/Reply heading or a post block holding the
// inline reply textbox. Scoping to that container keeps a reply submit from
// resolving to an unrelated Reply button in another post.
func threadsComposerContainer(root *axNode) (*axNode, bool) {
	var found *axNode
	root.walk(func(n *axNode) bool {
		if n == nil {
			return true
		}
		if isThreadsComposerDialog(n) || (isThreadsPostBlock(n) && nodeHasTextbox(n)) {
			found = n
		}
		return true
	})
	return found, found != nil
}

func isThreadsComposerDialog(node *axNode) bool {
	if node.role != "dialog" {
		return false
	}
	heading := false
	node.walk(func(n *axNode) bool {
		if n.role == "heading" && (strings.EqualFold(n.name, "New thread") || strings.EqualFold(n.name, "Reply")) {
			heading = true
			return false
		}
		return true
	})
	return heading
}

func isThreadsPostBlock(node *axNode) bool {
	if node.role != "generic" {
		return false
	}
	_, _, ok := threadsPostBlockPermalink(node, nil)
	return ok
}

func nodeHasTextbox(node *axNode) bool {
	found := false
	node.walk(func(n *axNode) bool {
		if n.role == "textbox" {
			found = true
			return false
		}
		return true
	})
	return found
}

func threadsComposerTextbox(container *axNode) (*axNode, bool) {
	var textbox *axNode
	container.walk(func(n *axNode) bool {
		if textbox != nil {
			return false
		}
		if n.role == "textbox" && n.ref != "" {
			textbox = n
			return false
		}
		return true
	})
	return textbox, textbox != nil
}

// threadsComposerControls resolves the textbox and its submit control inside one
// composer container. The submit is the first Post/Reply button that follows the
// textbox in document order, which excludes the post's metric Reply button (it
// precedes the inline composer) and reply blocks further down the page.
func threadsComposerControls(root *axNode) (textbox, submit *axNode, ok bool) {
	container, found := threadsComposerContainer(root)
	if !found {
		return nil, nil, false
	}
	textbox, ok = threadsComposerTextbox(container)
	if !ok {
		return nil, nil, false
	}
	// The submit is the first Post/Reply button after the textbox in document
	// order. The real composer wraps the textbox in a generic node, so it is not
	// the textbox's sibling. A nested post block is skipped: its Reply buttons
	// belong to other posts.
	seen := false
	var find func(n *axNode) bool
	find = func(n *axNode) bool {
		if n == textbox {
			seen = true
			return false
		}
		if seen && n.role == "button" && n.ref != "" && (strings.EqualFold(n.name, "Post") || strings.EqualFold(n.name, "Reply")) {
			submit = n
			return true
		}
		if n != container && isThreadsPostBlock(n) {
			return false
		}
		for _, child := range n.children {
			if find(child) {
				return true
			}
		}
		return false
	}
	find(container)
	return textbox, submit, submit != nil
}

// threadsOwnUsername reads the measured authenticated navigation's Profile
// link. It is used only to recognize a newly published own post/reply.
func threadsOwnUsername(root *axNode) string {
	own := ""
	root.walk(func(n *axNode) bool {
		if own != "" {
			return false
		}
		if n.role != "link" || !(strings.EqualFold(n.name, "Profile") || strings.EqualFold(n.name, "Profile Profile")) {
			return true
		}
		u, err := url.Parse(n.url)
		if err != nil {
			return true
		}
		if user := threadsUserFromPath(u.Path); user != "" {
			own = user
			return false
		}
		return true
	})
	return own
}

// threadsAckURL recognizes a measured successful write acknowledgement: either
// the status toast whose "View" link points at the new own post/reply, or a post
// block with the submitted text / new own permalink that was not present before
// the click. It never guesses from unrelated page text.
func threadsAckURL(root *axNode, base *url.URL, ownUser, submitted string, known map[string]bool) (string, bool) {
	toast := ""
	root.walk(func(n *axNode) bool {
		if toast != "" {
			return false
		}
		if n.role != "status" {
			return true
		}
		n.walk(func(m *axNode) bool {
			if m.role != "link" || !strings.EqualFold(strings.TrimSpace(m.name), "View") {
				return true
			}
			u, err := url.Parse(m.url)
			if err != nil || !sameOrigin(base, u) {
				return true
			}
			user, _, ok := threadsPostPathParts(u.Path)
			if !ok || (ownUser != "" && !strings.EqualFold(user, ownUser)) {
				return true
			}
			toast = u.String()
			return false
		})
		return toast == ""
	})
	if toast != "" && !known[toast] {
		return toast, true
	}

	match, ownNew := "", ""
	root.walk(func(n *axNode) bool {
		if n.role != "generic" {
			return true
		}
		raw, user, ok := threadsPostBlockPermalink(n, base)
		if !ok || known[raw] {
			return true
		}
		if threadsAckText(threadsPostText(n), submitted) && match == "" {
			match = raw
		}
		if ownNew == "" && ownUser != "" && strings.EqualFold(user, ownUser) {
			ownNew = raw
		}
		return true
	})
	if match != "" {
		return match, true
	}
	if ownNew != "" {
		return ownNew, true
	}
	return "", false
}

func threadsPostBlockPermalink(node *axNode, base *url.URL) (string, string, bool) {
	raw, user := "", ""
	for _, child := range threadsPostLinks(node) {
		u, err := url.Parse(child.url)
		if err != nil || (base != nil && !sameOrigin(base, u)) {
			continue
		}
		name, _, ok := threadsPostPathParts(u.Path)
		if !ok {
			continue
		}
		if raw != "" && raw != u.String() {
			return "", "", false // a wrapper around several posts is not one post
		}
		raw, user = u.String(), name
	}
	return raw, user, raw != ""
}

func threadsAckText(blockText, submitted string) bool {
	block := strings.TrimSpace(blockText)
	want := strings.TrimSpace(submitted)
	if block == "" || want == "" {
		return false
	}
	if block == want {
		return true
	}
	block = strings.TrimSuffix(block, "…")
	if len([]rune(block)) >= 12 && strings.HasPrefix(want, block) {
		return true
	}
	if len([]rune(want)) >= 12 && strings.HasPrefix(block, want) {
		return true
	}
	return false
}
