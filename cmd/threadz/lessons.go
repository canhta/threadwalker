package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/canhta/threadz/internal/admin"
	"github.com/canhta/threadz/internal/store"
)

// lessonRow is the --json shape of `ctl lessons`.
type lessonRow struct {
	ID            int64     `json:"id"`
	Kind          string    `json:"kind"`
	Polarity      string    `json:"polarity"`
	Text          string    `json:"text"`
	CreatedAt     time.Time `json:"created_at"`
	ConfirmedAt   time.Time `json:"confirmed_at"`
	RetiredAt     time.Time `json:"retired_at,omitzero"`
	RetiredBy     string    `json:"retired_by,omitempty"`
	RetireReason  string    `json:"retire_reason,omitempty"`
	Evidence      []int64   `json:"evidence"`
	AppliedN      int       `json:"applied_n"`
	AppliedRanked int       `json:"applied_ranked"`
	AppliedMedian float64   `json:"applied_median_rank,omitempty"`
	OthersN       int       `json:"others_n"`
	OthersMedian  float64   `json:"others_median_rank,omitempty"`
}

type lessonOutcome struct {
	Agent  string `json:"agent"`
	Lesson int64  `json:"lesson"`
}

// ctlLessons lists the lessons from the database, or forgets one through the
// running service, which is the only writer.
func ctlLessons(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "forget" {
		return ctlForgetLesson(ctx, getenv, args[1:], stdout, stderr)
	}
	f := newCtlFlags("lessons", getenv)
	if rest, ok := f.parse(args, true, stderr); !ok || len(rest) != 0 {
		return ctlUsageError(stderr, "lessons takes --agent ID, or the subcommand forget <id>")
	}
	st, ok := openReadOnly(ctx, *f.db, stderr)
	if !ok {
		return exitFailure
	}
	defer st.Close()
	all, err := st.Lessons(ctx, *f.agent, true)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	uses, err := st.LessonUses(ctx, *f.agent, all)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	rows := make([]lessonRow, 0, len(all))
	for _, l := range all {
		u := uses[l.ID]
		rows = append(rows, lessonRow{ID: l.ID, Kind: l.Kind, Polarity: l.Polarity, Text: l.Text, CreatedAt: l.CreatedAt, ConfirmedAt: l.ConfirmedAt, RetiredAt: l.RetiredAt, RetiredBy: l.RetiredBy, RetireReason: l.RetireReason,
			Evidence: l.Evidence, AppliedN: u.AppliedN, AppliedRanked: u.AppliedRanked, AppliedMedian: u.AppliedMedian, OthersN: u.OthersN, OthersMedian: u.OthersMedian})
	}
	return printResult(stdout, *f.json, rows, func(w io.Writer, all []lessonRow) {
		for _, l := range all {
			state := "active"
			if !l.RetiredAt.IsZero() {
				state = "retired by " + l.RetiredBy
			}
			fmt.Fprintf(w, "#%d  %s %s  %s\n  %s\n  applied %d", l.ID, l.Kind, l.Polarity, state, l.Text, l.AppliedN)
			if l.AppliedRanked > 0 {
				fmt.Fprintf(w, ", median rank %.2f of %d ranked", l.AppliedMedian, l.AppliedRanked)
			}
			if l.OthersN > 0 {
				fmt.Fprintf(w, "; others %.2f of %d", l.OthersMedian, l.OthersN)
			}
			fmt.Fprintln(w)
		}
	})
}

func ctlForgetLesson(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags("lessons forget", getenv)
	if len(args) == 0 {
		return ctlUsageError(stderr, "lessons forget takes a lesson id")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id < 1 {
		return ctlUsageError(stderr, "lessons forget takes a numeric lesson id")
	}
	if _, ok := f.parse(args[1:], true, stderr); !ok {
		return exitUsage
	}
	return ctlCall(ctx, f, admin.Request{Command: admin.ForgetLesson, Agent: *f.agent, Lesson: id}, stdout, stderr, func(w io.Writer, o lessonOutcome) {
		fmt.Fprintf(w, "%s: lesson #%d forgotten; the agent will not add it again\n", o.Agent, o.Lesson)
	})
}

// forgetLesson is the admin command behind `ctl lessons forget`.
func (a *adminService) forgetLesson(ctx context.Context, agentID string, id int64) (any, error) {
	err := a.store.ForgetLesson(ctx, agentID, id, a.deps.now())
	var refusal *store.LessonRefusal
	if errors.As(err, &refusal) {
		return nil, fmt.Errorf("there is no active lesson #%d", id)
	}
	if err != nil {
		return nil, err
	}
	return lessonOutcome{Agent: agentID, Lesson: id}, nil
}
