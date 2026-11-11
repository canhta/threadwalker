package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func readTrendFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "trends", name))
	if err != nil {
		t.Fatalf("reading synthetic fixture %s: %v", name, err)
	}
	return body
}

type fakeTrendsServer struct {
	server *httptest.Server

	mu           sync.Mutex
	topRequests  int
	phRequests   int
	gtRequests   int
	itemRequests []int64
	requestPaths []string

	topBody   []byte
	phBody    []byte
	gtBody    []byte
	topStatus int
	phStatus  int
	gtStatus  int

	itemBody     map[int64][]byte
	itemStatus   map[int64]int
	itemFallback func(int64) []byte
}

func newFakeTrendsServer(t *testing.T) *fakeTrendsServer {
	t.Helper()
	fake := &fakeTrendsServer{
		topBody:    readTrendFixture(t, "hn_topstories.json"),
		phBody:     readTrendFixture(t, "producthunt.xml"),
		gtBody:     readTrendFixture(t, "googletrends.xml"),
		itemBody:   make(map[int64][]byte),
		itemStatus: make(map[int64]int),
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeTrendsServer) options() TrendsOptions {
	return TrendsOptions{
		HTTPClient:      f.server.Client(),
		Timeout:         5 * time.Second,
		MaxItems:        20,
		HNBaseURL:       f.server.URL + "/v0",
		ProductHuntURL:  f.server.URL + "/feed",
		GoogleTrendsURL: f.server.URL + "/trends",
	}
}

func (f *fakeTrendsServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requestPaths = append(f.requestPaths, r.URL.Path)
	f.mu.Unlock()

	switch {
	case r.URL.Path == "/v0/topstories.json":
		f.mu.Lock()
		f.topRequests++
		status, body := f.topStatus, f.topBody
		f.mu.Unlock()
		writeTrendResponse(w, status, body)
	case strings.HasPrefix(r.URL.Path, "/v0/item/") && strings.HasSuffix(r.URL.Path, ".json"):
		rawID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v0/item/"), ".json")
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.itemRequests = append(f.itemRequests, id)
		f.mu.Unlock()
		status, body := f.itemResponse(id)
		writeTrendResponse(w, status, body)
	case r.URL.Path == "/feed":
		f.mu.Lock()
		f.phRequests++
		status, body := f.phStatus, f.phBody
		f.mu.Unlock()
		writeTrendResponse(w, status, body)
	case r.URL.Path == "/trends":
		f.mu.Lock()
		f.gtRequests++
		status, body := f.gtStatus, f.gtBody
		f.mu.Unlock()
		writeTrendResponse(w, status, body)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTrendsServer) itemResponse(id int64) (int, []byte) {
	f.mu.Lock()
	if status, ok := f.itemStatus[id]; ok {
		f.mu.Unlock()
		return status, []byte("SECRET-RAW-BODY API-KEY-123 https://private.example/token")
	}
	if body, ok := f.itemBody[id]; ok {
		f.mu.Unlock()
		return http.StatusOK, body
	}
	fallback := f.itemFallback
	f.mu.Unlock()

	path := filepath.Join("testdata", "trends", fmt.Sprintf("hn_item_%d.json", id))
	if body, err := os.ReadFile(path); err == nil {
		return http.StatusOK, body
	}
	if fallback != nil {
		return http.StatusOK, fallback(id)
	}
	return http.StatusNotFound, []byte("missing fixture")
}

func writeTrendResponse(w http.ResponseWriter, status int, body []byte) {
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (f *fakeTrendsServer) counts() (top, ph, gt int, items []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.topRequests, f.phRequests, f.gtRequests, append([]int64(nil), f.itemRequests...)
}

func (f *fakeTrendsServer) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requestPaths...)
}

func TestFetchTrends_SuccessAcrossSources(t *testing.T) {
	fake := newFakeTrendsServer(t)
	opts := fake.options()

	trends, err := FetchTrends(context.Background(), opts)
	if err != nil {
		t.Fatalf("FetchTrends returned error: %v", err)
	}

	want := []Trend{
		{Source: sourceHackerNews, Title: "Show HN: A & B — tooling for Go", URL: "https://example.com/hn/101?ref=top", Score: 321},
		{Source: sourceProductHunt, Title: "Ship & Tell — 日本語 & Tiếng Việt", URL: "https://www.producthunt.com/posts/ship-and-tell?utm=x&y=1"},
		{Source: sourceGoogleTrends, Title: "giá vàng & chứng khoán", URL: "https://trends.google.com/trending/rss?geo=VN&q=gold"},
		{Source: sourceHackerNews, Title: "Ask HN: Làm sao để học Go?", URL: "https://news.ycombinator.com/item?id=102", Score: 87},
		{Source: sourceProductHunt, Title: "CDATA <Tool> & More", URL: "https://www.producthunt.com/posts/cdata-tool"},
		{Source: sourceGoogleTrends, Title: "AI <tool> 日本語", URL: "https://trends.google.com/trends/explore?q=ai"},
		{Source: sourceHackerNews, Title: "Product <launch> — 日本語", URL: "https://example.com/hn/103", Score: 12},
		{Source: sourceProductHunt, Title: "Fallback link ✓", URL: "https://www.producthunt.com/posts/fallback"},
	}
	if !reflect.DeepEqual(trends, want) {
		t.Fatalf("trends mismatch\ngot:  %#v\nwant: %#v", trends, want)
	}

	again, err := FetchTrends(context.Background(), opts)
	if err != nil {
		t.Fatalf("second FetchTrends returned error: %v", err)
	}
	if !reflect.DeepEqual(trends, again) {
		t.Fatalf("FetchTrends ordering is not deterministic\nfirst:  %#v\nsecond: %#v", trends, again)
	}

	top, ph, gt, items := fake.counts()
	if top != 2 || ph != 2 || gt != 2 {
		t.Fatalf("unexpected source request counts: top=%d ph=%d gt=%d", top, ph, gt)
	}
	wantItems := []int64{101, 102, 103, 104, 105, 106, 107, 101, 102, 103, 104, 105, 106, 107}
	if !reflect.DeepEqual(items, wantItems) {
		t.Fatalf("HN item requests = %v, want %v", items, wantItems)
	}
}

func TestFetchTrends_EmptyData(t *testing.T) {
	fake := newFakeTrendsServer(t)
	fake.topBody = []byte("[]")
	fake.phBody = readTrendFixture(t, "empty_feed.xml")
	fake.gtBody = readTrendFixture(t, "empty_rss.xml")

	opts := fake.options()
	opts.MaxItems = 0 // Select the default total.
	opts.Timeout = 0  // Select the default timeout.

	trends, err := FetchTrends(context.Background(), opts)
	if err != nil {
		t.Fatalf("FetchTrends returned error: %v", err)
	}
	if len(trends) != 0 {
		t.Fatalf("trends = %#v, want empty", trends)
	}
	top, ph, gt, items := fake.counts()
	if top != 1 || ph != 1 || gt != 1 || len(items) != 0 {
		t.Fatalf("unexpected counts: top=%d ph=%d gt=%d items=%v", top, ph, gt, items)
	}
}

func TestFetchTrends_MalformedPayloads(t *testing.T) {
	tests := []struct {
		name string
		set  func(*fakeTrendsServer)
		want string
	}{
		{
			name: "hacker news top stories",
			set:  func(f *fakeTrendsServer) { f.topBody = readTrendFixture(t, "malformed.json") },
			want: "decode hacker news top stories",
		},
		{
			name: "hacker news item",
			set: func(f *fakeTrendsServer) {
				f.itemBody[101] = readTrendFixture(t, "malformed.json")
			},
			want: "decode hacker news item 101",
		},
		{
			name: "product hunt xml",
			set:  func(f *fakeTrendsServer) { f.phBody = readTrendFixture(t, "malformed_feed.xml") },
			want: "decode product hunt feed",
		},
		{
			name: "google trends xml",
			set:  func(f *fakeTrendsServer) { f.gtBody = readTrendFixture(t, "malformed_feed.xml") },
			want: "decode google trends feed",
		},
		{
			name: "html served as a feed",
			set:  func(f *fakeTrendsServer) { f.phBody = readTrendFixture(t, "unexpected_root.xml") },
			want: "unsupported feed root",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeTrendsServer(t)
			test.set(fake)

			trends, err := FetchTrends(context.Background(), fake.options())
			if err == nil {
				t.Fatalf("FetchTrends succeeded with trends %#v, want an error", trends)
			}
			if trends != nil {
				t.Fatalf("FetchTrends returned partial trends %#v with error", trends)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not contain %q", err, test.want)
			}
			assertSafeTrendError(t, err, fake.server.URL)
		})
	}
}

