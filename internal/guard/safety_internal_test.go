package guard

import "testing"

func TestContentBlockDetailIsTheOriginalText(t *testing.T) {
	reason, detail := contentBlock(nil, "TIM dI mn")
	if reason != reasonBait || detail != "TIM dI mn" {
		t.Fatalf("got %q %q, want the match as written", reason, detail)
	}
}
