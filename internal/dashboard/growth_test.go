package dashboard

import (
	"fmt"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func sampleAt(id int64, posted time.Time, at time.Time, v, l, r, rp int64) metricSample {
	return metricSample{ActionID: id, PostedAt: posted, MeasuredAt: at, Views: v, Likes: l, Replies: r, Reposts: rp}
}

func TestEarnedBetweenUsesTheLatestSampleAtEachEnd(t *testing.T) {
	posted := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	samples := []metricSample{
		sampleAt(1, posted, time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC), 100, 10, 2, 1),
		sampleAt(1, posted, time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC), 250, 25, 5, 2),
		sampleAt(1, posted, time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), 400, 40, 8, 3),
	}
	current := earnedBetween(samples, time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC))
	prior := earnedBetween(samples, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
	if current != (counts{300, 30, 6, 2}) {
		t.Fatalf("current = %+v", current)
	}
	if prior != (counts{100, 10, 2, 1}) {
		t.Fatalf("prior = %+v", prior)
	}
	if text, ok := countDelta(current.Views, prior.Views, "7 days"); !ok || text != "up 200% on the previous 7 days" {
		t.Fatalf("delta = %q %v", text, ok)
	}
	if text, ok := countDelta(10, 0, "7 days"); ok || text != "no earlier period to compare" {
		t.Fatalf("no-baseline delta = %q %v", text, ok)
	}
}

func TestEngagementRateNeedsViews(t *testing.T) {
	if _, ok := engagementRate(counts{Likes: 5, Replies: 1}); ok {
		t.Fatal("rate with zero views should be unavailable")
	}
	if rate, ok := engagementRate(counts{Views: 200, Likes: 10, Replies: 2, Reposts: 1}); !ok || rate != 0.065 {
		t.Fatalf("rate = %v %v", rate, ok)
	}
	if text, ok := rateDelta(0.065, 0.05, true, true, "7 days"); !ok || text != "up 1.5 points on the previous 7 days" {
		t.Fatalf("rate delta = %q %v", text, ok)
	}
	if text, ok := rateDelta(0.065, 0, false, false, "7 days"); ok || text != "no earlier period to compare" {
		t.Fatalf("no-baseline rate delta = %q %v", text, ok)
	}
}

func TestDailySeriesIsBoundedAndBreaksOnMissingDenominator(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, days := range []int{7, 14, 30} {
		starts := dayStarts(config.LimitsConfig{}, now, days)
		if len(starts) != days {
			t.Fatalf("bucket count = %d", len(starts))
		}
		chart := lineChart("views", "Views", starts, func(day time.Time) ([]*float64, []string) {
			v := float64(0)
			return []*float64{&v}, []string{"0"}
		}, func(v float64) string { return formatValue(v) }, time.UTC, 4)
		if len(chart.Rows) != days || len(chart.Rows) > 30 {
			t.Fatalf("chart rows = %d", len(chart.Rows))
		}
		if chart.YMax != "4" {
			t.Fatalf("empty chart max = %q", chart.YMax)
		}
		if strings.Count(chart.Path, "L") > days-1 {
			t.Fatalf("too many segments: %q", chart.Path)
		}
	}
	one := 1.0
	three := 3.0
	path, _ := lineGeometry([]*float64{&one, nil, &three}, 4)
	if strings.Count(path, "M") != 2 || strings.Count(path, "L") != 0 {
		t.Fatalf("gap path = %q", path)
	}
	if p, max := lineGeometry(nil, 0); p != "" || max != 1 {
		t.Fatalf("empty geometry = %q %v", p, max)
	}
}

func TestNiceMaxAndAxisLabels(t *testing.T) {
	for in, want := range map[float64]float64{0: 1, 1: 1, 7: 10, 472: 500, 1400: 2000, 0.011: 0.02, 0.31: 0.5} {
		if got := niceMax(in); got != want {
			t.Errorf("niceMax(%v) = %v want %v", in, got, want)
		}
	}
	if got := formatValue(1400); got != "1,400" {
		t.Fatalf("axis value = %q", got)
	}
	if got := formatUSD(0.011); got != "$0.01" {
		t.Fatalf("usd = %q", got)
	}
	if got := formatUSD(2); got != "$2.00" {
		t.Fatalf("budget usd = %q", got)
	}
	if got := formatUSD(0.0006); got != "<$0.01" {
		t.Fatalf("small usd = %q", got)
	}
}

