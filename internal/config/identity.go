package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/canhta/threadz/internal/store"
)

func normalizeOwnUsername(raw string) (string, error) { return store.NormalizeUsername(raw) }

// ValidateTemplates checks the complete operator-owned set before any wake or
// state initialization.
func ValidateTemplates(templates []Template) error {
	if len(templates) == 0 {
		return errors.New("validating agents: no templates")
	}
	ids, users := map[string]bool{}, map[string]bool{}
	var profiles []string
	for _, t := range templates {
		if strings.TrimSpace(t.ID) == "" || ids[t.ID] {
			return errors.New("validating agents: distinct nonempty agent IDs required")
		}
		ids[t.ID] = true
		user, err := normalizeOwnUsername(t.Threads.Username)
		if err != nil || users[user] {
			return errors.New("validating agents: distinct valid threads.username identities required")
		}
		users[user] = true
		if len(templates) == 1 {
			continue
		}
		if strings.TrimSpace(t.Threads.ProfileDir) == "" {
			return errors.New("validating agents: distinct threads.profile_dir paths required")
		}
		profile, err := canonicalProfile(t.Threads.ProfileDir)
		if err != nil {
			return fmt.Errorf("validating agents: canonicalizing profile: %w", err)
		}
		for _, other := range profiles {
			if pathContains(profile, other) || pathContains(other, profile) {
				return errors.New("validating agents: browser profiles must not alias or overlap")
			}
		}
		profiles = append(profiles, profile)
	}
	return nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// Resolve existing ancestors too: new profile children under a symlink must
// not evade the overlap check merely because Chromium has not created them.
func canonicalProfile(raw string) (string, error) {
	path, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	var tail []string
	for {
		resolved, e := filepath.EvalSymlinks(path)
		if e == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(e) {
			return "", errors.New("profile path cannot be resolved")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", errors.New("profile ancestor unavailable")
		}
		tail = append(tail, filepath.Base(path))
		path = parent
	}
}
