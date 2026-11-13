package dashboard_test

import (
	"regexp"
	"strings"
	"testing"
)

func TestTimesCarryMachineTimeForTheBrowser(t *testing.T) {
	h := newHandler(newSource(measured()))
	rr := get(h, "/")
	page := rr.Body.String()
	if !strings.Contains(page, `<time datetime="2026-11-02T12:00:00Z" data-title="full"`) {
		t.Fatalf("post time not machine readable")
	}
	if !regexp.MustCompile(`<time datetime="[^"]+Z" data-fmt="clock">`).MatchString(page) {
		t.Fatal("stats read time not machine readable")
	}
	m := regexp.MustCompile(`<script src="(/local-time\.js\?v=[^"]+)" defer>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("page does not reference the script")
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "unsafe") {
		t.Fatalf("csp: %q", csp)
	}
	js := get(h, m[1])
	if js.Code != 200 || js.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || !strings.Contains(js.Body.String(), "Intl.DateTimeFormat") {
		t.Fatalf("script: %d %q", js.Code, js.Header().Get("Content-Type"))
	}
}
