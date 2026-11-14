package tools

import (
	"errors"
	"fmt"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

// A failure the model can fix reaches it as a blocked hint; any other keeps
// the generic error, and both log the same code.
func TestModelFacingFailures(t *testing.T) {
	var blocked *guard.BlockedError
	fixable := modelFacing(fmt.Errorf("checking lesson: %w", errLessonSecret))
	if !errors.As(fixable, &blocked) || blocked.Reason != "lesson_secret" || guard.Hint(blocked.Reason, "") == "" || failureReason(fixable) != "lesson_secret" {
		t.Fatalf("fixable: %v", fixable)
	}
	if !errors.Is(modelFacing(fmt.Errorf("checking lesson: %w", errStaleRef)), errStaleRef) {
		t.Fatal("the cause was lost")
	}
	other := modelFacing(errors.New("disk full"))
	if errors.As(other, &blocked) || failureReason(other) != "operation_failed" {
		t.Fatalf("other: %v", other)
	}
}

// The dream's second try on 2026-11-14 dropped every metric but was still
// refused for its date and ids; those are names, a counter claim is not.
func TestLessonNumbersIgnoreDatesAndIds(t *testing.T) {
	ev := store.ActionEvidence{Counters: map[int64]bool{3: true, 273: true}, URLs: map[string]bool{}}
	if err := checkLesson("2026-11-14 wake: action 43 reply chưa gán lift; chi tiết ở note:43.", ev); err != nil {
		t.Fatalf("names refused: %v", err)
	}
	if err := checkLesson("action 43 có 273 view, 3 like", ev); err != nil {
		t.Fatalf("measured counters refused: %v", err)
	}
	if err := checkLesson("action 43 có 22,959 view", ev); !errors.Is(err, errLessonNumber) {
		t.Fatalf("an unmeasured number passed: %v", err)
	}
}

func TestCheckLessonGate(t *testing.T) {
	ev := store.ActionEvidence{Counters: map[int64]bool{7: true, 120: true}, URLs: map[string]bool{"https://threads.example/p/1": true}}
	for _, tc := range []struct {
		name, text string
		want       error
	}{
		{"measured counters", "Post 120 views, 7 likes: short roasts work", nil},
		{"no numbers", "Short roasts work", nil},
		{"own url", "see https://threads.example/p/1 for the shape", nil},
		{"unmeasured number", "Post got 121 views", errLessonNumber},
		{"decimal", "Post got 7.5 likes", errLessonNumber},
		{"foreign url", "see https://threads.example/p/2", errLessonURL},
		{"secret key", "key sk-abcdefghijklmnopqrstuvwx", errLessonSecret},
		{"bearer token", "use Bearer abcdefghijklmnopqrstuvwx", errLessonSecret},
		{"per-wake ref", "post r58 got the reaction", errStaleRef},
	} {
		if err := checkLesson(tc.text, ev); !errors.Is(err, tc.want) && (err != nil || tc.want != nil) {
			t.Errorf("%s: err %v, want %v", tc.name, err, tc.want)
		}
	}
}
