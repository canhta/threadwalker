package stdx

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestSleepStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := Sleep(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}

func TestFiniteNonNegative(t *testing.T) {
	for n, want := range map[float64]bool{0: true, 1.5: true, -1: false, math.NaN(): false, math.Inf(1): false, math.Inf(-1): false} {
		if got := FiniteNonNegative(n); got != want {
			t.Errorf("FiniteNonNegative(%v) = %v", n, got)
		}
	}
}

func TestThreadsHostAndCapRunes(t *testing.T) {
	for host, want := range map[string]string{"threads.com": "threads.com", "WWW.Threads.NET": "threads.com", "threads.net": "threads.com", "example.com": "", "threads.com.evil.io": ""} {
		if got, ok := ThreadsHost(host); got != want || ok != (want != "") {
			t.Errorf("%s: got %q, %v", host, got, ok)
		}
	}
	if got := CapRunes("đủ ba chữ", 4); got != "đủ b" || CapRunes("ngắn", 10) != "ngắn" {
		t.Errorf("CapRunes cut at %q", got)
	}
}
