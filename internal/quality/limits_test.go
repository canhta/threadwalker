package quality

import (
	"fmt"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

func TestLinesCountOnlyNonBlankLines(t *testing.T) {
	for text, want := range map[string]int{"": 0, "a": 1, "a\nb\nc": 3, "a\n\nb\n\nc": 3, "\n a \n\n": 1, "a\n  \nb": 2} {
		if got := Lines(text); got != want {
			t.Errorf("Lines(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestWriterInstructionsStateTheLengthLimits(t *testing.T) {
	r, err := DefaultRubric()
	if err != nil {
		t.Fatal(err)
	}
	p := config.PersonaConfig{Name: "N"}
	post := WriterMessages(r, p, WriterInput{Kind: Post})[0].Content
	if want := fmt.Sprintf("at most %d characters and %d lines", MaxPostRunes, MaxPostLines); !strings.Contains(post, want) {
		t.Errorf("post instructions lack %q", want)
	}
	if want := fmt.Sprintf("first line to at most %d characters", MaxHookRunes); !strings.Contains(post, want) {
		t.Errorf("post instructions lack %q", want)
	}
	if reply := WriterMessages(r, p, WriterInput{Kind: Reply, ReplyMaxRunes: 80})[0].Content; !strings.Contains(reply, "at most 80 characters;") || strings.Contains(reply, "lines") {
		t.Errorf("reply instructions: %q", reply)
	}
	if reply := WriterMessages(r, p, WriterInput{Kind: Reply})[0].Content; !strings.Contains(reply, "at most 500 characters;") {
		t.Errorf("reply without a template limit: %q", reply)
	}
}
