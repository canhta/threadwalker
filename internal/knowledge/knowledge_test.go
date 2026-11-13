package knowledge

import (
	"strings"
	"testing"
)

func TestCuratedFileIsValid(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no entries")
	}
	for _, e := range all {
		if e.SourceURL == "" || e.SourceDate == "" || e.Evidence == "" {
			t.Errorf("%s lacks url, date or level", e.ID)
		}
	}
}

func TestParseRejectsBadEntries(t *testing.T) {
	ok := `{"id":"a","claim":"c","evidence":"study","source_url":"https://x.test/p","source_date":"2026-01","scope":"threads","hypothesis":"h"}`
	if _, err := parse([]byte(`{"entries":[` + ok + `]}`)); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	for name, bad := range map[string]string{
		"no url":      strings.Replace(ok, `"https://x.test/p"`, `""`, 1),
		"http url":    strings.Replace(ok, "https://", "http://", 1),
		"no date":     strings.Replace(ok, `"2026-01"`, `""`, 1),
		"bad date":    strings.Replace(ok, `"2026-01"`, `"Jan 2026"`, 1),
		"bad level":   strings.Replace(ok, `"study"`, `"rumour"`, 1),
		"bad scope":   strings.Replace(ok, `"threads"`, `"mars"`, 1),
		"no claim":    strings.Replace(ok, `"claim":"c"`, `"claim":""`, 1),
		"bad penalty": strings.Replace(ok, `"hypothesis":"h"`, `"hypothesis":"h","penalty_risk":"spam"`, 1),
		"duplicate":   ok + "," + ok,
		"not json":    "",
	} {
		if _, err := parse([]byte(`{"entries":[` + bad + `]}`)); err == nil && name != "not json" {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := parse([]byte("")); err == nil {
		t.Error("empty file accepted")
	}
}

func TestFilterAndGet(t *testing.T) {
	all, _ := All()
	got, err := Filter("", "")
	if err != nil || len(got) != len(all) {
		t.Fatalf("empty filter: %d, %v", len(got), err)
	}
	got, err = Filter("vietnam", LevelPractitioner)
	if err != nil || len(got) == 0 {
		t.Fatalf("vietnam practitioner: %d, %v", len(got), err)
	}
	for _, e := range got {
		if e.Scope != "vietnam" || e.Evidence != LevelPractitioner {
			t.Errorf("%s leaked through the filter", e.ID)
		}
	}
	if _, err = Filter("mars", ""); err == nil {
		t.Error("unknown scope accepted")
	}
	if _, err = Filter("", "rumour"); err == nil {
		t.Error("unknown level accepted")
	}
	if e, ok, err := Get(all[0].ID); err != nil || !ok || e.ID != all[0].ID {
		t.Error("Get missed a real id")
	}
	if _, ok, _ := Get("nope"); ok {
		t.Error("Get found a missing id")
	}
}
