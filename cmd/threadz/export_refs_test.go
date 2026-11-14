package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A replay starts on an empty database, so an exported write names its posts by
// the number the replay's reads give them, not by the durable id the wake saw.
func TestExportRenumbersPostRefsInOrderOfAppearance(t *testing.T) {
	feed := json.RawMessage(`{"untrusted":true,"source":"find_posts","data":{"kind":"feed","posts":[{"ref":"p44"},{"ref":"p17"}],"trends":[{"ref":"t1"}]}}`)
	numbering := map[string]string{}
	for _, ref := range postRefs(feed) {
		if _, ok := numbering[ref]; !ok {
			numbering[ref] = "p" + string(rune('1'+len(numbering)))
		}
	}
	if want := (map[string]string{"p44": "p1", "p17": "p2"}); !reflect.DeepEqual(numbering, want) {
		t.Fatalf("numbering %v, want %v", numbering, want)
	}
	got := renumberRefs(`{"text":"x","target":"p17","trend":"t1"}`, numbering)
	if got != `{"target":"p2","text":"x","trend":"t1"}` {
		t.Fatalf("renumbered %s", got)
	}
	// A post an earlier wake stored is not in this wake's reads: it keeps its id.
	if got = renumberRefs(`{"text":"x","target":"p3"}`, numbering); got != `{"target":"p3","text":"x"}` {
		t.Fatalf("unknown ref changed: %s", got)
	}
}
