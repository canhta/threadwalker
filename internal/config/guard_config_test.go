package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
)

func TestGuardStopPhraseConfiguration(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "agents", "solo-builder.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, extra string
		wantError   bool
	}{{"omitted", "", false}, {"custom", "\nguard:\n  stop_phrases:\n    - please stop\n    - đừng trả lời\n", false}, {"empty", "\nguard:\n  stop_phrases:\n", true}, {"scalar", "\nguard:\n  stop_phrases: stop\n", true}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(path, append(data, []byte(tc.extra)...), 0600); err != nil {
				t.Fatal(err)
			}
			tmpl, err := config.LoadFile(path, validEnv())
			if (err != nil) != tc.wantError {
				t.Fatalf("configuration err %v", err)
			}
			if tc.name == "omitted" && tmpl.Guard.StopPhrases != nil {
				t.Fatal("default must remain omitted")
			}
			if tc.name == "custom" && strings.Join(tmpl.Guard.StopPhrases, "|") != "please stop|đừng trả lời" {
				t.Fatalf("custom phrases %+v", tmpl.Guard.StopPhrases)
			}
		})
	}
}
