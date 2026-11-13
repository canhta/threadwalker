package config_test

import (
	"os"
	"path/filepath"

	"github.com/canhta/threadz/internal/config"
)

// loadOne loads a single template file through the one public entry
// point by placing it alone in a directory.
func loadOne(path string, getenv func(string) string) (config.Template, error) {
	dir, err := os.MkdirTemp("", "tw-load-one")
	if err != nil {
		return config.Template{}, err
	}
	defer os.RemoveAll(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		return config.Template{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "only.yaml"), data, 0o600); err != nil {
		return config.Template{}, err
	}
	tmpls, err := config.Load(dir, config.Options{Getenv: getenv})
	if err != nil {
		return config.Template{}, err
	}
	return tmpls[0], nil
}