func TestEveryEnumHasAPlainLabel(t *testing.T) {
	for _, spec := range guard.Specs() {
		name := spec.Name
		label := toolLabel(name)
		if label == "" || label == "Other" {
			t.Errorf("tool %q has no plain label", name)
		}
	}
	for _, kind := range []string{"agent", "metrics", "dream"} {
		if label := kindLabel(kind); label == "" || label == "Run" && kind != "agent" {
			t.Errorf("kind %q has no plain label", kind)
		}
	}
	for _, reason := range endReasons {
		if label := endReasonLabel(reason); label == "" {
			t.Errorf("end reason %q has no plain label", reason)
		}
	}
	for _, code := range blockedReasons {
		if label, ok := blockedLabels[code]; !ok || label == "" {
			t.Errorf("blocked code %q has no plain label", code)
		}
	}
	for code := range blockedLabels {
		if !oneOfOK(code, blockedReasons...) {
			t.Errorf("blocked label for unmapped code %q", code)
		}
	}
	for _, source := range config.SourceNames {
		if _, ok := sourceTags[source]; !ok {
			t.Errorf("source %q has no tag", source)
		}
	}
	for source := range sourceTags {
		if !slices.Contains(config.SourceNames, source) {
			t.Errorf("tag for unknown source %q", source)
		}
	}
	if toolLabel("new_tool") != "Other" || endReasonLabel("new_reason") != "Stopped" || blockedLabel("new_code") != "Blocked" || sourceTag("new_source") != "WEB" {
		t.Fatal("unknown enum fallbacks are not plain")
	}
	if statusText("killed") != "Stopped" || statusText("new") != "Unknown" {
		t.Fatal("status mapping")
	}
	if endReasonBad("too_few_episodes") || endReasonBad("nothing_to_do") || endReasonLabel("nothing_to_do") != "Skipped: nothing to do" || endReasonLabel("too_few_episodes") != "Skipped: too few new results" || endReasonLabel("browser_error") != "Browser error" || endReasonLabel("provider_error") != "Model error" {
		t.Fatal("end reason wording")
	}
}

func TestPageCacheCollapsesCapsAndServesStaleWithinItsBound(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	c := newPageCache(10*time.Second, 2*time.Minute, 2, clock)
	renders := 0
	fn := func(body string) func() (pageResponse, error) {
		return func() (pageResponse, error) {
			renders++
			return pageResponse{status: 200, body: []byte(body), etag: etagFor([]byte(body))}, nil
		}
	}
	for _, key := range []string{"a", "b", "c"} {
		if _, _, err := c.do(key, fn(key)); err != nil {
			t.Fatal(err)
		}
	}
	if c.len() > 2 {
		t.Fatalf("cache size = %d", c.len())
	}
	if renders != 3 {
		t.Fatalf("renders = %d", renders)
	}
	if _, _, err := c.do("c", fn("c")); err != nil {
		t.Fatal(err)
	}
	if renders != 3 {
		t.Fatalf("fresh hit re-rendered: %d", renders)
	}
	now = now.Add(11 * time.Second)
	resp, stale, err := c.do("c", func() (pageResponse, error) { return pageResponse{}, errTest })
	if err != nil || !stale || string(resp.body) != "c" {
		t.Fatalf("stale fallback: %q %v %v", resp.body, stale, err)
	}
	now = now.Add(3 * time.Minute)
	if _, _, err = c.do("c", func() (pageResponse, error) { return pageResponse{}, errTest }); err == nil {
		t.Fatal("stale window did not expire")
	}
}

var errTest = testError("render failed")

type testError string

func (e testError) Error() string { return string(e) }

func TestPageCacheCollapsesConcurrentMisses(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	c := newPageCache(10*time.Second, time.Minute, 8, func() time.Time { return now })
	renders := 0
	start := make(chan struct{})
	done := make(chan struct{}, 16)
	for range 16 {
		go func() {
			<-start
			if _, _, err := c.do("same", func() (pageResponse, error) {
				renders++
				time.Sleep(20 * time.Millisecond)
				return pageResponse{status: 200, body: []byte("x")}, nil
			}); err != nil {
				t.Error(err)
			}
			done <- struct{}{}
		}()
	}
	close(start)
	for range 16 {
		<-done
	}
	if renders != 1 {
		t.Fatalf("renders = %d, want 1", renders)
	}
}

