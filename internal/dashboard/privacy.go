package dashboard

import (
	"encoding/json"
	"io"
	"net/url"
	"strings"
)

// The publication path must bind the selected own username. Queries, fragments,
// credentials, redirects, arbitrary hosts and encoded path tricks are refused.
func ownURL(raw, username string) string {
	username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	if username == "" || len(username) > 64 {
		return ""
	}
	for _, c := range username {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.') {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	switch strings.ToLower(u.Host) {
	case "threads.net", "www.threads.net", "threads.com", "www.threads.com":
	default:
		return ""
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 4 || parts[0] != "" || strings.ToLower(parts[1]) != "@"+username || parts[2] != "post" {
		return ""
	}
	if len(parts[3]) < 1 || len(parts[3]) > 64 {
		return ""
	}
	for _, c := range parts[3] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return ""
		}
	}
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

func toolSummary(raw string, blocked bool) (string, string) {
	if blocked {
		return "blocked", ""
	}
	if raw == "" {
		return "pending", ""
	}
	var value struct {
		Outcome string `json:"outcome"`
		Bytes   *int64 `json:"bytes"`
		Retry   *bool  `json:"retry"`
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&value) != nil {
		return "unknown", ""
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return "unknown", ""
	}
	outcome := oneOf(value.Outcome, "success", "error", "unknown_assumed_sent", "interrupted_read")
	return outcome, ""
}
