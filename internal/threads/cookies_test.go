package threads

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agentbrowser"
)

var cookieNow = time.Unix(1700000000, 0)

func cookieFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func byName(cs []agentbrowser.Cookie) map[string]agentbrowser.Cookie {
	m := map[string]agentbrowser.Cookie{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

func TestParseCookieJSONExport(t *testing.T) {
	cs, status := ParseCookies(cookieFixture(t, "cookies-export.json"), mustBase("https://www.threads.com"), cookieNow)
	if status != "" || len(cs) != 4 {
		t.Fatalf("status %q, %d cookies", status, len(cs))
	}
	m := byName(cs)
	if _, ok := m["tracker"]; ok {
		t.Fatal("foreign domain kept")
	}
	s := m["sessionid"]
	if s.Domain != ".threads.com" || !s.HTTPOnly || !s.Secure || s.SameSite != "None" || s.Expires != 1893456000 || s.Value != "synthetic-session-value" {
		t.Fatalf("sessionid %+v", s)
	}
	if m["csrftoken"].SameSite != "Lax" || m["csrftoken"].Expires != 0 || m["mid"].SameSite != "Strict" || m["ds_user_id"].SameSite != "" {
		t.Fatalf("mapping %+v", m)
	}
}

func TestParseCookieHeader(t *testing.T) {
	cs, status := ParseCookies(cookieFixture(t, "cookies-header.txt"), mustBase("https://www.threads.com"), cookieNow)
	if status != "" || len(cs) != 5 {
		t.Fatalf("status %q, %d cookies", status, len(cs))
	}
	m := byName(cs)
	if m["rur"].Value != "SYN,000000000000001,1893456000:synthetic" {
		t.Fatalf("escapes not decoded: %q", m["rur"].Value)
	}
	if !m["sessionid"].HTTPOnly || !m["ps_l"].HTTPOnly || m["csrftoken"].HTTPOnly {
		t.Fatalf("httpOnly %+v", m)
	}
	c := m["sessionid"]
	if c.Domain != ".threads.com" || c.Path != "/" || !c.Secure || c.Expires != cookieNow.Add(365*24*time.Hour).Unix() {
		t.Fatalf("defaults %+v", c)
	}
}

func TestParseCookiesRefusals(t *testing.T) {
	base := mustBase("https://www.threads.com")
	for raw, want := range map[string]string{
		"sessionid=x; csrftoken=y": CookiesNoSession,
		"ds_user_id=1":             CookiesNoSession,
		`[{"domain":".threads.com","name":"sessionid","value":"a"}]`: CookiesNoSession,
		"":          CookiesBad,
		"[not json": CookiesBad,
		`[{"domain":".example.org","name":"sessionid","value":"a"}]`: CookiesBad,
	} {
		if _, got := ParseCookies(raw, base, cookieNow); got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}

func TestImportCookiesVerifiesTheFeed(t *testing.T) {
	cookies, _ := ParseCookies(cookieFixture(t, "cookies-header.txt"), mustBase("https://www.threads.com"), cookieNow)
	base := "https://www.threads.com"
	cli := &fakeAgentCLI{openURL: base + "/", snapshot: agentbrowser.Snapshot{Origin: base + "/", Text: liveFixture(t, "feed.ax.txt")}}
	if got := importCookies(context.Background(), testAgentBrowser(t, cli, nil), cookies); got != CookiesOK || len(cli.cookies) != 5 {
		t.Fatalf("%q, %d cookies set", got, len(cli.cookies))
	}
	cli = &fakeAgentCLI{openURL: base + "/", snapshot: agentbrowser.Snapshot{Origin: base + "/", Text: liveFixture(t, "logged-out.ax.txt")}}
	if got := importCookies(context.Background(), testAgentBrowser(t, cli, nil), cookies); got != CookiesLoggedOut {
		t.Fatalf("expired cookies: %q", got)
	}
	cli = &fakeAgentCLI{openURL: base + "/", cookieErr: agentbrowser.ErrCommand}
	if got := importCookies(context.Background(), testAgentBrowser(t, cli, nil), cookies); got != CookiesFailed || strings.Contains(got, "synthetic") {
		t.Fatalf("failed write: %q", got)
	}
}
