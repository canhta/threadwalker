package agent

import (
	"regexp"
	"strings"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

// memoryIndexBytes caps the memory index a wake shows the model.
const memoryIndexBytes = 1536

var datedTopic = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

// capMemoryIndex keeps the newest whole entries inside memoryIndexBytes,
// newest first. The input is oldest first.
func capMemoryIndex(index []store.MemoryEntry) []store.MemoryEntry {
	out, used := []store.MemoryEntry{}, 0
	for i := len(index) - 1; i >= 0; i-- {
		used += len(index[i].Text)
		if used > memoryIndexBytes {
			break
		}
		out = append(out, index[i])
	}
	return out
}

// undatedTopics drops topics named after a date; they are one-off logs.
func undatedTopics(topics []store.TopicSummary) []store.TopicSummary {
	out := topics[:0:0]
	for _, t := range topics {
		if !datedTopic.MatchString(t.Topic) {
			out = append(out, t)
		}
	}
	return out
}

// dietStories drops stories on the template's avoid topics and the Google News
// redirect URLs, which are opaque to the model.
func dietStories(stories []store.TrendStory, t config.Template) []store.TrendStory {
	_, avoid := t.TrendKeywords()
	out := stories[:0:0]
	for _, s := range stories {
		if trends.Avoided(s.Title, avoid) {
			continue
		}
		if strings.Contains(s.URL, "news.google.com/") {
			s.URL = ""
		}
		out = append(out, s)
	}
	return out
}
