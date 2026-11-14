package tools_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/stdx"
)

// The post and reply tools tell the model the limits code enforces.
func TestPostAndReplyDescriptionsStateTheirLimits(t *testing.T) {
	for name, want := range map[string]string{
		"post":  fmt.Sprintf("at most %d characters and %d lines, the first line at most %d", quality.MaxPostRunes, quality.MaxPostLines, quality.MaxHookRunes),
		"reply": fmt.Sprintf("at most %d characters", stdx.MaxThreadsPostRunes),
	} {
		spec, _ := guard.Spec(name)
		if !strings.Contains(spec.Description, want) {
			t.Errorf("%s description %q lacks %q", name, spec.Description, want)
		}
	}
}
