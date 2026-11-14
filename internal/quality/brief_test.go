package quality

import (
	"context"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

func TestCriticSeesTheBriefAndWriterSeesItsEndings(t *testing.T) {
	r, err := DefaultRubric()
	if err != nil {
		t.Fatal(err)
	}
	var seen CriticInput
	judge := func(_ context.Context, in CriticInput) (CriticVerdict, error) {
		seen = in
		return CriticVerdict{Pass: true, Scores: CriticScores{Hook: 3, NaturalVietnamese: 3, Specificity: 3, ForcedSelfReference: 5, TargetFit: 3, Novelty: 3, Clarity: 4}}, nil
	}
	in := WriterInput{Kind: Post, Angle: "fan tung hoa", Fact: "bài đăng 25 phút tuổi có 153 likes", Draft: []string{"25 phút đã 153 likes"}}
	if _, ok, err := Revise(context.Background(), nil, judge, nil, in, 0); err != nil || !ok {
		t.Fatalf("ok %v err %v", ok, err)
	}
	msg := CriticMessages(r, config.PersonaConfig{Name: "N"}, seen)[1].Content
	if !strings.Contains(msg, in.Fact) || !strings.Contains(msg, in.Angle) {
		t.Errorf("critic input lacks the brief: %q", msg)
	}
	in.Endings = []string{":)", "😭"}
	if w := WriterMessages(r, config.PersonaConfig{Name: "N"}, in)[1].Content; !strings.Contains(w, "Your last endings: :) 😭; avoid repeating them.") {
		t.Errorf("writer prompt lacks the endings: %q", w)
	}
}
