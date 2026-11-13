package threads

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
)

const testToken = "SYNTHETIC_TEST_TOKEN"

type staticToken struct{}

func (staticToken) Token(context.Context) (config.Secret, error) {
	return config.NewSecret(testToken), nil
}

func apiFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "api", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// fakeThreads serves recorded fixtures by method and path and records every
// request. Statuses are served in order, the last repeating; a route that
// starts with `{` is served as the literal body.
type fakeThreads struct {
	t        *testing.T
	mu       sync.Mutex
	requests []string
	queries  map[string]map[string]string
	routes   map[string][]string
	failures map[string]int
}

func newFake(t *testing.T) (*fakeThreads, *API, *int) {
	t.Helper()
	f := &fakeThreads{t: t, queries: map[string]map[string]string{}, routes: map[string][]string{}, failures: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	waits := 0
	api, err := NewAPI(APIOptions{BaseURL: srv.URL, UserID: "1000000000000001", Tokens: staticToken{}, HTTP: srv.Client(), Timeout: time.Second,
		Wait: func(context.Context, time.Duration) error { waits++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return f, api, &waits
}

func (f *fakeThreads) route(method, path string, fixtures ...string) {
	f.routes[method+" "+path] = fixtures
}

func (f *fakeThreads) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.requests = append(f.requests, key)
	q := map[string]string{}
	for k, v := range r.URL.Query() {
		q[k] = v[0]
	}
	f.queries[key] = q
	if strings.Contains(r.URL.String(), testToken) || r.Header.Get("Authorization") != "Bearer "+testToken {
		f.t.Errorf("token must travel only in the Authorization header: %s", r.URL)
	}
	files := f.routes[key]
	if len(files) == 0 {
		http.Error(w, string(apiFixture(f.t, "error.json")), http.StatusBadRequest)
		return
	}
	n := f.failures[key]
	file := files[min(n, len(files)-1)]
	f.failures[key]++
	if file == "error.json" {
		w.WriteHeader(http.StatusBadRequest)
	}
	if strings.HasPrefix(file, "{") {
		_, _ = w.Write([]byte(file))
		return
	}
	_, _ = w.Write(apiFixture(f.t, file))
}

func (f *fakeThreads) count(key string) int {
	n := 0
	for _, r := range f.requests {
		if r == key {
			n++
		}
	}
	return n
}

func TestAPIReads(t *testing.T) {
	f, api, _ := newFake(t)
	ctx := context.Background()
	f.route("GET", "/keyword_search", "keyword_search.json")
	f.route("GET", "/2000000000000001", "post.json")
	f.route("GET", "/2000000000000001/replies", "replies.json")
	f.route("GET", "/1000000000000001/mentions", "mentions.json")

	page, err := api.Search(ctx, "go tools", false)
	if err != nil || len(page.Posts) != 1 || page.Posts[0].ID != "2000000000000005" || page.Posts[0].URL != "https://www.threads.com/@synthetic_author/post/SYNTH0005" || page.Posts[0].AddressedToAgent {
		t.Fatalf("search=%+v err=%v", page, err)
	}
	if q := f.queries["GET /keyword_search"]; q["q"] != "go tools" || q["search_type"] != "TOP" {
		t.Fatalf("search query=%v", q)
	}
	page, err = api.ReadPost(ctx, "2000000000000001")
	// The API sends "+0000"; Post.Timestamp is parsed UTC time.
	if err != nil || page.Kind != "post" || page.Posts[0].Username != "tester_account" || page.Posts[0].Text != "synthetic own post" || !page.Posts[0].Timestamp.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("post=%+v err=%v", page, err)
	}
	page, err = api.ReadReplies(ctx, "2000000000000001")
	if err != nil || page.Kind != "replies" || len(page.Posts) != 2 || !page.Posts[0].IsReply || !page.Posts[0].AddressedToAgent || page.Posts[0].Username != "synthetic_author" || page.Posts[1].Username != "another_author" {
		t.Fatalf("replies=%+v err=%v", page, err)
	}
	page, err = api.ReadReplies(ctx, "")
	if err != nil || page.Kind != "mentions" || len(page.Posts) != 1 || !page.Posts[0].AddressedToAgent {
		t.Fatalf("mentions=%+v err=%v", page, err)
	}
}

func TestAPIInsightsAndFollowers(t *testing.T) {
	f, api, _ := newFake(t)
	ctx := context.Background()
	f.route("GET", "/1000000000000001/threads_insights", "account_insights.json", "followers.json")
	m, err := api.ReadOwnMetrics(ctx)
	want := Metrics{Views: 1204, Likes: 31, Replies: 4, Reposts: 2, Quotes: n64(1), Followers: n64(0)}
	if err != nil || !reflect.DeepEqual(m, want) {
		t.Fatalf("account=%+v err=%v", m, err)
	}

	f.route("GET", "/2000000000000001/insights", "post_insights.json")
	m, err = api.ReadActionMetrics(ctx, "2000000000000001")
	if err != nil || !reflect.DeepEqual(m, Metrics{Views: 1500, Likes: 42, Replies: 7, Reposts: 3, Quotes: n64(2), Shares: n64(5)}) {
		t.Fatalf("post insights=%+v err=%v", m, err)
	}
	if got := f.queries["GET /2000000000000001/insights"]["metric"]; got != "views,likes,replies,reposts,quotes,shares" {
		t.Fatalf("metric list %q", got)
	}
}

func n64(v int64) *int64 { return &v }

func TestAPIPostInsightsMissingCountersAreUnavailable(t *testing.T) {
	for name, body := range map[string]string{
		"no views":  `{"data":[{"name":"likes","values":[{"value":1}]},{"name":"replies","values":[{"value":1}]},{"name":"reposts","values":[{"value":1}]}]}`,
		"no likes":  `{"data":[{"name":"views","values":[{"value":9}]},{"name":"replies","values":[{"value":1}]},{"name":"reposts","values":[{"value":1}]}]}`,
		"no counts": `{"data":[]}`,
	} {
		f, api, _ := newFake(t)
		f.route("GET", "/2000000000000001/insights", body)
		if _, err := api.ReadActionMetrics(context.Background(), "2000000000000001"); !errors.Is(err, ErrMetricsUnavailable) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
	f, api, _ := newFake(t)
	f.route("GET", "/2000000000000001/insights", `{"data":[{"name":"views","values":[{"value":9}]},{"name":"likes","values":[{"value":1}]},{"name":"replies","values":[{"value":0}]},{"name":"reposts","values":[{"value":0}]}]}`)
	m, err := api.ReadActionMetrics(context.Background(), "2000000000000001")
	if err != nil || m.Quotes != nil || m.Shares != nil || m.Followers != nil {
		t.Fatalf("absent quotes and shares must stay nil: %+v err=%v", m, err)
	}
}

func TestAPIReplyRepliesComeFromDirectReplies(t *testing.T) {
	const zero = `{"data":[{"name":"views","values":[{"value":9}]},{"name":"likes","values":[{"value":2}]},{"name":"replies","values":[{"value":0}]},{"name":"reposts","values":[{"value":0}]}]}`
	f, api, _ := newFake(t)
	f.route("GET", "/3/insights", zero)
	f.route("GET", "/3", `{"id":"3","is_reply":true}`)
	f.route("GET", "/3/replies", `{"data":[{"id":"a"},{"id":"b"}]}`)
	if m, err := api.ReadActionMetrics(context.Background(), "3"); err != nil || m.Replies != 2 || m.Likes != 2 {
		t.Fatalf("reply: %+v err=%v", m, err)
	}
	f.route("GET", "/3/replies", "error.json")
	if m, err := api.ReadActionMetrics(context.Background(), "3"); err != nil || m.Replies != 0 {
		t.Fatalf("failed /replies keeps insights: %+v err=%v", m, err)
	}
	// A post keeps its insights value and never reads /replies.
	f.route("GET", "/4/insights", `{"data":[{"name":"views","values":[{"value":9}]},{"name":"likes","values":[{"value":1}]},{"name":"replies","values":[{"value":5}]},{"name":"reposts","values":[{"value":0}]}]}`)
	f.route("GET", "/4", `{"id":"4","is_reply":false}`)
	f.route("GET", "/4/replies", `{"data":[{"id":"a"}]}`)
	if m, err := api.ReadActionMetrics(context.Background(), "4"); err != nil || m.Replies != 5 || f.count("GET /4/replies") != 0 {
		t.Fatalf("post: %+v err=%v", m, err)
	}
}

func TestAPIFollowersFixtureReadsAccountValue(t *testing.T) {
	var in insights
	if err := json.Unmarshal(apiFixture(t, "followers.json"), &in); err != nil {
		t.Fatal(err)
	}
	m, err := in.metrics()
	if err != nil || m.Followers == nil || *m.Followers != 0 || len(in.Data) != 1 || in.Data[0].Name != "followers_count" {
		t.Fatalf("followers=%+v err=%v", m, err)
	}
}

func TestAPIInsightsRejectNegativeAndHugeValues(t *testing.T) {
	for _, body := range []string{`{"data":[{"name":"views","total_value":{"value":-1}}]}`, `{"data":[{"name":"views","total_value":{"value":9007199254740993}}]}`, `{"data":[{"name":"views"}]}`} {
		var in insights
		if err := json.Unmarshal([]byte(body), &in); err != nil {
			t.Fatal(err)
		}
		if _, err := in.metrics(); !errors.Is(err, ErrMetricsUnavailable) {
			t.Fatalf("%s accepted: %v", body, err)
		}
	}
}

func TestAPIPublishingLimit(t *testing.T) {
	f, api, _ := newFake(t)
	f.route("GET", "/1000000000000001/threads_publishing_limit", "publishing_limit.json")
	q, err := api.PublishingLimit(context.Background())
	if err != nil || q != (Quota{PostsUsed: 0, PostsTotal: 250, RepliesUsed: 0, RepliesTotal: 1000}) {
		t.Fatalf("quota=%+v err=%v", q, err)
	}
}

func TestAPIPostPublishesAfterContainerFinishes(t *testing.T) {
	f, api, waits := newFake(t)
	f.route("POST", "/1000000000000001/threads", "container.json")
	f.route("GET", "/3000000000000001", "status_in_progress.json", "status_finished.json")
	f.route("POST", "/1000000000000001/threads_publish", "publish.json")
	f.route("GET", "/2000000000000001", "post_permalink.json")
	pub, err := api.Post(context.Background(), "hello", " #AI Threads & Node.js\n")
	if err != nil || pub != (Published{URL: "https://www.threads.com/@tester_account/post/SYNTH0001", ID: "2000000000000001"}) {
		t.Fatalf("published=%+v err=%v", pub, err)
	}
	if *waits != 1 || f.count("POST /1000000000000001/threads_publish") != 1 {
		t.Fatalf("waits=%d publishes=%d", *waits, f.count("POST /1000000000000001/threads_publish"))
	}
	if q := f.queries["POST /1000000000000001/threads"]; q["media_type"] != "TEXT" || q["text"] != "hello" || q["reply_to_id"] != "" || q["topic_tag"] != "AI Threads Nodejs" {
		t.Fatalf("container query=%v", q)
	}
	if f.queries["POST /1000000000000001/threads_publish"]["creation_id"] != "3000000000000001" {
		t.Fatal("publish did not use the container id")
	}
}

func TestAPIReplySendsReplyToID(t *testing.T) {
	f, api, _ := newFake(t)
	f.route("POST", "/1000000000000001/threads", "container.json")
	f.route("GET", "/3000000000000001", "status_finished.json")
	f.route("POST", "/1000000000000001/threads_publish", "publish.json")
	f.route("GET", "/2000000000000001", "post_permalink.json")
	if _, err := api.Reply(context.Background(), "2000000000000002", "thanks"); err != nil {
		t.Fatal(err)
	}
	if got := f.queries["POST /1000000000000001/threads"]["reply_to_id"]; got != "2000000000000002" {
		t.Fatalf("reply_to_id=%q", got)
	}
}

func TestAPIPostFailuresBeforePublishAreRefusedAndNeverPublish(t *testing.T) {
	for name, setup := range map[string]func(*fakeThreads){
		"container error":  func(f *fakeThreads) { f.route("POST", "/1000000000000001/threads", "error.json") },
		"container status": func(f *fakeThreads) { f.route("GET", "/3000000000000001", "status_error.json") },
		"never finishes":   func(f *fakeThreads) { f.route("GET", "/3000000000000001", "status_in_progress.json") },
	} {
		t.Run(name, func(t *testing.T) {
			f, api, _ := newFake(t)
			f.route("POST", "/1000000000000001/threads", "container.json")
			f.route("GET", "/3000000000000001", "status_finished.json")
			setup(f)
			_, err := api.Post(context.Background(), "x", "")
			if err == nil || errors.Is(err, ErrUnknownOutcome) {
				t.Fatalf("err=%v must fail without an unknown outcome", err)
			}
			if f.count("POST /1000000000000001/threads_publish") != 0 {
				t.Fatal("published despite a failed container")
			}
		})
	}
}

func TestAPIFailureAfterPublishCallIsUnknownAndNotRetried(t *testing.T) {
	for name, setup := range map[string]func(*fakeThreads){
		"publish rejected":     func(f *fakeThreads) { f.route("POST", "/1000000000000001/threads_publish", "error.json") },
		"publish no id":        func(f *fakeThreads) { f.route("POST", "/1000000000000001/threads_publish", "error.json") },
		"permalink unreadable": func(f *fakeThreads) { f.route("GET", "/2000000000000001", "error.json") },
	} {
		t.Run(name, func(t *testing.T) {
			f, api, _ := newFake(t)
			f.route("POST", "/1000000000000001/threads", "container.json")
			f.route("GET", "/3000000000000001", "status_finished.json")
			f.route("POST", "/1000000000000001/threads_publish", "publish.json")
			setup(f)
			_, err := api.Post(context.Background(), "x", "")
			if !errors.Is(err, ErrUnknownOutcome) || Reason(err) != ReasonUnknownOutcome {
				t.Fatalf("err=%v want unknown outcome", err)
			}
			if n := f.count("POST /1000000000000001/threads_publish"); n != 1 {
				t.Fatalf("publish sent %d times", n)
			}
		})
	}
}

func TestAPITimeoutDuringPublishIsUnknown(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1000000000000001/threads":
			_, _ = w.Write(apiFixture(t, "container.json"))
		case "/3000000000000001":
			_, _ = w.Write(apiFixture(t, "status_finished.json"))
		default:
			<-block
		}
	}))
	defer srv.Close()
	defer close(block)
	api, _ := NewAPI(APIOptions{BaseURL: srv.URL, UserID: "1000000000000001", Tokens: staticToken{}, HTTP: srv.Client(), Timeout: 50 * time.Millisecond, Wait: func(context.Context, time.Duration) error { return nil }})
	_, err := api.Post(context.Background(), "x", "")
	if !errors.Is(err, ErrUnknownOutcome) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestAPIErrorsCarryNoBodyOrToken(t *testing.T) {
	f, api, _ := newFake(t)
	f.route("GET", "/keyword_search", "error.json")
	_, err := api.Search(context.Background(), "q", false)
	if !errors.Is(err, ErrAPI) || Reason(err) != "api_error" {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "Invalid OAuth") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaks response: %v", err)
	}
}

