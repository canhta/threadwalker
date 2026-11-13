package agent

import (
	"testing"
	"time"
)

func TestSampledObservationsCarryReadCounters(t *testing.T) {
	out := `{"untrusted":true,"data":{"kind":"feed","posts":[` +
		`{"username":"a","text":"counted","url":"https://www.threads.com/@a/post/1","likes":12,"replies":3,"reposts":0,"age_minutes":20},` +
		`{"username":"b","text":"uncounted","url":"https://www.threads.com/@b/post/2"}]}}`
	got := observationsFromToolOutput(out, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	if len(got) != 2 || got[0].Likes == nil || *got[0].Likes != 12 || *got[0].Replies != 3 || got[0].Reposts == nil || *got[0].Reposts != 0 {
		t.Fatalf("counted: %+v", got)
	}
	if got[1].Likes != nil || got[1].Replies != nil || got[1].Reposts != nil {
		t.Fatalf("an unshown figure must stay nil: %+v", got[1])
	}
}
