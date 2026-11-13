package trends_test

import (
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

func TestObservationRetentionCoversTheBriefWindowPlusOneDay(t *testing.T) {
	age, ok := store.RetentionAge("threads_observations")
	if !ok || age != trends.BaselineWindow+24*time.Hour {
		t.Fatalf("threads_observations kept %v, brief reads %v", age, trends.BaselineWindow)
	}
}
