package guard_test

import (
	"context"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/threads"
)

// readPost has g read the post at url (an API post id, empty for a post only a
// browser read showed, with the given text) and returns its ref.
func readPost(t *testing.T, g *guard.Guard, url, id, text string) string {
	t.Helper()
	ref := g.NotePost(context.Background(), threads.Post{ID: id, URL: url, Text: text})
	if ref == "" {
		t.Fatalf("post %q has no ref", url)
	}
	return ref
}

// replyTo is the arguments of a reply to the post at url, which g reads first.
func replyTo(t *testing.T, g *guard.Guard, url, id, text string) string {
	t.Helper()
	return `{"text":"` + text + `","target":"` + readPost(t, g, url, id, "") + `"}`
}
