package trends_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if _, err := trends.NewWebSearcher(server.Client())(context.Background(), trends.WebSearchRequest{BaseURL: server.URL, Model: "search-model", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second}); trends.WebSearchReason(err) != "http_500" {
		t.Fatalf("HTTP 500: %v", err)
	}
	missingUsage := webSearchServer(t, http.StatusOK, `{"output":[{"content":[{"type":"output_text","text":"answer"}]}]}`)
	if _, err := trends.NewWebSearcher(missingUsage.Client())(context.Background(), trends.WebSearchRequest{BaseURL: missingUsage.URL, Model: "search-model", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second}); trends.WebSearchReason(err) != "missing_usage" {
		t.Fatalf("missing usage: %v", err)
	}
	empty := webSearchServer(t, http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	if _, err := trends.NewWebSearcher(empty.Client())(context.Background(), trends.WebSearchRequest{BaseURL: empty.URL, Model: "search-model", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second}); err == nil {
		t.Fatal("empty answer accepted")
	}
}

// The search is a fact check before a post, so it asks for the small context the
// API documents for that, and says where its readers are instead of taking the
// United States default. A query long enough to be a pasted URL is refused
// before the call, which is what used to run into the timeout.
func TestSearchWebSendsLowContextAndLocationAndRefusesALongQuery(t *testing.T) {
	var tool struct {
		Type         string `json:"type"`
		ContextSize  string `json:"search_context_size"`
		UserLocation struct {
			Type    string `json:"type"`
			Country string `json:"country"`
		} `json:"user_location"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Tools) != 1 {
			t.Errorf("tools=%v err=%v", request.Tools, err)
			return
		}
		if err := json.Unmarshal(request.Tools[0], &tool); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	t.Cleanup(server.Close)

	search := trends.NewWebSearcher(server.Client())
	if _, err := search(context.Background(), trends.WebSearchRequest{BaseURL: server.URL, Model: "m", APIKey: "key", Query: "who won", MaxOutput: 64, Country: "VN", Timeout: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if tool.Type != "web_search" || tool.ContextSize != "low" {
		t.Fatalf("tool=%+v", tool)
	}
	if tool.UserLocation.Type != "approximate" || tool.UserLocation.Country != "VN" {
		t.Fatalf("location=%+v", tool.UserLocation)
	}

	long := strings.Repeat("a", 201)
	if _, err := search(context.Background(), trends.WebSearchRequest{BaseURL: server.URL, Model: "m", APIKey: "key", Query: long, MaxOutput: 64, Timeout: 3 * time.Second}); err == nil {
		t.Fatal("a query past the cap was sent")
	}
}

// A turn that ends with no message is asked once more; a second empty turn and
// any other failure are not retried.
func TestSearchWebRetriesOneEmptyResponse(t *testing.T) {
	empty := `{"output":[{"type":"web_search_call"}],"usage":{"input_tokens":5,"output_tokens":0}}`
	answer := `{"output":[{"content":[{"type":"output_text","text":"an answer"}]}],"usage":{"input_tokens":5,"output_tokens":3}}`
	for _, tc := range []struct {
		name    string
		bodies  []string
		calls   int
		wantErr bool
		in, out int64
	}{
		{"empty then answer", []string{empty, answer}, 2, false, 10, 3},
		{"empty twice", []string{empty, empty, answer}, 2, true, 10, 0},
		{"answer first", []string{answer, answer}, 1, false, 5, 3},
	} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, tc.bodies[calls])
			calls++
		}))
		result, err := trends.NewWebSearcher(server.Client())(context.Background(), trends.WebSearchRequest{BaseURL: server.URL, Model: "m", APIKey: "key", Query: "q", MaxOutput: 256, Timeout: time.Second})
		server.Close()
		if calls != tc.calls || (err != nil) != tc.wantErr || (!tc.wantErr && result.Answer != "an answer") {
			t.Errorf("%s: calls %d, err %v, answer %q", tc.name, calls, err, result.Answer)
		}
		// Every call made is paid for, answered or not.
		if result.Usage.TokensIn != tc.in || result.Usage.TokensOut != tc.out {
			t.Errorf("%s: usage %d in, %d out; want %d, %d", tc.name, result.Usage.TokensIn, result.Usage.TokensOut, tc.in, tc.out)
		}
	}
}
