package eval

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/trends"
)

// evalTrends and evalStudy satisfy dispatcher construction in scenarios that
// never request outside content.
func evalTrends(context.Context) ([]trends.Trend, error) { return []trends.Trend{}, nil }

func evalStudy(context.Context) (trends.StudyFeed, error) { return trends.StudyFeed{}, nil }

// caseRun is the setup every component runner shares: a temp directory, a
// captured logger, the store and the result being filled in. The runner owns a
// named CaseResult and lends it to beginCase; end stamps its duration.
type caseRun struct {
	Dir     string
	Store   *store.Store
	Logs    bytes.Buffer
	Logger  *slog.Logger
	r       *CaseResult
	started time.Time
}

// beginCase starts r as a failed result with the given end and reason, which
// the runner overwrites as it succeeds. Logs also go to capture when set.
func beginCase(r *CaseResult, c Case, p Profile, repeat int, name, endReason, reason string, capture io.Writer) (*caseRun, error) {
	*r = CaseResult{CaseID: c.ID, ProfileID: p.ID, ProviderID: p.ProviderID, Model: p.Config.Model, Protocol: p.Config.Protocol, Repeat: repeat, EndReason: endReason, Reason: reason, ToolCalls: []string{}}
	dir, err := os.MkdirTemp("", "threadz-"+name+"-eval-*")
	if err != nil {
		return nil, err
	}
	run := &caseRun{Dir: dir, r: r, started: time.Now()}
	w := io.Writer(&run.Logs)
	if capture != nil {
		w = io.MultiWriter(w, capture)
	}
	run.Logger, _ = logging.New(w, "debug", "json")
	return run, nil
}

// Open opens (or reopens, after closing) the case database file.
func (run *caseRun) Open(ctx context.Context, file string) (err error) {
	if run.Store != nil {
		if err = run.Store.Close(); err != nil {
			run.Store = nil
			return err
		}
		run.Store = nil
	}
	run.Store, err = store.Open(ctx, filepath.Join(run.Dir, file))
	return err
}

// Path is a file path inside the case directory.
func (run *caseRun) Path(file string) string { return filepath.Join(run.Dir, file) }

// fail marks the result failed with reason.
func (run *caseRun) fail(reason string) CaseResult {
	run.r.Passed = false
	run.r.Reason = reason
	return *run.r
}

// end releases the store and directory and stamps the duration.
func (run *caseRun) end() {
	if run.Store != nil {
		run.Store.Close()
	}
	os.RemoveAll(run.Dir)
	run.r.DurationMS = float64(time.Since(run.started).Nanoseconds()) / 1e6
}

// loadToolCalls fills r.ToolCalls from the audit, for one agent and wake or,
// with zero values, for everything.
func (run *caseRun) loadToolCalls(ctx context.Context, agentID string, wakeID int64) error {
	names, err := run.Store.ToolCallNames(ctx, agentID, wakeID)
	run.r.ToolCalls = append(run.r.ToolCalls, names...)
	return err
}
