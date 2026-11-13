// Package knowledge holds the harness-owned, read-only craft knowledge base:
// sourced claims about Threads, marketing and creator craft that the agent
// turns into hypotheses. The curated file is embedded in the binary; nothing
// at run time writes to it.
package knowledge

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
)

// Evidence levels, strongest first.
const (
	LevelPlatform     = "platform"
	LevelStudy        = "study"
	LevelPractitioner = "practitioner"
)

var (
	levels = []string{LevelPlatform, LevelStudy, LevelPractitioner}
	scopes = []string{"threads", "cross-platform", "vietnam"}
	// penalties are the Meta demotions an entry can collide with.
	penalties = []string{"engagement_bait", "rage_bait", "unoriginal_content", "political_recommendations"}
	datePat   = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2})?)?$`)
)

// Entry is one sourced claim. SourceDate is the publication date (YYYY,
// YYYY-MM or YYYY-MM-DD), or the date the source was read when the page shows
// none.
type Entry struct {
	ID          string `json:"id"`
	Claim       string `json:"claim"`
	Evidence    string `json:"evidence"`
	SourceURL   string `json:"source_url"`
	SourceDate  string `json:"source_date"`
	Scope       string `json:"scope"`
	Hypothesis  string `json:"hypothesis"`
	PenaltyRisk string `json:"penalty_risk,omitempty"`
}

//go:embed threads.json
var curated []byte

var catalog, catalogErr = parse(curated)

func parse(raw []byte) ([]Entry, error) {
	var file struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parsing knowledge file: %w", err)
	}
	seen := map[string]bool{}
	for _, e := range file.Entries {
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("knowledge entry %q: %w", e.ID, err)
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("knowledge entry %q: duplicate id", e.ID)
		}
		seen[e.ID] = true
	}
	return file.Entries, nil
}

func (e Entry) validate() error {
	u, err := url.Parse(e.SourceURL)
	switch {
	case e.ID == "" || e.Claim == "" || e.Hypothesis == "":
		return fmt.Errorf("id, claim and hypothesis are required")
	case err != nil || u.Scheme != "https" || u.Host == "":
		return fmt.Errorf("source_url must be an https URL")
	case !datePat.MatchString(e.SourceDate):
		return fmt.Errorf("source_date must be YYYY, YYYY-MM or YYYY-MM-DD")
	case !slices.Contains(levels, e.Evidence):
		return fmt.Errorf("unknown evidence level %q", e.Evidence)
	case !slices.Contains(scopes, e.Scope):
		return fmt.Errorf("unknown scope %q", e.Scope)
	case e.PenaltyRisk != "" && !slices.Contains(penalties, e.PenaltyRisk):
		return fmt.Errorf("unknown penalty_risk %q", e.PenaltyRisk)
	}
	return nil
}

// All returns every curated entry in file order.
func All() ([]Entry, error) {
	return slices.Clone(catalog), catalogErr
}

// Get returns the entry with the given id.
func Get(id string) (Entry, bool, error) {
	if catalogErr != nil {
		return Entry{}, false, catalogErr
	}
	i := slices.IndexFunc(catalog, func(e Entry) bool { return e.ID == id })
	if i < 0 {
		return Entry{}, false, nil
	}
	return catalog[i], true, nil
}

// Filter returns the entries matching a scope and an evidence level; an empty
// filter matches everything. An unknown filter value is an error, so a typo is
// never read as "no knowledge".
func Filter(scope, evidence string) ([]Entry, error) {
	if catalogErr != nil {
		return nil, catalogErr
	}
	if scope != "" && !slices.Contains(scopes, scope) {
		return nil, fmt.Errorf("unknown scope %q (one of %v)", scope, scopes)
	}
	if evidence != "" && !slices.Contains(levels, evidence) {
		return nil, fmt.Errorf("unknown evidence level %q (one of %v)", evidence, levels)
	}
	out := []Entry{}
	for _, e := range catalog {
		if (scope == "" || e.Scope == scope) && (evidence == "" || e.Evidence == evidence) {
			out = append(out, e)
		}
	}
	return out, nil
}
