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
	if err := d.attach(map[string]executor{"fetch_trends": run}); err != nil || d.registry["fetch_trends"] == nil {
		t.Fatalf("listed tool refused: %v", err)
	}
}
