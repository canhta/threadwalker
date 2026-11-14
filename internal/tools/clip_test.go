package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipResult(t *testing.T) {
	posts := make([]string, 60)
	for i := range posts {
		posts[i] = fmt.Sprintf(`{"ref":"p%d","text":%q}`, i, strings.Repeat("é", 150))
	}
	wrapped := `{"untrusted":true,"source":"find_posts","data":{"kind":"feed","posts":[` + strings.Join(posts, ",") + `],"trends":[{"ref":"t1"}]}}`

	if got := clipResult(`{"a":1}`, MaxResultBytes); got != `{"a":1}` {
		t.Fatalf("small result changed: %s", got)
	}
	out := clipResult(wrapped, MaxResultBytes)
	var v struct {
		Untrusted    bool
		ClippedLists struct{ Shown, Total map[string]int } `json:"clipped_lists"`
		Data         struct{ Posts []struct{ Ref, Text string } }
	}
	if len(out) > MaxResultBytes || !utf8.ValidString(out) || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("invalid clip: %d bytes", len(out))
	}
	// The longest list is trimmed; the short one is kept whole.
	if !v.Untrusted || v.ClippedLists.Total["data.posts"] != 60 || v.ClippedLists.Total["data.trends"] != 1 || v.ClippedLists.Shown["data.trends"] != 1 || v.ClippedLists.Shown["data.posts"] != len(v.Data.Posts) || len(v.Data.Posts) == 0 || len(v.Data.Posts) >= 60 {
		t.Fatalf("header %+v", v)
	}
	for i, p := range v.Data.Posts { // whole items, in order, none cut
		if p.Ref != fmt.Sprintf("p%d", i) || p.Text != strings.Repeat("é", 150) {
			t.Fatalf("item %d cut: %+v", i, p)
		}
	}
	// One item bigger than the limit: shown 0, still valid.
	huge := `{"data":{"posts":[{"text":"` + strings.Repeat("é", MaxResultBytes) + `"}]}}`
	if out := clipResult(huge, MaxResultBytes); !json.Valid([]byte(out)) || !strings.Contains(out, `"shown":{"data.posts":0}`) || len(out) > MaxResultBytes {
		t.Fatalf("huge item: %.80s", out)
	}
	// No list to drop: a note, never a cut string.
	flat := `{"text":"` + strings.Repeat("é", MaxResultBytes) + `"}`
	if out := clipResult(flat, MaxResultBytes); !json.Valid([]byte(out)) || !strings.Contains(out, `"clipped":true`) {
		t.Fatalf("flat: %.80s", out)
	}
}

// Equal lists lose items in key order, and an existing shown or total key survives.
func TestClipResultTiesAndExistingKeys(t *testing.T) {
	item := `{"t":"` + strings.Repeat("x", 500) + `"}`
	list := "[" + strings.Repeat(item+",", 11) + item + "]"
	out := clipResult(`{"shown":"mine","total":"mine","b":`+list+`,"a":`+list+`}`, 8000)
	var v struct {
		Shown, Total string
		A, B         []any
		ClippedLists struct{ Shown map[string]int } `json:"clipped_lists"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Shown != "mine" || v.Total != "mine" {
		t.Fatalf("keys overwritten: %v %.80s", err, out)
	}
	if len(v.A)+len(v.B) >= 24 || len(v.A) != v.ClippedLists.Shown["a"] || len(v.B) != v.ClippedLists.Shown["b"] || len(v.A) > len(v.B) || len(v.B)-len(v.A) > 1 {
		t.Fatalf("a %d b %d, want the longest trimmed first and a before b on a tie", len(v.A), len(v.B))
	}
}
