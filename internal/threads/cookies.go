package threads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/agentbrowser"
)

// What a cookie import ended in. They are also the reasons the owner is told.
const (
	CookiesOK = "ok"
	// CookiesLoggedOut means the cookies were set but the feed still shows the
	// logged-out page: they are expired or not from a logged-in browser.
	CookiesLoggedOut = "logged_out"
	CookiesCaptcha   = "captcha"
	// CookiesNoSession means sessionid or ds_user_id is absent.
	CookiesNoSession = "missing_sessionid"
	CookiesBad       = "bad_cookies"
	CookiesFailed    = "failed"
)

const (
	maxCookies     = 100
	maxCookieBytes = 256 << 10
)

// ParseCookies reads the owner's cookies: a JSON export (a Cookie-Editor style
// array, or an object holding "cookies") or a Cookie header string. Only
// threads.com, threads.net and instagram.com cookies are kept. Errors name the
// kind of problem, never a cookie name or value.
func ParseCookies(raw string, base *url.URL, now time.Time) ([]agentbrowser.Cookie, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxCookieBytes {
		return nil, CookiesBad
	}
	var cookies []agentbrowser.Cookie
	if raw[0] == '[' || raw[0] == '{' {
		var ok bool
		if cookies, ok = parseCookieJSON(raw); !ok {
			return nil, CookiesBad
		}
	} else {
		cookies = parseCookieHeader(raw, base, now)
	}
	if len(cookies) == 0 || len(cookies) > maxCookies {
		return nil, CookiesBad
	}
	have := map[string]bool{}
	for _, c := range cookies {
		if c.Value != "" {
			have[c.Name] = true
		}
	}
	if !have["sessionid"] || !have["ds_user_id"] {
		return nil, CookiesNoSession
	}
	return cookies, ""
}

func allowedCookieDomain(d string) bool {
	d = strings.ToLower(strings.TrimPrefix(d, "."))
	for _, s := range []string{"threads.com", "threads.net", "instagram.com"} {
		if d == s || strings.HasSuffix(d, "."+s) {
			return true
		}
	}
	return false
}

type jsonCookie struct {
	Domain         string   `json:"domain"`
	ExpirationDate *float64 `json:"expirationDate"`
	HTTPOnly       bool     `json:"httpOnly"`
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	SameSite       *string  `json:"sameSite"`
	Secure         bool     `json:"secure"`
	Session        bool     `json:"session"`
	Value          string   `json:"value"`
}

func parseCookieJSON(raw string) ([]agentbrowser.Cookie, bool) {
	var list []jsonCookie
	data := []byte(raw)
	if raw[0] == '{' {
		var wrap struct {
			Cookies []jsonCookie `json:"cookies"`
		}
		if json.Unmarshal(data, &wrap) != nil {
			return nil, false
		}
		list = wrap.Cookies
	} else if json.Unmarshal(bytes.TrimSpace(data), &list) != nil {
		return nil, false
	}
	var out []agentbrowser.Cookie
	for _, j := range list {
		if !allowedCookieDomain(j.Domain) {
			continue
		}
		c := agentbrowser.Cookie{Name: j.Name, Value: j.Value, Domain: strings.ToLower(j.Domain), Path: j.Path, Secure: j.Secure, HTTPOnly: j.HTTPOnly}
		if c.Path == "" {
			c.Path = "/"
		}
		if j.SameSite != nil {
			switch strings.ToLower(*j.SameSite) {
			case "lax":
				c.SameSite = "Lax"
			case "strict":
				c.SameSite = "Strict"
			case "no_restriction", "none":
				c.SameSite = "None"
			}
		}
		if !j.Session && j.ExpirationDate != nil && *j.ExpirationDate > 0 {
			c.Expires = int64(*j.ExpirationDate)
		}
		out = append(out, c)
	}
	return out, true
}

// parseCookieHeader reads "name=value; name=value", with an optional "Cookie:"
// prefix. Values may be double-quoted and hold octal escapes such as \054.
func parseCookieHeader(raw string, base *url.URL, now time.Time) []agentbrowser.Cookie {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "Cookie:"))
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "cookie:"))
	domain := "." + strings.TrimPrefix(strings.ToLower(base.Hostname()), "www.")
	var out []agentbrowser.Cookie
	for _, part := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" {
			continue
		}
		out = append(out, agentbrowser.Cookie{
			Name: strings.TrimSpace(name), Value: unquoteCookie(value), Domain: domain, Path: "/", Secure: true,
			HTTPOnly: name == "sessionid" || strings.HasPrefix(name, "ps_"),
			Expires:  now.Add(365 * 24 * time.Hour).Unix(),
		})
	}
	return out
}

func unquoteCookie(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+4 <= len(v) {
			if n, err := strconv.ParseUint(v[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		if v[i] == '\\' && i+1 < len(v) {
			i++
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// ImportCookies writes the owner's cookies into the agent's profile through the
// agent-browser session a wake uses, reads the feed with the usual logged-out
// detection and closes the browser gracefully. It returns a Cookies* status.
func ImportCookies(ctx context.Context, opts AgentBrowserOptions, raw string) string {
	base, err := url.Parse(opts.BaseURL)
	if err != nil || base == nil {
		return CookiesFailed
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	cookies, status := ParseCookies(raw, base, now())
	if status != "" {
		return status
	}
	b, err := NewAgentBrowser(ctx, opts)
	if err != nil {
		return CookiesFailed
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = b.Close(closeCtx)
	}()
	return importCookies(ctx, b, cookies)
}

func importCookies(ctx context.Context, b *Browser, cookies []agentbrowser.Cookie) string {
	d, ok := b.driver.(*agentBrowserDriver)
	if !ok {
		return CookiesFailed
	}
	// Cookies written before any page has loaded do not survive, so load one first.
	if _, err := d.cli.Open(ctx, b.base.String()); err != nil {
		return CookiesFailed
	}
	for _, c := range cookies {
		if err := d.cli.SetCookie(ctx, c); err != nil {
			if errors.Is(err, agentbrowser.ErrUnsafe) {
				return CookiesBad
			}
			return CookiesFailed
		}
	}
	_, err := b.ReadFeed(ctx)
	switch Reason(err) {
	case "":
		return CookiesOK
	case ReasonLoggedOut:
		return CookiesLoggedOut
	case ReasonCaptcha:
		return CookiesCaptcha
	}
	return CookiesFailed
}
