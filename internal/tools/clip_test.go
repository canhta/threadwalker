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
		posts[i] = fmt.Sprintf(`{"ref":"r%d","text":%q}`, i, strings.Repeat("é", 150))
	}
	wrapped := `{"untrusted":true,"source":"read_feed","data":{"kind":"feed","posts":[` + strings.Join(posts, ",") + `],"trends":[{"ref":"t1"}]}}`

	if got := clipResult(`{"a":1}`, MaxResultBytes); got != `{"a":1}` {
		t.Fatalf("small result changed: %s", got)
	}
	out := clipResult(wrapped, MaxResultBytes)
	var v struct {
		Untrusted bool
		Shown     int
		Total     int
		Data      struct{ Posts []struct{ Ref, Text string } }
	}
	if len(out) > MaxResultBytes || !utf8.ValidString(out) || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("invalid clip: %d bytes", len(out))
	}
	if !v.Untrusted || v.Total != 61 || v.Shown != len(v.Data.Posts)+1 || v.Shown >= 61 || len(v.Data.Posts) == 0 {
		t.Fatalf("header %+v", v)
	}
	for i, p := range v.Data.Posts { // whole items, in order, none cut
		if p.Ref != fmt.Sprintf("r%d", i) || p.Text != strings.Repeat("é", 150) {
			t.Fatalf("item %d cut: %+v", i, p)
		}
	}
	// One item bigger than the limit: shown 0, still valid.
	huge := `{"data":{"posts":[{"text":"` + strings.Repeat("é", MaxResultBytes) + `"}]}}`
	if out := clipResult(huge, MaxResultBytes); !json.Valid([]byte(out)) || !strings.Contains(out, `"shown":0`) || len(out) > MaxResultBytes {
		t.Fatalf("huge item: %.80s", out)
	}
	// No list to drop: a note, never a cut string.
	flat := `{"text":"` + strings.Repeat("é", MaxResultBytes) + `"}`
	if out := clipResult(flat, MaxResultBytes); !json.Valid([]byte(out)) || !strings.Contains(out, `"clipped":true`) {
		t.Fatalf("flat: %.80s", out)
	}
}
