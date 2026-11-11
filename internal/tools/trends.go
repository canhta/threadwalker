// Package tools holds the agent's tool definitions and dispatcher.
//
// trends.go implements fetch_trends. Titles and URLs returned here are
// untrusted external data: the dispatcher must wrap them before the model sees
// them. This file never logs titles, URLs or response bodies.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Trend is one item from an external trend source. Score is source-native and
// currently only populated for Hacker News.
type Trend struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Score  int64  `json:"score,omitempty"`
}

// TrendsOptions configures FetchTrends. Zero values select the documented
// public endpoints, a 15-second overall deadline and 20 total items.
//
// MaxItems bounds the total number of trends returned. Each source is also
// independently capped at 10 items (and at MaxItems), so one source cannot
// exhaust the whole budget. Results are merged round-robin in source order:
// Hacker News, Product Hunt and Google Trends VN. This ordering is
// deterministic for a given set of responses.
type TrendsOptions struct {
	HTTPClient      *http.Client
	Timeout         time.Duration
	MaxItems        int
	HNBaseURL       string
	ProductHuntURL  string
	GoogleTrendsURL string
}

const (
	sourceHackerNews   = "hacker_news"
	sourceProductHunt  = "product_hunt"
	sourceGoogleTrends = "google_trends"

	defaultHNBaseURL       = "https://hacker-news.firebaseio.com/v0"
	defaultProductHuntURL  = "https://www.producthunt.com/feed"
	defaultGoogleTrendsURL = "https://trends.google.com/trending/rss?geo=VN"

	defaultTrendsTimeout   = 15 * time.Second
	defaultMaxTrendItems   = 20
	maxTrendItemsPerSource = 10
	maxTrendResponseBytes  = 1 << 20 // 1 MiB per response
)

// FetchTrends fetches the three configured sources without retries.
//
// The policy is fail-closed: if any configured source fails, the call returns an
// error rather than a partial result that could look like complete success.
// Deleted/dead Hacker News items and feed entries missing a title or link are
// skipped as source data, not treated as transport or parse failures.
//
// Every request inherits the caller's context and an overall timeout. Errors
// name the operation and HTTP status only; endpoint URLs, response bodies and
// credentials never enter diagnostics.
func FetchTrends(ctx context.Context, opts TrendsOptions) ([]Trend, error) {
	if ctx == nil {
		return nil, fmt.Errorf("fetch trends: nil context")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTrendsTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	maxItems := opts.MaxItems
	if maxItems <= 0 {
		maxItems = defaultMaxTrendItems
	}
	perSource := maxItems
	if perSource > maxTrendItemsPerSource {
		perSource = maxTrendItemsPerSource
	}

	hnBaseURL := firstNonEmpty(opts.HNBaseURL, defaultHNBaseURL)
	productHuntURL := firstNonEmpty(opts.ProductHuntURL, defaultProductHuntURL)
	googleTrendsURL := firstNonEmpty(opts.GoogleTrendsURL, defaultGoogleTrendsURL)

	hn, err := fetchHackerNews(ctx, client, hnBaseURL, perSource)
	if err != nil {
		return nil, err
	}
	productHunt, err := fetchRSSFeed(ctx, client, productHuntURL, sourceProductHunt, "product hunt feed", perSource)
	if err != nil {
		return nil, err
	}
	googleTrends, err := fetchRSSFeed(ctx, client, googleTrendsURL, sourceGoogleTrends, "google trends feed", perSource)
	if err != nil {
		return nil, err
	}

	return mergeTrends(maxItems, hn, productHunt, googleTrends), nil
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// mergeTrends interleaves source slices so every source is represented while
// the total stays bounded and ordering stays deterministic.
func mergeTrends(limit int, groups ...[]Trend) []Trend {
	merged := make([]Trend, 0, limit)
	for index := 0; len(merged) < limit; index++ {
		added := false
		for _, group := range groups {
			if index >= len(group) {
				continue
			}
			merged = append(merged, group[index])
			added = true
			if len(merged) == limit {
				return merged
			}
		}
		if !added {
			break
		}
	}
	return merged
}

type hackerNewsItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Score   int64  `json:"score"`
	Deleted bool   `json:"deleted"`
	Dead    bool   `json:"dead"`
}

func fetchHackerNews(ctx context.Context, client *http.Client, baseURL string, limit int) ([]Trend, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	body, err := fetchTrendBody(ctx, client, baseURL+"/topstories.json", "hacker news top stories")
	if err != nil {
		return nil, err
	}

	var ids []int64
	if err := json.Unmarshal(body, &ids); err != nil {
		return nil, fmt.Errorf("decode hacker news top stories: %w", err)
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}

	trends := make([]Trend, 0, len(ids))
	for _, id := range ids {
		itemBody, err := fetchTrendBody(ctx, client, baseURL+"/item/"+strconv.FormatInt(id, 10)+".json", fmt.Sprintf("hacker news item %d", id))
		if err != nil {
			return nil, err
		}
		var item *hackerNewsItem
		if err := json.Unmarshal(itemBody, &item); err != nil {
			return nil, fmt.Errorf("decode hacker news item %d: %w", id, err)
		}
		if item == nil || item.Deleted || item.Dead {
			continue
		}
		title := strings.TrimSpace(html.UnescapeString(item.Title))
		if title == "" {
			continue
		}
		link := strings.TrimSpace(item.URL)
		if link == "" {
			link = "https://news.ycombinator.com/item?id=" + strconv.FormatInt(id, 10)
		}
		trends = append(trends, Trend{
			Source: sourceHackerNews,
			Title:  title,
			URL:    link,
			Score:  item.Score,
		})
	}
	return trends, nil
}

type feedDocument struct {
	XMLName xml.Name
	Channel struct {
		Items []feedItem `xml:"item"`
	} `xml:"channel"`
	Entries []feedEntry `xml:"entry"`
	Items   []feedItem  `xml:"item"` // RSS 1.0/RDF puts items at the root.
}

type feedItem struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

type feedEntry struct {
	Title string     `xml:"title"`
	Links []feedLink `xml:"link"`
}

type feedLink struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type feedRecord struct {
	title string
	link  string
}

func fetchRSSFeed(ctx context.Context, client *http.Client, endpoint, source, operation string, limit int) ([]Trend, error) {
	body, err := fetchTrendBody(ctx, client, endpoint, operation)
	if err != nil {
		return nil, err
	}
	records, err := parseTrendFeed(body)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", operation, err)
	}

	trends := make([]Trend, 0, limit)
	for _, record := range records {
		title := strings.TrimSpace(record.title)
		link := strings.TrimSpace(record.link)
		if title == "" || link == "" {
			continue
		}
		trends = append(trends, Trend{Source: source, Title: title, URL: link})
		if len(trends) == limit {
			break
		}
	}
	return trends, nil
}

