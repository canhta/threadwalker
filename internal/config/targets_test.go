package config_test

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/config"
)

func TestTargetsKnobs(t *testing.T) {
	tmpl, err := loadTemplate(t, "")
	if err != nil || tmpl.Targets != config.DefaultTargets {
		t.Fatalf("defaults: %+v %v", tmpl.Targets, err)
	}
	if config.DefaultTargets.MinLikes != 0 {
		t.Fatalf("min_likes must default to off: %+v", config.DefaultTargets)
	}
	tmpl, err = loadTemplate(t, "targets:\n  max_age_minutes: 90\n  max_replies: 12\n  min_likes_per_hour: 2.5\n  min_likes: 30\n")
	if err != nil || tmpl.Targets != (config.TargetsConfig{MaxAgeMinutes: 90, MaxReplies: 12, MinLikesPerHour: 2.5, MinLikes: 30}) {
		t.Fatalf("set: %+v %v", tmpl.Targets, err)
	}
	for _, bad := range []string{"max_age_minutes: 0", "max_replies: -1", "min_likes_per_hour: 0", "max_age_minutes: soon", "min_likes: -1", "min_likes: 1000001"} {
		if _, err = loadTemplate(t, "targets:\n  "+bad+"\n"); err == nil || !strings.Contains(err.Error(), "targets.") {
			t.Errorf("%s accepted: %v", bad, err)
		}
	}
}

func TestViralTemplateTargets(t *testing.T) {
	tmpl, err := loadOne(viralTemplatePath(), viralEnv())
	if err != nil || tmpl.Targets != (config.TargetsConfig{MaxAgeMinutes: 180, MaxReplies: 20, MinLikesPerHour: 10, MinLikes: 50}) {
		t.Fatalf("%+v %v", tmpl.Targets, err)
	}
}
