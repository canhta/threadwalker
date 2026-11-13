package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

const (
	// EvidencePerAgent is how many page snapshots one agent keeps.
	EvidencePerAgent = 20
	evidenceStamp    = "20060102T150405.000"
)

// maxEvidenceBytes bounds one saved snapshot.
const maxEvidenceBytes = 512 << 10

// redactedArgs is the audit's copy of a call's arguments: every secret
// replaced, every string cut to store.MaxArgField. Arguments that hold a
// secret keep their key names only.
func (d *Dispatcher) redactedArgs(raw string, sensitive bool) string {
	var value any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&value) != nil {
		cut, _ := stdx.TruncateUTF8(d.redactor.String(raw), store.MaxArgField)
		value = map[string]any{"unparsed": cut}
	} else if sensitive {
		keys := []string{}
		if m, ok := value.(map[string]any); ok {
			for k := range m {
				keys = append(keys, d.redactor.String(k))
			}
			sort.Strings(keys)
		}
		value = map[string]any{"keys": keys}
	}
	out, err := json.Marshal(d.capStrings(value))
	if err != nil {
		return "{}"
	}
	return string(out)
}

func (d *Dispatcher) capStrings(v any) any {
	switch v := v.(type) {
	case string:
		cut, _ := stdx.TruncateUTF8(d.redactor.String(v), store.MaxArgField)
		return cut
	case []any:
		for i := range v {
			v[i] = d.capStrings(v[i])
		}
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[d.redactor.String(k)] = d.capStrings(e)
		}
		return out
	}
	return v
}

// saveEvidence stores the page snapshot a failed browser step carries, redacted,
// under <EvidenceDir>/<agent>/, and logs its path. It returns the path, or ""
// when there is nothing to save or nowhere to save it. Saving is best effort.
func (d *Dispatcher) saveEvidence(ctx context.Context, tool string, se *threads.StageError) string {
	if d.opts.EvidenceDir == "" || se == nil || se.Snapshot == "" {
		return ""
	}
	dir := filepath.Join(d.opts.EvidenceDir, d.opts.Template.ID)
	now := d.opts.Now().UTC()
	name := fmt.Sprintf("%s-w%d-%s-%s.txt", now.Format(evidenceStamp), d.opts.WakeID, tool, se.Stage)
	body, _ := stdx.TruncateUTF8(d.redactor.String(se.Snapshot), maxEvidenceBytes)
	header := fmt.Sprintf("agent %s wake %d tool %s stage %s origin %s\n\n", d.opts.Template.ID, d.opts.WakeID, tool, se.Stage, d.redactor.String(se.Origin))
	path := filepath.Join(dir, name)
	if os.MkdirAll(dir, 0o700) != nil || os.WriteFile(path, []byte(header+body), 0o600) != nil {
		return ""
	}
	pruneEvidence(dir, now)
	logging.Emit(context.WithoutCancel(ctx), d.opts.Logger, slog.LevelInfo, logging.Record{Event: logging.BrowserEvidenceSaved, AgentID: d.opts.Template.ID, Phase: d.opts.Phase, WakeID: d.opts.WakeID, Tool: tool, Operation: "browser", Outcome: "success", Reason: se.Stage, Evidence: path})
	return path
}

// pruneEvidence keeps the newest EvidencePerAgent snapshots, none older than
// the transcripts' retention.
func pruneEvidence(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	age, _ := store.RetentionAge("wake_transcripts")
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for i, name := range names {
		at, err := time.Parse(evidenceStamp, name[:min(len(name), len(evidenceStamp))])
		if i < len(names)-EvidencePerAgent || (err == nil && now.Sub(at) > age) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