func TestFetchTrends_BadStatus(t *testing.T) {
	tests := []struct {
		name string
		set  func(*fakeTrendsServer)
		want []string
	}{
		{
			name: "hacker news top stories",
			set:  func(f *fakeTrendsServer) { f.topStatus = http.StatusServiceUnavailable },
			want: []string{"hacker news top stories", "status 503"},
		},
		{
			name: "hacker news item",
			set:  func(f *fakeTrendsServer) { f.itemStatus[102] = http.StatusServiceUnavailable },
			want: []string{"hacker news item 102", "status 503"},
		},
		{
			name: "product hunt",
			set:  func(f *fakeTrendsServer) { f.phStatus = http.StatusBadGateway },
			want: []string{"product hunt feed", "status 502"},
		},
		{
			name: "google trends",
			set:  func(f *fakeTrendsServer) { f.gtStatus = http.StatusInternalServerError },
			want: []string{"google trends feed", "status 500"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeTrendsServer(t)
			test.set(fake)

			trends, err := FetchTrends(context.Background(), fake.options())
			if err == nil {
				t.Fatalf("FetchTrends succeeded with trends %#v, want an error", trends)
			}
			if trends != nil {
				t.Fatalf("FetchTrends returned partial trends %#v with error", trends)
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
			assertSafeTrendError(t, err, fake.server.URL)
		})
	}
}

func TestFetchTrends_OversizedPayload(t *testing.T) {
	oversized := append([]byte("PAYLOAD-SECRET "), bytes.Repeat([]byte("x"), maxTrendResponseBytes+1)...)
	tests := []struct {
		name string
		set  func(*fakeTrendsServer)
	}{
		{name: "hacker news top stories", set: func(f *fakeTrendsServer) { f.topBody = oversized }},
		{name: "hacker news item", set: func(f *fakeTrendsServer) { f.itemBody[101] = oversized }},
		{name: "product hunt", set: func(f *fakeTrendsServer) { f.phBody = oversized }},
		{name: "google trends", set: func(f *fakeTrendsServer) { f.gtBody = oversized }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeTrendsServer(t)
			test.set(fake)

			trends, err := FetchTrends(context.Background(), fake.options())
			if err == nil {
				t.Fatalf("FetchTrends succeeded with trends %#v, want an error", trends)
			}
			if trends != nil {
				t.Fatalf("FetchTrends returned partial trends %#v with error", trends)
			}
			if !strings.Contains(err.Error(), "response body exceeds") {
				t.Fatalf("error %q does not name the payload cap", err)
			}
			assertSafeTrendError(t, err, fake.server.URL, "PAYLOAD-SECRET")
		})
	}
}