// parseTrendFeed accepts RSS 2.0, Atom and RSS 1.0/RDF. XML entities and CDATA
// are decoded by encoding/xml; unknown roots fail closed because an HTML error
// page served with HTTP 200 is not a successful empty feed.
func parseTrendFeed(body []byte) ([]feedRecord, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.Strict = true
	decoder.Entity = xml.HTMLEntity

	var document feedDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}

	switch document.XMLName.Local {
	case "rss":
		return recordsFromItems(document.Channel.Items), nil
	case "feed":
		records := make([]feedRecord, 0, len(document.Entries))
		for _, entry := range document.Entries {
			records = append(records, feedRecord{
				title: entry.Title,
				link:  atomEntryLink(entry.Links),
			})
		}
		return records, nil
	case "RDF":
		return recordsFromItems(document.Items), nil
	default:
		return nil, fmt.Errorf("unsupported feed root")
	}
}

func recordsFromItems(items []feedItem) []feedRecord {
	records := make([]feedRecord, 0, len(items))
	for _, item := range items {
		records = append(records, feedRecord{title: item.Title, link: item.Link})
	}
	return records
}

// atomEntryLink prefers an alternate text/html link, then the first link with
// an href. Atom entries often carry several links (self, alternate, replies).
func atomEntryLink(links []feedLink) string {
	first := ""
	for _, link := range links {
		href := strings.TrimSpace(link.Href)
		if href == "" {
			continue
		}
		if first == "" {
			first = href
		}
		if strings.EqualFold(link.Rel, "alternate") &&
			(link.Type == "" || strings.HasPrefix(strings.ToLower(link.Type), "text/html")) {
			return href
		}
	}
	return first
}

func fetchTrendBody(ctx context.Context, client *http.Client, endpoint, operation string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build request", operation)
	}
	request.Header.Set("User-Agent", "threadwalker/trends")
	request.Header.Set("Accept", "application/json, application/atom+xml, application/rss+xml, application/xml, text/xml;q=0.9, */*;q=0.1")

	response, err := client.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, fmt.Errorf("%s: %w", operation, contextErr)
		}
		return nil, fmt.Errorf("%s: request failed", operation)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s: unexpected HTTP status %d", operation, response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxTrendResponseBytes+1))
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, fmt.Errorf("%s: %w", operation, contextErr)
		}
		return nil, fmt.Errorf("%s: read response body", operation)
	}
	if len(body) > maxTrendResponseBytes {
		return nil, fmt.Errorf("%s: response body exceeds %d bytes", operation, maxTrendResponseBytes)
	}
	return body, nil
}
