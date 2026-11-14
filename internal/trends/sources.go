package trends

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/config"
)

// SourceOptions bounds one fetch_trends read over the template's sources. Sources are independent: a
// failing source is reported and skipped, never turning a partial read into a
// total failure.
type SourceOptions struct {
	HTTPClient        *http.Client
	Timeout           time.Duration
	MaxItemsPerSource int
	Sources           []string
	Queries           []string
	// URLs overrides canonical endpoints for offline fixtures, keyed by source.
	URLs map[string]string
}

// SourceFeed is the fail-soft result of one fetch.
type SourceFeed struct {
	Items  []Trend  `json:"items"`
	Failed []string `json:"failed_sources,omitempty"`
}

const (
	sourceTimeout        = 10 * time.Second
	sourceOverallTimeout = 45 * time.Second
	sourceMaxItems       = 8
	sourceWorkers        = 4
	sourceUserAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
)

var sourceDefaultURLs = map[string]string{
	"google_news_vn":     "https://news.google.com/rss?hl=vi&gl=VN&ceid=VN:vi",
	"google_news_global": "https://news.google.com/rss?hl=en-US&gl=US&ceid=US:en",
	"vnexpress":          "https://vnexpress.net/rss/so-hoa.rss",
	"genk":               "https://genk.vn/rss/home.rss",
	"tinhte":             "https://tinhte.vn/rss/",
	"vietnamnet":         "https://vietnamnet.vn/rss/cong-nghe.rss",
	"the_verge":          "https://www.theverge.com/rss/index.xml",
	"techcrunch":         "https://techcrunch.com/feed/",
	"arstechnica":        "https://feeds.arstechnica.com/arstechnica/index",
	"hacker_news":        "https://hacker-news.firebaseio.com/v0",
	"product_hunt":       "https://www.producthunt.com/feed",
	"google_trends":      "https://trends.google.com/trending/rss?geo=VN",
	"github_trending":    "https://github.com/trending?since=daily",
}

// SourcesFromTemplate resolves the template's fetch_trends sources and queries.
func SourcesFromTemplate(tmpl config.Template) SourceOptions {
	sources := append([]string{}, tmpl.Trends.Sources...)
	include, _ := tmpl.TrendKeywords()
	queries := include
	if len(queries) > 3 {
		queries = queries[:3]
	}
	return SourceOptions{
		Timeout:           sourceTimeout,
		MaxItemsPerSource: sourceMaxItems,
		Sources:           sources,
		Queries:           queries,
	}
}

type sourceResult struct {
	source string
	items  []Trend
	failed bool
}

// FetchSources fetches every enabled source with a per-source deadline and
// a shared overall deadline. Results keep canonical source order.
func FetchSources(ctx context.Context, opts SourceOptions) (SourceFeed, error) {
	if ctx == nil {
		return SourceFeed{}, errors.New("fetch sources: nil context")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = sourceTimeout
	}
	if opts.MaxItemsPerSource <= 0 || opts.MaxItemsPerSource > 50 {
		opts.MaxItemsPerSource = sourceMaxItems
	}
	ctx, cancel := context.WithTimeout(ctx, sourceOverallTimeout)
	defer cancel()
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	endpoint := func(id string) string {
		if raw := strings.TrimSpace(opts.URLs[id]); raw != "" {
			return raw
		}
		if id == "google_news_topics" {
			return ""
		}
		return sourceDefaultURLs[id]
	}
	sources := append([]string{}, opts.Sources...)
	results := make([]sourceResult, len(sources))
	sem := make(chan struct{}, sourceWorkers)
	var wg sync.WaitGroup
	for index, source := range sources {
		results[index].source = source
		wg.Add(1)
		go func(index int, source string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			items, err := fetchSource(ctx, client, opts, source, endpoint(source), timeout)
			switch {
			case err != nil:
				results[index].failed = true
			default:
				results[index].items = items
			}
		}(index, source)
	}
	wg.Wait()
	feed := SourceFeed{Items: []Trend{}, Failed: []string{}}
	for _, result := range results {
		switch {
		case result.failed:
			feed.Failed = append(feed.Failed, result.source)
		default:
			feed.Items = append(feed.Items, result.items...)
		}
	}
	if len(feed.Items) == 0 && len(feed.Failed) == len(sources) {
		return feed, errors.New("fetch sources: every source failed")
	}
	return feed, nil
}