func TestFetchTrends_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	opts := TrendsOptions{
		HTTPClient:      server.Client(),
		Timeout:         30 * time.Millisecond,
		HNBaseURL:       server.URL + "/v0",
		ProductHuntURL:  server.URL + "/feed",
		GoogleTrendsURL: server.URL + "/trends",
	}
	trends, err := FetchTrends(context.Background(), opts)
	if err == nil {
		t.Fatalf("FetchTrends succeeded with trends %#v, want a timeout", trends)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v does not wrap context.DeadlineExceeded", err)
	}
	assertSafeTrendError(t, err, server.URL)
}

func TestFetchTrends_Cancellation(t *testing.T) {
	fake := newFakeTrendsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	trends, err := FetchTrends(ctx, fake.options())
	if err == nil {
		t.Fatalf("FetchTrends succeeded with trends %#v, want cancellation", trends)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v does not wrap context.Canceled", err)
	}
	if trends != nil {
		t.Fatalf("FetchTrends returned trends %#v with a cancellation error", trends)
	}
	assertSafeTrendError(t, err, fake.server.URL)
}

func TestFetchTrends_NilContext(t *testing.T) {
	trends, err := FetchTrends(nil, TrendsOptions{})
	if err == nil || err.Error() != "fetch trends: nil context" {
		t.Fatalf("FetchTrends(nil) = (%#v, %v), want a nil-context error", trends, err)
	}
}

