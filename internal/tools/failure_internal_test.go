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
	fixable := modelFacing(fmt.Errorf("writing strategy: %w", store.ErrStrategyInput))
	if !errors.As(fixable, &blocked) || blocked.Reason != "strategy_input" || guard.Hint(blocked.Reason, "") == "" || failureReason(fixable) != "strategy_input" {
		t.Fatalf("fixable: %v", fixable)
	}
	if !errors.Is(modelFacing(fmt.Errorf("adding memory entry: %w", errLessonCap)), errLessonCap) {
		t.Fatal("the cause was lost")
	}
	other := modelFacing(errors.New("disk full"))
	if errors.As(other, &blocked) || failureReason(other) != "operation_failed" {
		t.Fatalf("other: %v", other)
	}
}

// The dream's second try on 2026-11-14 dropped every metric but was still
// refused for its date and ids; those are names, a counter claim is not.
func TestLessonNumbersIgnoreDatesIdsAndRefs(t *testing.T) {
	ev := store.ActionEvidence{Counters: map[int64]bool{3: true, 273: true}, URLs: map[string]bool{}}
	if err := checkLesson("2026-11-14 wake: action 43 reply r69/r68 chưa gán lift; chi tiết ở note:43.", ev); err != nil {
		t.Fatalf("names refused: %v", err)
	}
	if err := checkLesson("action 43 có 273 view, 3 like", ev); err != nil {
		t.Fatalf("measured counters refused: %v", err)
	}
	if err := checkLesson("action 43 có 22,959 view", ev); !errors.Is(err, errLessonNumber) {
		t.Fatalf("an unmeasured number passed: %v", err)
	}
}
