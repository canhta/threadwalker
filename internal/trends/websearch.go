package trends

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/store"
)

// WebSearchRequest is one bounded OpenAI Responses web-search call. It makes no
// retries, streams nothing and accepts only a small answer plus citations.
type WebSearchRequest struct {
	BaseURL    string
	Model      string
	APIKey     string
	Query      string
	MaxOutput  int
	MaxResults int
	Timeout    time.Duration
	// Country is an ISO 3166-1 alpha-2 code. The API localises results to the
	// United States when no location is sent, which is wrong for an agent that
	// writes about one country's news.
	Country string
}

// WebSource is one cited URL returned by the search provider.
type WebSource struct {
	Ref   string `json:"ref,omitempty"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// WebSearchResult carries the answer, bounded citations and reported usage.
type WebSearchResult struct {
	Answer  string
	Sources []WebSource
	Usage   store.LLMUsage
}

const webSearchMaxResponseBytes = 2 << 20

// webSearchMaxQueryRunes bounds the query. A long one is the agent pasting a
// whole URL into a site: filter, which the search has to interpret and which is
// what timed out; the tool's own filters are the way to narrow a search.
const webSearchMaxQueryRunes = 200

// WebSearcher runs one web-search request.
type WebSearcher func(context.Context, WebSearchRequest) (WebSearchResult, error)

// NewWebSearcher binds the injected client and refuses redirects. Each call
// performs exactly one OpenAI Responses web-search request; errors contain
// operation and HTTP status only, never a body or credential.
func NewWebSearcher(client *http.Client) WebSearcher {
	copied := *client
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return func(ctx context.Context, req WebSearchRequest) (WebSearchResult, error) {
		return searchWeb(ctx, &copied, req)
	}
}

func searchWeb(ctx context.Context, client *http.Client, req WebSearchRequest) (WebSearchResult, error) {
	if ctx == nil {
		return WebSearchResult{}, errors.New("web search: nil context")
	}
	if strings.TrimSpace(req.Query) == "" || req.Model == "" || req.APIKey == "" || req.BaseURL == "" {
		return WebSearchResult{}, errors.New("web search: query, model, key and base URL required")
	}
	if req.MaxOutput <= 0 || req.MaxOutput > 1024 {
		req.MaxOutput = 512
	}
	if req.MaxResults <= 0 || req.MaxResults > 10 {
		req.MaxResults = 5
	}
	if len([]rune(req.Query)) > webSearchMaxQueryRunes {
		return WebSearchResult{}, errors.New("web search: query too long")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 40 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// search_context_size "low" is the documented setting for a lookup that
	// needs a few details, which is all a fact check before a post needs, and it
	// is the difference between an answer and a timeout.
	tool := map[string]any{"type": "web_search", "search_context_size": "low"}
	if req.Country != "" {
		tool["user_location"] = map[string]any{"type": "approximate", "country": req.Country}
	}
	body, err := json.Marshal(map[string]any{
		"model":             req.Model,
		"input":             req.Query,
		"tools":             []map[string]any{tool},
		"max_output_tokens": req.MaxOutput,
		"store":             false,
	})
	if err != nil {
		return WebSearchResult{}, errors.New("web search: encoding request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(req.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return WebSearchResult{}, errors.New("web search: building request")
	}
	request.Header.Set("Authorization", "Bearer "+req.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return WebSearchResult{}, fmt.Errorf("web search: %w", contextErr)
		}
		return WebSearchResult{}, errors.New("web search: request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return WebSearchResult{}, fmt.Errorf("web search: unexpected HTTP status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, webSearchMaxResponseBytes+1))
	if err != nil {
		return WebSearchResult{}, errors.New("web search: reading response")
	}
	if len(raw) > webSearchMaxResponseBytes {
		return WebSearchResult{}, errors.New("web search: response too large")
	}
	var decoded struct {
		Output []struct {
			Content []struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Annotations []struct {
					Type  string `json:"type"`
					URL   string `json:"url"`
					Title string `json:"title"`
				} `json:"annotations"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  *int64 `json:"input_tokens"`
			OutputTokens *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return WebSearchResult{}, errors.New("web search: invalid response")
	}
	result := WebSearchResult{Sources: []WebSource{}}
	seen := map[string]bool{}
	for _, item := range decoded.Output {
		for _, content := range item.Content {
			if content.Type != "output_text" && content.Type != "text" {
				continue
			}
			if result.Answer == "" {
				result.Answer = strings.TrimSpace(content.Text)
			}
			for _, annotation := range content.Annotations {
				if annotation.URL == "" || seen[annotation.URL] {
					continue
				}
				seen[annotation.URL] = true
				result.Sources = append(result.Sources, WebSource{Title: annotation.Title, URL: annotation.URL})
				if len(result.Sources) == req.MaxResults {
					break
				}
			}
		}
	}
	if result.Answer == "" && len(result.Sources) == 0 {
		return WebSearchResult{}, errors.New("web search: empty response")
	}
	if decoded.Usage.InputTokens == nil || decoded.Usage.OutputTokens == nil {
		return WebSearchResult{}, errors.New("web search: missing usage")
	}
	if *decoded.Usage.InputTokens < 0 || *decoded.Usage.OutputTokens < 0 {
		return WebSearchResult{}, errors.New("web search: invalid usage")
	}
	result.Usage = store.LLMUsage{TokensIn: *decoded.Usage.InputTokens, TokensOut: *decoded.Usage.OutputTokens}
	return result, nil
}