func TestFetchTrends_ConfiguredClientAndEndpoints(t *testing.T) {
	fake := newFakeTrendsServer(t)
	transport := &countingTransport{base: http.DefaultTransport}
	opts := fake.options()
	opts.HTTPClient = &http.Client{Transport: transport}
	opts.HNBaseURL = fake.server.URL + "/v0/" // Trailing slashes are normalized.

	trends, err := FetchTrends(context.Background(), opts)
	if err != nil {
		t.Fatalf("FetchTrends returned error: %v", err)
	}
	if len(trends) != 8 {
		t.Fatalf("len(trends) = %d, want 8", len(trends))
	}
	if transport.count != 10 {
		t.Fatalf("configured client saw %d requests, want 10", transport.count)
	}
	_, _, _, items := fake.counts()
	if len(items) != 7 {
		t.Fatalf("HN item requests = %v, want 7", items)
	}
	if transport.missingDeadline {
		t.Fatal("a request did not inherit a context deadline")
	}

	paths := fake.paths()
	for _, want := range []string{"/v0/topstories.json", "/v0/item/101.json", "/feed", "/trends"} {
		if !containsString(paths, want) {
			t.Fatalf("configured endpoint path %q not requested; paths=%v", want, paths)
		}
	}
}

func TestFetchTrends_DefaultClientWithInjectedEndpoints(t *testing.T) {
	fake := newFakeTrendsServer(t)
	opts := fake.options()
	opts.HTTPClient = nil // Exercise the default client branch offline.

	trends, err := FetchTrends(context.Background(), opts)
	if err != nil {
		t.Fatalf("FetchTrends returned error: %v", err)
	}
	if len(trends) != 8 {
		t.Fatalf("len(trends) = %d, want 8", len(trends))
	}
}

func TestFetchTrends_BoundsSourcesAndTotal(t *testing.T) {
	tests := []struct {
		name        string
		maxItems    int
		wantItems   int
		wantTotal   int
		wantHNItems int
	}{
		{name: "total cap", maxItems: 3, wantItems: 3, wantTotal: 3, wantHNItems: 1},
		{name: "per source cap", maxItems: 100, wantItems: maxTrendItemsPerSource, wantTotal: 15, wantHNItems: 10},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeTrendsServer(t)
			fake.topBody = makeTopStories(t, 50)
			fake.itemFallback = func(id int64) []byte {
				return []byte(fmt.Sprintf(`{"id":%d,"type":"story","title":"Story %d","url":"https://example.com/hn/%d","score":%d}`, id, id, id, id))
			}

			opts := fake.options()
			opts.MaxItems = test.maxItems
			trends, err := FetchTrends(context.Background(), opts)
			if err != nil {
				t.Fatalf("FetchTrends returned error: %v", err)
			}
			_, _, _, items := fake.counts()
			if len(items) != test.wantItems {
				t.Fatalf("HN item requests = %d, want %d", len(items), test.wantItems)
			}
			if len(trends) != test.wantTotal {
				t.Fatalf("len(trends) = %d, want %d", len(trends), test.wantTotal)
			}
			if len(trends) > test.maxItems {
				t.Fatalf("len(trends) = %d exceeds MaxItems %d", len(trends), test.maxItems)
			}

			perSource := map[string]int{}
			for _, trend := range trends {
				perSource[trend.Source]++
			}
			if perSource[sourceHackerNews] != test.wantHNItems {
				t.Fatalf("HN output count = %d, want %d", perSource[sourceHackerNews], test.wantHNItems)
			}
			for source, count := range perSource {
				if count > maxTrendItemsPerSource {
					t.Fatalf("source %s returned %d items, cap is %d", source, count, maxTrendItemsPerSource)
				}
			}
		})
	}
}

func TestFetchTrends_InvalidEndpointIsSafe(t *testing.T) {
	trends, err := FetchTrends(context.Background(), TrendsOptions{
		HNBaseURL: "://bad",
		Timeout:   time.Second,
	})
	if err == nil {
		t.Fatalf("FetchTrends succeeded with trends %#v, want an error", trends)
	}
	if !strings.Contains(err.Error(), "hacker news top stories: build request") {
		t.Fatalf("error %q does not name the failed operation", err)
	}
	assertSafeTrendError(t, err)
}

func TestFetchTrends_RequestFailureIsSafe(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("DIAL-SECRET https://private.example/token")
	})}

	trends, err := FetchTrends(context.Background(), TrendsOptions{
		HTTPClient: client,
		Timeout:    time.Second,
	})
	if err == nil {
		t.Fatalf("FetchTrends succeeded with trends %#v, want an error", trends)
	}
	if !strings.Contains(err.Error(), "hacker news top stories: request failed") {
		t.Fatalf("error %q does not name the failed operation", err)
	}
	assertSafeTrendError(t, err, "DIAL-SECRET")
}

