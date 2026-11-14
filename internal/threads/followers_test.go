package threads

import (
	"context"
	"errors"
	"testing"
)

func TestParseFollowers(t *testing.T) {
	for label, want := range map[string]int64{
		"A Threads user's profile picture 1 follower": 1,
		"42 followers":          42,
		"1.2K followers":        1200,
		"1,5k followers":        1500,
		"3M followers":          3000000,
		"1,234 followers":       1234,
		"1.234.567 followers":   1234567,
		"7 người theo dõi":      7,
		"1,2K người theo dõi":   1200,
		"12.345 người theo dõi": 12345,
	} {
		if got := parseFollowers(label); got == nil || *got != want {
			t.Errorf("%q = %v, want %d", label, got, want)
		}
	}
	for _, label := range []string{" follower", "Net followers", " followers", "Followers", "Follow", ""} {
		if got := parseFollowers(label); got != nil {
			t.Errorf("%q = %d, want none", label, *got)
		}
	}
}

func TestThreadsFollowersTakesTheFirstCountInTheTree(t *testing.T) {
	tree, err := parseAXTree("- link \"A Threads user's profile picture 1 follower\"\n- text \"Net followers\"\n- button \"9 followers\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := threadsFollowers(tree); got == nil || *got != 1 {
		t.Fatalf("followers = %v", got)
	}
}

func TestLiveReadOwnMetricsPrefersTheProfilePageCount(t *testing.T) {
	for name, tc := range map[string]struct {
		driver *fakeDriver
		user   string
		want   int64
		source string
	}{
		"page":        {&fakeDriver{read: Page{Kind: "profile", Followers: n64(1)}}, "me", 1, FollowersFromBrowser},
		"no count":    {&fakeDriver{read: Page{Kind: "profile"}}, "me", 0, FollowersFromAPI},
		"read failed": {&fakeDriver{pageErr: ErrLoggedOut}, "me", 0, FollowersFromAPI},
		"no username": {&fakeDriver{read: Page{Kind: "profile", Followers: n64(1)}}, "", 0, FollowersFromAPI},
	} {
		t.Run(name, func(t *testing.T) {
			l, fake := liveWith(t, tc.driver)
			l.username = tc.user
			fake.route("GET", "/1000000000000001/threads_insights", "account_insights.json", "followers.json")
			m, err := l.ReadOwnMetrics(context.Background())
			if err != nil || m.Followers == nil || *m.Followers != tc.want || m.FollowersSource != tc.source || m.Views != 1204 {
				t.Fatalf("metrics=%+v err=%v", m, err)
			}
			if tc.user != "" && (len(tc.driver.destinations) != 1 || tc.driver.destinations[0] != "https://fixture.test/@me") {
				t.Fatalf("navigated to %v", tc.driver.destinations)
			}
		})
	}
}

func TestLiveReadOwnMetricsKeepsTheAPIErrorAndNeverOpensTheBrowser(t *testing.T) {
	opened := 0
	_, api, _ := newFake(t) // no insights route: the API read fails
	l := &Live{API: api, username: "me", openBrowser: func(context.Context) (*Browser, error) { opened++; return nil, errors.New("boom") }}
	if _, err := l.ReadOwnMetrics(context.Background()); err == nil || opened != 0 {
		t.Fatalf("err=%v opened=%d", err, opened)
	}
}

func TestParseThreadsPageProfileReadsTheCountOfAnAuthenticatedProfile(t *testing.T) {
	const base = "https://www.threads.com"
	nav := "- navigation\n  - link \"Profile Profile\" [ref=e1, url=https://www.threads.com/@me]\n  - link \"Insights Insights\" [ref=e2, url=https://www.threads.com/insights/]\n"
	page, err := parseThreadsPage(nav+"- link \"A Threads user's profile picture 1 follower\" [ref=e3, url=https://www.threads.com/@me]\n", base+"/@me", "profile", base)
	if err != nil || page.Followers == nil || *page.Followers != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if page, err = parseThreadsPage(nav+"- text \"Net followers\"\n", base+"/@me", "profile", base); err != nil || page.Followers != nil {
		t.Fatalf("a page without a count: %+v err=%v", page, err)
	}
	if _, err = parseThreadsPage(nav, base+"/search", "profile", base); !errors.Is(err, ErrUnknownPage) {
		t.Fatalf("a non-profile path: %v", err)
	}
}
