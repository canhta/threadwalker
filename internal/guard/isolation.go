package guard

import (
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/store"
)

// Deliberately local syntax: no redirect resolution or network access. Query
// values are retained, fragments discarded, and equivalent host/default ports,
// escaped paths, trailing slashes and query ordering share a promotion key.
var promotionURLs = regexp.MustCompile(`(?i)(?:https?://|www\.|(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}\b)[^\s<>"']*`)

func promotedLinks(text string) map[string]bool {
	out := map[string]bool{}
	for _, span := range promotionURLs.FindAllStringIndex(text, -1) {
		if span[0] > 0 && text[span[0]-1] == '@' {
			continue
		}
		raw := text[span[0]:span[1]]
		raw = strings.TrimRight(raw, ".,;!?)\u3002")
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		host = strings.TrimPrefix(host, "www.")
		port := u.Port()
		if port != "" && port != "80" && port != "443" {
			host += ":" + port
		}
		// HTTP and HTTPS links identify the same resource for this refusal policy.
		u.Scheme = "https"
		u.Host = host
		u.Fragment = ""
		u.RawFragment = ""
		u.RawPath = ""
		u.Path = strings.TrimRight(path.Clean("/"+strings.TrimPrefix(u.Path, "/")), "/")
		u.RawQuery = u.Query().Encode()
		u.ForceQuery = false
		out[u.String()] = true
	}
	return out
}

func (g *Guard) ownURL(raw string) bool {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	_, known := stdx.ThreadsHost(host)
	if base, e := url.Parse(g.opts.Template.Threads.BaseURL); e == nil && base.Hostname() != "" {
		known = known || strings.EqualFold(host, strings.TrimPrefix(base.Hostname(), "www."))
	}
	if !known {
		return false
	}
	first := strings.Split(strings.TrimPrefix(path.Clean(u.Path), "/"), "/")[0]
	user, e := store.NormalizeUsername(first)
	return e == nil && g.own[user]
}

func (g *Guard) ownInteraction(text string) bool {
	for _, m := range mentions.FindAllString(text, -1) {
		user, e := store.NormalizeUsername(m)
		if e == nil && g.own[user] {
			return true
		}
	}
	for link := range promotedLinks(text) {
		if g.ownURL(link) {
			return true
		}
	}
	return false
}

// Check every prior reservation inside the same write transaction as the new
// intent. Failed, uncertain and dry-run publications all retain their claim.
func (g *Guard) isolation(actions []store.ReservedAction, intent *actionIntent) string {
	links := promotedLinks(intent.Text)
	if len(links) == 0 && intent.TargetURL == "" {
		return ""
	}
	for _, row := range actions {
		if row.ThreadURL != "" {
			for own := range publicationKeys(row.ThreadURL) {
				if publicationKeys(intent.TargetURL)[own] {
					return "own_account"
				}
				for outbound := range links {
					if publicationKeys(outbound)[own] {
						return "own_account"
					}
				}
			}
		}
		if row.Agent == g.opts.Template.ID {
			continue
		}
		for link := range promotedLinks(row.Text) {
			if links[link] {
				return "shared_promotion_link"
			}
		}
	}
	return ""
}

func publicationKeys(raw string) map[string]bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	u.RawQuery = ""
	u.ForceQuery = false
	if host, ok := stdx.ThreadsHost(u.Hostname()); ok {
		u.Host = host
	}
	return promotedLinks(u.String())
}
