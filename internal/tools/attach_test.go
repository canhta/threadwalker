package tools

import (
	"context"
	"testing"
)

func TestAttachRefusesToolMissingFromGuardTable(t *testing.T) {
	d := &Dispatcher{registry: map[string]executor{}}
	run := func(context.Context, arguments) (any, error) { return nil, nil }
	if err := d.attach(map[string]executor{"not_in_table": run}); err == nil || len(d.registry) != 0 {
		t.Fatalf("err=%v registry=%v", err, d.registry)
	}
	if err := d.attach(map[string]executor{"note_read": run}); err != nil || d.registry["note_read"] == nil {
		t.Fatalf("listed tool refused: %v", err)
	}
}
