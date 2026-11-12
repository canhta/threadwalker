package threads

// This file is the measured live Threads accessibility mapping. It parses the
// compact accessibility tree produced by agent-browser/Orca snapshots. It uses
// no invented page attributes: roles, nesting, link URLs and visible text are
// the only inputs. Reads and measured guarded writes share this mapping.

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	pageProfileFixture = "fixture"
	pageProfileThreads = "threads"

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
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	return host == "threads.com" || host == "threads.net"
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

	switch expectedKind {
	case "feed":
		if !authenticated || !threadsFeedPath(page.Path) {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		posts, err := threadsPosts(root, base)
		if err != nil {
			return Page{}, snapshotError(operation, err)
		}
		return Page{Kind: "feed", Posts: posts}, nil
	case "search":
		if !authenticated || page.Path != "/search" {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		posts, err := threadsPosts(root, base)
		if err != nil {
			return Page{}, snapshotError(operation, err)
		}
		return Page{Kind: "search", Posts: posts}, nil
	case "post":
		if !authenticated || !threadsPostPath(page.Path) {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		posts, err := threadsPosts(root, base)
		if err != nil {
			return Page{}, snapshotError(operation, err)
		}
		if len(posts) == 0 {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		var result Page
		if sameThreadPath(posts[0].URL, page) {
			// The measured post page exposes the root and the author's own
			// thread continuation.
			contextPosts := []Post{posts[0]}
			for _, post := range posts[1:] {
				if post.Username == posts[0].Username {
					contextPosts = append(contextPosts, post)
				}
			}
			result = Page{Kind: "post", Posts: contextPosts}
		} else {
			// The measured reply permalink page shows the parent chain plus the
			// reply. Return the page's posts in order instead of pretending the
			// parent is the requested reply.
			matched := false
			for _, post := range posts {
				if sameThreadPath(post.URL, page) {
					matched = true
					break
				}
			}
			if !matched {
				return Page{}, snapshotError(operation, ErrUnknownPage)
			}
			result = Page{Kind: "post", Posts: posts}
		}
		if form, ok := threadsInlineComposer(root, pageURL); ok {
			result.Form = &form
		}
		return result, nil
	case "metrics":
		if !authenticated || page.Path != "/insights/" {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		// The measured Insights overview has Views/Viewers/Net followers/
		// Interactions, not likes/replies/reposts. Never invent zeros.
		return Page{}, snapshotError(operation, ErrMetricsUnavailable)
	case "action_metrics":
		if !authenticated {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		// Per-post insights were not measured; aggregate counters are never
		// attributed to one action.
		return Page{}, snapshotError(operation, ErrMetricsUnavailable)
	case "compose":
		if !authenticated {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		form, ok := threadsComposer(root, pageURL, base)
		if !ok {
			return Page{}, snapshotError(operation, ErrUnknownPage)
		}
		return Page{Kind: "compose", Form: &form}, nil
	default:
		return Page{}, snapshotError(operation, ErrUnknownPage)
	}
}

func threadsFeedPath(path string) bool {
	switch path {
	case "/", "/for_you", "/following/":
		return true
	default:
		return false
	}
}

func threadsPostPath(path string) bool {
	_, _, ok := threadsPostPathParts(path)
	return ok
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

func sameThreadPath(raw string, page *url.URL) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.TrimSuffix(u.Path, "/") == strings.TrimSuffix(page.Path, "/")
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
	author, permalink := "", ""
	var permalinkURL string
	for _, child := range node.children {
		if child.role != "link" || child.url == "" {
			continue
		}
		u, err := url.Parse(child.url)
		if err != nil || !sameOrigin(base, u) {
			continue
		}
		if user, _, ok := threadsPostPathParts(u.Path); ok {
			if permalink == "" {
				permalink = user
				permalinkURL = u.String()
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
	_, id, _ := threadsPostPathParts(mustPath(permalinkURL))
	return Post{
		ID:       id,
		Username: author,
		Text:     text,
		URL:      permalinkURL,
	}, true
}

func mustPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

func threadsPostText(node *axNode) string {
	parts := make([]string, 0, 8)
	for _, child := range node.children {
		if child.role == "StaticText" && strings.TrimSpace(child.name) != "" {
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
	for _, child := range node.children {
		if child.role != "link" || child.url == "" {
			continue
		}
		u, err := url.Parse(child.url)
		if err != nil {
			continue
		}
		if _, _, ok := threadsPostPathParts(u.Path); ok {
			return true
		}
	}
	return false
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
	if isThreadsPostBlock(container) {
		// Inline composer: the submit is a sibling of the textbox. Never walk
		// into following reply blocks, whose Reply buttons are unrelated.
		parent := threadsParent(container, textbox)
		if parent == nil {
			return nil, nil, false
		}
		seen := false
		for _, child := range parent.children {
			if child == textbox {
				seen = true
				continue
			}
			if seen && child.role == "button" && child.ref != "" && (strings.EqualFold(child.name, "Post") || strings.EqualFold(child.name, "Reply")) {
				return textbox, child, true
			}
		}
		return textbox, nil, false
	}
	seen := false
	container.walk(func(n *axNode) bool {
		if n == textbox {
			seen = true
			return true
		}
		if !seen {
			return true
		}
		if n.role == "button" && n.ref != "" && (strings.EqualFold(n.name, "Post") || strings.EqualFold(n.name, "Reply")) {
			submit = n
			return false
		}
		return true
	})
	return textbox, submit, submit != nil
}

// threadsParent returns the parent of target within root, or nil.
func threadsParent(root, target *axNode) *axNode {
	var parent *axNode
	var walk func(*axNode)
	walk = func(n *axNode) {
		if n == nil || parent != nil {
			return
		}
		for _, child := range n.children {
			if child == target {
				parent = n
				return
			}
			walk(child)
		}
	}
	walk(root)
	return parent
}

func threadsComposer(root *axNode, pageURL string, base *url.URL) (Form, bool) {
	u, err := url.Parse(pageURL)
	if err != nil || !sameOrigin(base, u) {
		return Form{}, false
	}
	var dialog *axNode
	root.walk(func(n *axNode) bool {
		if dialog != nil {
			return false
		}
		if isThreadsComposerDialog(n) {
			dialog = n
			return false
		}
		return true
	})
	if dialog == nil {
		return Form{}, false
	}
	if _, ok := threadsComposerTextbox(dialog); !ok {
		return Form{}, false
	}
	post := false
	dialog.walk(func(n *axNode) bool {
		if n.role == "button" && strings.EqualFold(n.name, "Post") {
			post = true
			return false
		}
		return true
	})
	if !post {
		return Form{}, false
	}
	return Form{Action: pageURL, Method: "POST", Composer: true, Submit: true}, true
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
	for _, child := range node.children {
		if child.role != "link" || child.url == "" {
			continue
		}
		u, err := url.Parse(child.url)
		if err != nil || !sameOrigin(base, u) {
			continue
		}
		user, _, ok := threadsPostPathParts(u.Path)
		if ok {
			return u.String(), user, true
		}
	}
	return "", "", false
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

func threadsLoadingText(snapshot string) bool {
	root, err := parseAXTree(snapshot)
	if err != nil {
		return false
	}
	return threadsLoading(root)
}
