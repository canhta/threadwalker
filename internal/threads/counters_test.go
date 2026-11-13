package threads

import "testing"

func TestParseCount(t *testing.T) {
	for text, want := range map[string]int64{
		"0": 0, "83": 83, "1,234": 1234, "1.234": 1234, "1.234.567": 1234567,
		"6.5K": 6500, "1,2K": 1200, "2K": 2000, "2K posts": 2000, "1.2M": 1200000, "1,2 N": 1200,
		"3 Tr bài viết": 3000000, "2 posts": 2, "12 N": 12000, "2.1k": 2100,
	} {
		got := parseCount(text)
		if got == nil || *got != want {
			t.Errorf("%q = %v, want %d", text, got, want)
		}
	}
	for _, text := range []string{"", "K", "posts", "1,2", "1.23", "1.2.3K", "9999999999999999999", "-5"} {
		if got := parseCount(text); got != nil {
			t.Errorf("%q = %d, want nil", text, *got)
		}
	}
}

func TestParseAge(t *testing.T) {
	for text, want := range map[string]int64{
		"45m": 45, "2h": 120, "3d": 4320, "1w": 10080, "45 minutes ago": 45, "1 hour ago": 60,
		"about an hour ago": 60, "a day ago": 1440, "just now": 0, "15 phút": 15, "2 giờ trước": 120,
		"3 ngày": 4320, "1 tuần": 10080, "2 hours ago": 120,
	} {
		got := parseAge(text)
		if got == nil || *got != want {
			t.Errorf("%q = %v, want %d", text, got, want)
		}
	}
	for _, text := range []string{"", "yesterday", "5 fortnights", "Follow"} {
		if got := parseAge(text); got != nil {
			t.Errorf("%q = %d, want nil", text, *got)
		}
	}
}

func val(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestLiveFixtureCounters(t *testing.T) {
	base := "https://www.threads.com"
	type want struct{ likes, replies, reposts, age int64 }
	check := func(name, kind, path string, wants ...want) {
		t.Helper()
		page, err := parseThreadsPage(liveFixture(t, name), base+path, kind, base)
		if err != nil || len(page.Posts) < len(wants) {
			t.Fatalf("%s: %d posts, %v", name, len(page.Posts), err)
		}
		for i, w := range wants {
			p := page.Posts[i]
			if got := (want{val(p.Likes), val(p.Replies), val(p.Reposts), val(p.AgeMinutes)}); got != w {
				t.Errorf("%s post %d (%s): %+v, want %+v", name, i, p.URL, got, w)
			}
		}
	}
	// -1: the snapshot does not show the figure.
	check("feed.ax.txt", "feed", "/", want{12, 3, 2, 120}, want{1, 0, 0, 180})
	check("search-page.ax.txt", "topic", "/search?q=x", want{1, -1, -1, 60}, want{2, -1, -1, 60}, want{3, -1, -1, 120})
	check("topic.ax.txt", "topic", "/search?q=x", want{278, -1, -1, 60}, want{-1, -1, -1, 120})
	check("post-real.ax.txt", "post", "/@synthetic_author/post/fixtureRealPost1", want{2, 0, 0, 45})
	check("post-tagged.ax.txt", "post", "/@synthetic_author/post/fixtureTaggedPost", want{305, 0, 5, 960})
	check("post-replies.ax.txt", "post", "/@synthetic_author/post/fixtureRealPost2", want{6500, 83, 163, 1380})
	check("home-trending.ax.txt", "feed", "/", want{0, -1, -1, 19})
}

func TestLiveFixtureTrendPostCount(t *testing.T) {
	base := "https://www.threads.com"
	feed, err := parseThreadsPage(liveFixture(t, "home-trending.ax.txt"), base+"/", "feed", base)
	if err != nil || len(feed.Trends) != 2 {
		t.Fatalf("%+v %v", feed, err)
	}
	if val(feed.Trends[0].PostCount) != 2000 || val(feed.Trends[1].PostCount) != 9000 {
		t.Fatalf("%+v", feed.Trends)
	}
}