type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn, 2), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

func TestLimitedListenerCapsConcurrentConnections(t *testing.T) {
	inner := newPipeListener()
	l := newLimitedListener(inner, 1)
	first := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			first <- c
		}
	}()
	server, client := net.Pipe()
	inner.conns <- server
	conn := <-first
	second := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			second <- c
		}
	}()
	otherServer, _ := net.Pipe()
	inner.conns <- otherServer
	select {
	case <-second:
		t.Fatal("second connection accepted before the first closed")
	case <-time.After(30 * time.Millisecond):
	}
	_ = conn.Close()
	select {
	case c := <-second:
		_ = c.Close()
	case <-time.After(time.Second):
		t.Fatal("second connection not accepted after close")
	}
	_ = client.Close()
	_ = l.Close()
}

func TestConnectionSemaphoreCapsConcurrency(t *testing.T) {
	h := &Handler{sem: make(chan struct{}, 1)}
	if !h.acquire() {
		t.Fatal("first acquire failed")
	}
	if h.acquire() {
		t.Fatal("second acquire exceeded the cap")
	}
	h.release()
	if !h.acquire() {
		t.Fatal("release did not free a slot")
	}
}

func TestCountLabelsUseSingularOnlyForOne(t *testing.T) {
	cases := []struct {
		one, many        string
		zero, oneT, twoT string
	}{
		{"draft", "drafts", "0 drafts", "1 draft", "2 drafts"},
		{"run", "runs", "0 runs", "1 run", "2 runs"},
		{"step", "steps", "0 steps", "1 step", "2 steps"},
		{"note", "notes", "0 notes", "1 note", "2 notes"},
		{"source", "sources", "0 sources", "1 source", "2 sources"},
		{"day", "days", "0 days", "1 day", "2 days"},
		{"check", "checks", "0 checks", "1 check", "2 checks"},
		{"view", "views", "0 views", "1 view", "2 views"},
		{"post", "posts", "0 posts", "1 post", "2 posts"},
		{"topic", "topics", "0 topics", "1 topic", "2 topics"},
		{"try", "tries", "0 tries", "1 try", "2 tries"},
		{"hour", "hours", "0 hours", "1 hour", "2 hours"},
	}
	for _, tc := range cases {
		for n, want := range map[int]string{0: tc.zero, 1: tc.oneT, 2: tc.twoT} {
			if got := countText(n, tc.one, tc.many); got != want {
				t.Errorf("countText(%d, %q, %q) = %q want %q", n, tc.one, tc.many, got, want)
			}
			if n == 1 && pluralWord(1, tc.one, tc.many) != tc.one {
				t.Errorf("pluralWord(1, %q, %q) is not singular", tc.one, tc.many)
			}
		}
	}
	if got := formatCount(1) + " " + pluralWord(1, "view", "views"); got != "1 view" {
		t.Fatalf("formatted view label = %q", got)
	}
	if got := formatCount(1000) + " " + pluralWord(1000, "view", "views"); got != "1,000 views" {
		t.Fatalf("formatted view label = %q", got)
	}
}

func TestCountVerbsUsedInState(t *testing.T) {
	for _, tc := range []struct {
		one, many string
		want      [3]string
	}{
		{"draft is", "drafts are", [3]string{"0 drafts are", "1 draft is", "2 drafts are"}},
		{"step was", "steps were", [3]string{"0 steps were", "1 step was", "2 steps were"}},
		{"try", "tries", [3]string{"0 tries", "1 try", "2 tries"}},
		{"run", "runs", [3]string{"0 runs", "1 run", "2 runs"}},
	} {
		for n := 0; n <= 2; n++ {
			got := fmt.Sprintf("%d %s", n, plural(n, tc.one, tc.many))
			if got != tc.want[n] {
				t.Errorf("state label %d %q/%q = %q want %q", n, tc.one, tc.many, got, tc.want[n])
			}
		}
	}
}
