package trends_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/trends"
)

func webSearchServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("unexpected path/auth: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request struct {
			Model     string                  `json:"model"`
			Tools     []struct{ Type string } `json:"tools"`
			MaxOutput int                     `json:"max_output_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "search-model" || len(request.Tools) != 1 || request.Tools[0].Type != "web_search" || request.MaxOutput != 256 {
			t.Errorf("request=%+v", request)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSearchWebParsesBoundedAnswerSourcesAndUsage(t *testing.T) {
	server := webSearchServer(t, http.StatusOK, `{
		"output":[{"type":"message","content":[{"type":"output_text","text":"Jensen Huang is in the news.","annotations":[
			{"type":"url_citation","url":"https://a.invalid/1","title":"A"},
			{"type":"url_citation","url":"https://a.invalid/1","title":"duplicate"},
			{"type":"url_citation","url":"https://b.invalid/2","title":"B"},
			{"type":"url_citation","url":"https://c.invalid/3","title":"C"}]}]}],
		"usage":{"input_tokens":120,"output_tokens":40}
	}`)
	result, err := trends.NewWebSearcher(server.Client())(context.Background(), trends.WebSearchRequest{BaseURL: server.URL, Model: "search-model", APIKey: "key", Query: "Jensen Huang news", MaxOutput: 256, MaxResults: 3, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "Jensen Huang is in the news." || len(result.Sources) != 3 {
		t.Fatalf("result=%+v", result)
	}
	if result.Usage.TokensIn != 120 || result.Usage.TokensOut != 40 {
		t.Fatalf("usage=%+v", result.Usage)
	}
}

func TestSearchWebFailsClosed(t *testing.T) {
	server := webSearchServer(t, http.StatusInternalServerError, `{"error":"nope"}`)
	if _, err := trends.NewWebSearcher(server.Client())(context.Background(), trends.WebSearchRequest{BaseURL: server.URL, Model: "search-model", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second}); err == nil {
		t.Fatal("HTTP 500 accepted")
	}
	missingUsage := webSearchServer(t, http.StatusOK, `{"output":[{"content":[{"type":"output_text","text":"answer"}]}]}`)
	if _, err := trends.NewWebSearcher(missingUsage.Client())(context.Background(), trends.WebSearchRequest{BaseURL: missingUsage.URL, Model: "search-model", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second}); err == nil {
		t.Fatal("missing usage accepted")
	}
	empty := webSearchServer(t, http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	if _, err := trends.NewWebSearcher(empty.Client())(context.Background(), trends.WebSearchRequest{BaseURL: empty.URL, Model: "search-model", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second}); err == nil {
		t.Fatal("empty answer accepted")
	}
}
