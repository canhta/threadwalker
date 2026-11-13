package eval_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/eval"
)

const replayCase = `{"cases":[{"id":"r","kind":"replay","replay":%s,"expect":{"end_reason":"completed"}}]}`

func loadReplay(t *testing.T, replay string) (*eval.Suite, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(path, []byte(strings.Replace(replayCase, "%s", replay, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	return eval.LoadSuite(path)
}

func TestReplaySuiteRejectsMalformedCases(t *testing.T) {
	call := `{"type":"function","function":{"name":"fetch_trends","arguments":"{}"}}`
	for name, replay := range map[string]string{
		"no steps":              `{"steps":[]}`,
		"writer without critic": `{"writer":[["x"]],"steps":[{"call":` + call + `}]}`,
		"stage without a fault": `{"live":true,"steps":[{"call":` + call + `,"expected_stage":"submit"}]}`,
		"fault without live":    `{"steps":[{"call":` + call + `,"fail_write":{"reason":"write_refused","stage":"submit"},"expected_stage":"submit"}]}`,
		"unknown fault":         `{"live":true,"steps":[{"call":` + call + `,"fail_write":{"reason":"nope","stage":"submit"},"expected_stage":"submit"}]}`,
	} {
		if _, err := loadReplay(t, replay); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := loadReplay(t, `{"steps":[{"call":`+call+`}]}`); err != nil {
		t.Fatalf("valid case refused: %v", err)
	}
}

func TestReplayFailsWhenTheGuardVerdictDiffers(t *testing.T) {
	call := `{"type":"function","function":{"name":"post","arguments":"{\"text\":\"hi\",\"hook\":\"trend_detail\",\"rationale\":\"r\",\"trend_ref\":\"r9\"}"}}`
	for expected, want := range map[string]string{`"expected_blocked":"no_trend_anchor"`: "", `"expected_blocked":"text_length"`: "replay_outcome_mismatch", `"expected_detail":"nowhere"`: "replay_outcome_mismatch"} {
		suite, err := loadReplay(t, `{"steps":[{"call":`+call+`,`+expected+`}]}`)
		if err != nil {
			t.Fatal(err)
		}
		suite.Cases[0].Expect.Tools = []string{"post"}
		report := runFixture(t, suite, eval.Options{Seed: 1})
		for _, r := range report.Results {
			if (want == "") != r.Passed || (want != "" && r.Reason != want && r.Reason != "replay_detail_mismatch") {
				t.Errorf("%s: passed=%v reason=%s", expected, r.Passed, r.Reason)
			}
		}
	}
}
