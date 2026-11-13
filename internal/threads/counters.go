package threads

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// threadsCounters reads the Like, Reply and Repost buttons of one post block,
// looking through plain generic wrappers but not into nested post blocks. A
// button named only by its label has no number and counts 0; an absent button
// or a number that does not parse stays nil.
func threadsCounters(node *axNode) (likes, replies, reposts *int64) {
	var visit func(*axNode)
	visit = func(n *axNode) {
		for _, c := range n.children {
			switch c.role {
			case "button":
				label, count, ok := splitCounter(c.name)
				if !ok {
					continue
				}
				// The post's own counters precede any composer or reply controls.
				switch label {
				case "like":
					if likes == nil {
						likes = count
					}
				case "reply":
					if replies == nil {
						replies = count
					}
				case "repost":
					if reposts == nil {
						reposts = count
					}
				}
			case "generic":
				if !isThreadsPostBlock(c) {
					visit(c)
				}
			}
		}
	}
	visit(node)
	return likes, replies, reposts
}

// splitCounter splits a button name such as "Like 6.5K" or "Repost" into its
// lowercase label and count. ok is false for any other button. The count is nil
// when the label carries an unreadable number and 0 when it carries none.
func splitCounter(name string) (label string, count *int64, ok bool) {
	word, rest, _ := strings.Cut(strings.TrimSpace(name), " ")
	switch label = strings.ToLower(word); label {
	case "like", "reply", "repost":
	default:
		return "", nil, false
	}
	if rest = strings.TrimSpace(rest); rest == "" {
		zero := int64(0)
		return label, &zero, true
	}
	return label, parseCount(rest), true
}

var countText = regexp.MustCompile(`^(\d[\d.,]*)\s*([\p{L}]*)`)

// parseCount reads a Threads figure: "83", "1,234", "1.234", "6.5K", "1,2K",
// "1,2 N", "1.2M", "2K posts", "2 Tr bài viết". A suffix (K or N thousand, M or
// Tr million, B billion) makes the single separator a decimal point; without a
// suffix a separator must start groups of exactly three digits. Nil when the
// text is not a figure.
func parseCount(text string) *int64 {
	m := countText.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return nil
	}
	digits, scale := m[1], multiplier(strings.ToLower(m[2]))
	var value float64
	if scale > 0 {
		if strings.Count(digits, ".")+strings.Count(digits, ",") > 1 {
			return nil
		}
		f, err := strconv.ParseFloat(strings.Replace(digits, ",", ".", 1), 64)
		if err != nil {
			return nil
		}
		value = f * scale
	} else {
		groups := strings.FieldsFunc(digits, func(r rune) bool { return r == '.' || r == ',' })
		for i, g := range groups {
			if i > 0 && len(g) != 3 {
				return nil
			}
		}
		f, err := strconv.ParseFloat(strings.Join(groups, ""), 64)
		if err != nil {
			return nil
		}
		value = f
	}
	if value < 0 || value > snapshotMaxMetric {
		return nil
	}
	n := int64(math.Round(value))
	return &n
}

// multiplier maps a count suffix to its factor; 0 means no suffix (a following
// word such as "posts" is not one).
func multiplier(suffix string) float64 {
	switch suffix {
	case "k", "n":
		return 1e3
	case "m", "tr":
		return 1e6
	case "b":
		return 1e9
	}
	return 0
}

var ageText = regexp.MustCompile(`^(\d+)\s*([^\d\s]+)`)

// threadsAge reads a post's age in minutes from its timestamp link: the link
// name ("45 minutes ago"), then the short text under it ("45m"). Nil when none
// parses.
func threadsAge(link *axNode) *int64 {
	if link == nil {
		return nil
	}
	var age *int64
	link.walk(func(n *axNode) bool {
		if age == nil && (n == link || n.role == "Abbr" || n.role == "StaticText") {
			age = parseAge(n.name)
		}
		return age == nil
	})
	return age
}

// parseAge converts "15m", "2h", "3d", "1w", "45 minutes ago", "about an hour
// ago", "a day ago", "just now" and the Vietnamese "15 phút", "2 giờ", "3 ngày",
// "1 tuần" to minutes.
func parseAge(label string) *int64 {
	s := strings.ToLower(strings.TrimSpace(label))
	s = strings.TrimPrefix(s, "about ")
	s = strings.TrimPrefix(s, "khoảng ")
	s = strings.TrimSuffix(strings.TrimSuffix(s, " ago"), " trước")
	switch s {
	case "now", "just now", "vừa xong", "vừa mới":
		zero := int64(0)
		return &zero
	}
	for _, article := range []string{"a ", "an ", "một "} {
		if strings.HasPrefix(s, article) {
			s = "1 " + strings.TrimPrefix(s, article)
		}
	}
	m := ageText.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > snapshotMaxMetric {
		return nil
	}
	var unit int64
	switch strings.TrimSuffix(m[2], "s") {
	case "m", "min", "minute", "phút":
		unit = 1
	case "h", "hr", "hour", "giờ", "tiếng":
		unit = 60
	case "d", "day", "ngày":
		unit = 24 * 60
	case "w", "week", "tuần":
		unit = 7 * 24 * 60
	default:
		return nil
	}
	minutes := n * unit
	return &minutes
}