func TestFetchTrends_BodyReadFailureIsSafe(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       errorReadCloser{err: errors.New("READ-SECRET https://private.example/token")},
		}, nil
	})}

	trends, err := FetchTrends(context.Background(), TrendsOptions{
		HTTPClient: client,
		Timeout:    time.Second,
	})
	if err == nil {
		t.Fatalf("FetchTrends succeeded with trends %#v, want an error", trends)
	}
	if !strings.Contains(err.Error(), "hacker news top stories: read response body") {
		t.Fatalf("error %q does not name the failed operation", err)
	}
	assertSafeTrendError(t, err, "READ-SECRET")
}

func TestParseTrendFeedRDF(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/">
  <item>
    <title>RDF &amp; item</title>
    <link>https://example.com/rdf</link>
  </item>
</rdf:RDF>`)

	records, err := parseTrendFeed(body)
	if err != nil {
		t.Fatalf("parseTrendFeed returned error: %v", err)
	}
	want := []feedRecord{{title: "RDF & item", link: "https://example.com/rdf"}}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("records = %#v, want %#v", records, want)
	}
}

func TestAtomEntryLink(t *testing.T) {
	tests := []struct {
		name  string
		links []feedLink
		want  string
	}{
		{
			name:  "alternate html preferred over earlier self",
			links: []feedLink{{Rel: "self", Type: "application/atom+xml", Href: "https://example.com/self"}, {Rel: "alternate", Type: "text/html", Href: "https://example.com/html"}},
			want:  "https://example.com/html",
		},
		{
			name:  "first href fallback when no alternate",
			links: []feedLink{{Rel: "self", Type: "application/atom+xml", Href: "https://example.com/self"}},
			want:  "https://example.com/self",
		},
		{
			name:  "alternate without type",
			links: []feedLink{{Rel: "alternate", Href: "https://example.com/plain"}},
			want:  "https://example.com/plain",
		},
		{
			name:  "alternate html with charset",
			links: []feedLink{{Rel: "self", Href: "https://example.com/self"}, {Rel: "alternate", Type: "text/html; charset=utf-8", Href: "https://example.com/html"}},
			want:  "https://example.com/html",
		},
		{
			name:  "no links",
			links: nil,
			want:  "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := atomEntryLink(test.links); got != test.want {
				t.Fatalf("atomEntryLink(%#v) = %q, want %q", test.links, got, test.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("  ", "fallback"); got != "fallback" {
		t.Fatalf("firstNonEmpty blank = %q, want fallback", got)
	}
	if got := firstNonEmpty("value", "fallback"); got != "value" {
		t.Fatalf("firstNonEmpty value = %q, want value", got)
	}
}

func TestMergeTrendsEmptyAndLimitBranches(t *testing.T) {
	if got := mergeTrends(3); len(got) != 0 {
		t.Fatalf("mergeTrends with no groups = %#v, want empty", got)
	}
	groups := [][]Trend{
		{{Source: "a", Title: "a1"}, {Source: "a", Title: "a2"}},
		{{Source: "b", Title: "b1"}},
	}
	got := mergeTrends(2, groups...)
	want := []Trend{{Source: "a", Title: "a1"}, {Source: "b", Title: "b1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeTrends = %#v, want %#v", got, want)
	}
}

func assertSafeTrendError(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	for _, value := range append([]string{"http://", "https://", "SECRET", "API-KEY"}, forbidden...) {
		if value == "" {
			continue
		}
		if strings.Contains(message, value) {
			t.Fatalf("error %q leaks forbidden diagnostic %q", message, value)
		}
	}
}

func makeTopStories(t *testing.T, count int) []byte {
	t.Helper()
	ids := make([]int64, count)
	for index := range ids {
		ids[index] = int64(index + 1)
	}
	body, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("marshalling synthetic top stories: %v", err)
	}
	return body
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type countingTransport struct {
	base            http.RoundTripper
	count           int
	missingDeadline bool
}

func (c *countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	c.count++
	if _, ok := request.Context().Deadline(); !ok {
		c.missingDeadline = true
	}
	return c.base.RoundTrip(request)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type errorReadCloser struct {
	err error
}

func (r errorReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r errorReadCloser) Close() error {
	return nil
}

var _ io.ReadCloser = errorReadCloser{}