func TestNewAPIRequiresInputs(t *testing.T) {
	if _, err := NewAPI(APIOptions{}); err == nil {
		t.Fatal("empty options accepted")
	}
}

func TestAPIRecentSearchLooksBackOneHour(t *testing.T) {
	f, api, _ := newFake(t)
	api.o.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	f.route("GET", "/keyword_search", "keyword_search.json")
	if _, err := api.Search(context.Background(), "go", true); err != nil {
		t.Fatal(err)
	}
	if q := f.queries["GET /keyword_search"]; q["search_type"] != "RECENT" || q["since"] != "1799996400" {
		t.Fatalf("recent query=%v", q)
	}
}

func TestAPIProfileLookupKeepsAbsentFollowersNil(t *testing.T) {
	f, api, _ := newFake(t)
	f.route("GET", "/profile_lookup", `{"username":"big_one","follower_count":4200,"views_count":9,"is_verified":true}`)
	p, err := api.ProfileLookup(context.Background(), "big_one")
	if err != nil || p.Followers == nil || *p.Followers != 4200 || !p.Verified || f.queries["GET /profile_lookup"]["username"] != "big_one" {
		t.Fatalf("profile=%+v err=%v", p, err)
	}
	f.route("GET", "/profile_lookup", `{"username":"hidden"}`)
	if p, err = api.ProfileLookup(context.Background(), "hidden"); err != nil || p.Followers != nil {
		t.Fatalf("absent followers must stay nil: %+v err=%v", p, err)
	}
}

