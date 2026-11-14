package tools

import (
	"encoding/json"
	"slices"
	"strings"
)

// clipResult keeps a tool result within max bytes by dropping whole items from
// the end of its lists, the longest list first (ties by path), and says how
// many of how many each list kept in "clipped_lists", keyed by the list's path
// such as "data.posts". Nothing is cut inside a string, so the result stays
// valid JSON and valid UTF-8. A result that stays too large without its lists
// is replaced by a note.
//
// Refs of dropped items stay in the guard's registry, where the executor noted
// them before the clip; the dispatcher records which refs the model was shown
// and offers only those as open targets.
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
		path   string
		items  []any
		total  int
	}
	var lists []*list
	var walk func(map[string]any, string)
	walk = func(m map[string]any, prefix string) {
		for k, v := range m {
			switch v := v.(type) {
			case map[string]any:
				walk(v, prefix+k+".")
			case []any:
				lists = append(lists, &list{m, k, prefix + k, v, len(v)})
			}
		}
	}
	walk(root, "")
	slices.SortFunc(lists, func(a, b *list) int { return strings.Compare(a.path, b.path) })
	render := func() string {
		shown, total := map[string]int{}, map[string]int{}
		for _, l := range lists {
			l.holder[l.key] = l.items
			shown[l.path], total[l.path] = len(l.items), l.total
		}
		root["clipped_lists"] = map[string]any{"shown": shown, "total": total}
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
	}
}
