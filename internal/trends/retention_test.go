package trends_test

import (
	"testing"

	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

func TestTrendStoryRetentionCoversTheHeatWindow(t *testing.T) {
	age, ok := store.RetentionAge("trend_stories")
	if !ok || age < trends.BaselineWindow {
		t.Fatalf("trend_stories kept %v, story heat reads %v", age, trends.BaselineWindow)
	}
}