func TestAPISeriesPartRepliesToThePreviousPart(t *testing.T) {
	f, api, _ := newFake(t)
	f.route("POST", "/1000000000000001/threads", "container.json")
	f.route("GET", "/3000000000000001", "status_finished.json")
	f.route("POST", "/1000000000000001/threads_publish", "publish.json")
	f.route("GET", "/2000000000000001", "post_permalink.json")
	first, err := api.Post(context.Background(), "hook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.Reply(context.Background(), first.ID, "part two"); err != nil {
		t.Fatal(err)
	}
	if q := f.queries["POST /1000000000000001/threads"]; q["reply_to_id"] != first.ID || q["text"] != "part two" {
		t.Fatalf("part query=%v", q)
	}
}

func TestAPISearchEmptyIsLimitedAccess(t *testing.T) {
	f, api, _ := newFake(t)
	f.route("GET", "/keyword_search", `{"data":[]}`)
	page, err := api.Search(context.Background(), "Jensen Huang", false)
	if err != nil || len(page.Posts) != 0 || !strings.Contains(page.LimitedAccess, "App Review") {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestAPIProfilePosts(t *testing.T) {
	f, api, _ := newFake(t)
	ctx := context.Background()
	f.route("GET", "/profile_posts", "keyword_search.json")
	page, err := api.ProfilePosts(ctx, "synthetic_author")
	if err != nil || page.Kind != "profile_posts" || len(page.Posts) != 1 || page.Posts[0].ID == "" || page.LimitedAccess != "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if q := f.queries["GET /profile_posts"]; q["username"] != "synthetic_author" {
		t.Fatalf("query=%v", q)
	}
	// A refused profile is a limited_access page, not an error.
	delete(f.routes, "GET /profile_posts")
	page, err = api.ProfilePosts(ctx, "someone")
	if err != nil || len(page.Posts) != 0 || !strings.Contains(page.LimitedAccess, "threads_profile_discovery") {
		t.Fatalf("refused page=%+v err=%v", page, err)
	}
}
