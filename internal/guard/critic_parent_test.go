package guard_test

import (
	"context"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/threads"
)

// The critic grounds names and numbers in the parent text only, so a post must
// hand it the text of the item it anchors to: a trend's title and summary, or a
// post's own text.
func TestPostCriticParentCarriesTheTextOfItsAnchor(t *testing.T) {
	for name, tc := range map[string]struct {
		note func(*guard.Guard)
		ref  string
		want string
	}{
		"trend": {func(g *guard.Guard) {
			g.NoteTrendText("https://example.test/trend/1", "Model mới giá 3 đô. Ra mắt hôm nay.")
		}, "t1", "Model mới giá 3 đô. Ra mắt hôm nay."},
		"post": {func(g *guard.Guard) {
			g.NotePost(context.Background(), threads.Post{URL: criticPostURL, Text: "vé concert tăng giá gấp đôi"})
		}, "p1", "vé concert tăng giá gấp đôi"},
	} {
		t.Run(name, func(t *testing.T) {
			_, scope := setup(t)
			var seen quality.CriticInput
			scope.Critic = func(_ context.Context, in quality.CriticInput) (quality.CriticVerdict, error) {
				seen = in
				return verdict(true), nil
			}
			scope.Template = policyTemplate()
			scope.Template.ID = "agent"
			scope.Template.Guard.TrendAnchor = true
			g, err := guard.New(scope)
			if err != nil {
				t.Fatal(err)
			}
			tc.note(g)
			args := `{"text":"giá chỉ 3 đô thôi á","hook":"number","rationale":"r","trend":"` + tc.ref + `"}`
			if _, err := g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: args}, func() (string, error) { return "sent", nil }); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(seen.Parent, tc.want) {
				t.Fatalf("critic parent %q lacks the anchor text %q", seen.Parent, tc.want)
			}
		})
	}
}
