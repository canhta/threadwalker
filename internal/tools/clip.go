package tools

import (
	"encoding/json"
	"strings"
)

// clipResult keeps a tool result within max bytes by dropping whole items from
// the end of its lists, the longest list first, and says how many of how many
// it kept with top-level "shown" and "total". Nothing is cut inside a string,
// so the result stays valid JSON and valid UTF-8. A result that stays too large
// without its lists is replaced by a note.
func clipResult(result string, max int) string {
	if len(result) <= max {
		return result
	}
	note := func() string {
		out, _ := json.Marshal(map[string]any{"clipped": true, "bytes": len(result), "hint": "result too large; narrow the request"})
		return string(out)
	}
	decoder := json.NewDecoder(strings.NewReader(result))
	decoder.UseNumber()
	var root map[string]any
	if decoder.Decode(&root) != nil || root == nil {
		return note()
	}
	type list struct {
		holder map[string]any
		key    string
		items  []any
	}
	var lists []*list
	total := 0
	var walk func(map[string]any)
	walk = func(m map[string]any) {
		for k, v := range m {
			switch v := v.(type) {
			case map[string]any:
				walk(v)
			case []any:
				lists = append(lists, &list{m, k, v})
				total += len(v)
			}
		}
	}
	walk(root)
	shown := total
	render := func() string {
		for _, l := range lists {
			l.holder[l.key] = l.items
		}
		root["shown"], root["total"] = shown, total
		out, _ := json.Marshal(root)
		return string(out)
	}
	// ponytail: re-encodes after every dropped item; fine for lists of tens.
	for out := render(); ; out = render() {
		if len(out) <= max {
			return out
		}
		var longest *list
		for _, l := range lists {
			if len(l.items) > 0 && (longest == nil || len(l.items) > len(longest.items)) {
				longest = l
			}
		}
		if longest == nil {
			return note()
		}
		longest.items = longest.items[:len(longest.items)-1]
		shown--
	}
}
