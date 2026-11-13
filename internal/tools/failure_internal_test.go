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
