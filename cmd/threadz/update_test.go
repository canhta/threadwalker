package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestScheduledAllowed(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 13, h, 0, 0, 0, time.Local) }
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		name    string
		env     map[string]string
		hour    int
		want    bool
		wantErr bool
	}{
		{"off by default", nil, 3, false, false},
		{"on without window", map[string]string{"THREADZ_UPDATE_AUTO": "true"}, 15, true, false},
		{"inside window", map[string]string{"THREADZ_UPDATE_AUTO": "true", "THREADZ_UPDATE_WINDOW": "2-5"}, 3, true, false},
		{"outside window", map[string]string{"THREADZ_UPDATE_AUTO": "true", "THREADZ_UPDATE_WINDOW": "2-5"}, 12, false, false},
		{"bad flag", map[string]string{"THREADZ_UPDATE_AUTO": "maybe"}, 3, false, true},
		{"bad window", map[string]string{"THREADZ_UPDATE_AUTO": "true", "THREADZ_UPDATE_WINDOW": "x"}, 3, false, true},
	} {
		got, err := scheduledAllowed(env(c.env), at(c.hour))
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: got %v, %v", c.name, got, err)
		}
	}
}

func TestUpdateUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"--channel", "beta"}, {"extra"}, {"--nope"}} {
		var out, errb bytes.Buffer
		if code := runUpdate(context.Background(), args, func(string) string { return "" }, &out, &errb); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}
