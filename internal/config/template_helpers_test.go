package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withBlock returns the fixture template with block placed before limits.
func withBlock(t *testing.T, block string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture.agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(string(raw), "\nlimits:", "\n"+block+"limits:", 1)
}