func fetchSource(ctx context.Context, client *http.Client, opts SourceOptions, source, endpoint string, timeout time.Duration) ([]Trend, error) {
	sourceCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch source {
	case "hacker_news":
		items, err := fetchHackerNews(sourceCtx, client, endpoint, opts.MaxItemsPerSource)
		return items, err
	case "github_trending":
		items, err := fetchGitHubTrending(sourceCtx, client, endpoint, opts.MaxItemsPerSource)
		return items, err
	case "google_news_topics":
		var all []Trend
		if len(opts.Queries) == 0 {
			opts.Queries = []string{"AI"}
		}
		global := opts.URLs["google_news_global"] != ""
		for _, query := range opts.Queries {
			if len(all) >= opts.MaxItemsPerSource {
				break
			}
			raw := opts.URLs["google_news_topics"]
			if raw == "" {
				raw = "https://news.google.com/rss/search?q=" + url.QueryEscape(query) + "&hl=vi&gl=VN&ceid=VN:vi"
				if global {
					raw = "https://news.google.com/rss/search?q=" + url.QueryEscape(query) + "&hl=en-US&gl=US&ceid=US:en"
				}
			}
			items, err := fetchSourceRSS(sourceCtx, client, raw, source, opts.MaxItemsPerSource-len(all))
			if err != nil {
				if len(all) == 0 {
					return nil, err
				}
				break
			}
			all = append(all, items...)
			if opts.URLs["google_news_topics"] != "" {
				break
			}
		}
		return all, nil
	default:
		if endpoint == "" {
			return nil, errors.New("fetch source: no endpoint")
		}
		items, err := fetchSourceRSS(sourceCtx, client, endpoint, source, opts.MaxItemsPerSource)
		return items, err
	}
}

func fetchSourceRSS(ctx context.Context, client *http.Client, endpoint, source string, limit int) ([]Trend, error) {
	body, err := fetchSourceBody(ctx, client, endpoint)
	if err != nil {
		return nil, err
	}
	records, err := parseTrendFeed(body)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", source, err)
	}
	trends := make([]Trend, 0, limit)
	for _, record := range records {
		title := strings.TrimSpace(html.UnescapeString(record.title))
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

func fetchSourceBody(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("build request")
	}
	request.Header.Set("User-Agent", sourceUserAgent)
	request.Header.Set("Accept", "application/json, application/atom+xml, application/rss+xml, application/xml, text/xml, text/html;q=0.8, */*;q=0.1")
	response, err := client.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, errors.New("request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTrendResponseBytes+1))
	if err != nil {
		return nil, errors.New("read response body")
	}
	if len(body) > maxTrendResponseBytes {
		return nil, errors.New("response body too large")
	}
	return body, nil
}

var githubArticle = regexp.MustCompile(`(?s)<article[^>]*>(.*?)</article>`)
var githubRepo = regexp.MustCompile(`href="/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)"`)
var githubDescription = regexp.MustCompile(`(?s)<p[^>]*class="[^"]*col-9[^"]*"[^>]*>(.*?)</p>`)
var htmlTags = regexp.MustCompile(`<[^>]*>`)

// fetchGitHubTrending parses the public trending page. Only repository links,
// names and descriptions are read; no selector or script is guessed beyond the
// measured page shape, and malformed blocks are skipped.
func fetchGitHubTrending(ctx context.Context, client *http.Client, endpoint string, limit int) ([]Trend, error) {
	body, err := fetchSourceBody(ctx, client, endpoint)
	if err != nil {
		return nil, err
	}
	blocks := githubArticle.FindAllStringSubmatch(string(body), -1)
	trends := make([]Trend, 0, limit)
	seen := map[string]bool{}
	for _, block := range blocks {
		match := githubRepo.FindStringSubmatch(block[1])
		if match == nil {
			continue
		}
		owner, repo := match[1], match[2]
		if owner == "login" || owner == "sponsors" || seen[owner+"/"+repo] {
			continue
		}
		seen[owner+"/"+repo] = true
		description := ""
		if desc := githubDescription.FindStringSubmatch(block[1]); desc != nil {
			description = strings.TrimSpace(html.UnescapeString(htmlTags.ReplaceAllString(desc[1], " ")))
		}
		title := owner + "/" + repo
		if description != "" {
			title += ": " + description
		}
		trends = append(trends, Trend{Source: "github_trending", Title: title, URL: "https://github.com/" + owner + "/" + repo})
		if len(trends) == limit {
			break
		}
	}
	if len(trends) == 0 {
		return nil, errors.New("decode github trending: no repositories")
	}
	return trends, nil
}
