package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/config"
)

const (
	fakeToken  = "123456:FAKE-SYNTHETIC-TOKEN"
	fakeChatID = int64(9001)
	fakeText   = "synthetic-secret-text"
)

type capturedRequest struct {
	method      string
	path        string
	contentType string
	body        []byte
}

// contextRecorder records the client-side request deadline so tests can verify
// long-poll and caller-deadline bounds without relying on server context.
type contextRecorder struct {
	base     http.RoundTripper
	deadline time.Time
	has      bool
}

func (r *contextRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	r.deadline, r.has = request.Context().Deadline()
	base := r.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(request)
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "api", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

// newFixtureServer serves one synthetic Bot API response and records every
// request it receives. It never contacts a real service.
func newFixtureServer(t *testing.T, status int, response []byte) (*httptest.Server, <-chan capturedRequest) {
	t.Helper()
	requests := make(chan capturedRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		requests <- capturedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			body:        body,
		}
		w.Header().Set("Content-Type", "application/json")
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = w.Write(response)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func newClientForServer(t *testing.T, server *httptest.Server, mutate func(*ClientOptions)) *Client {
	t.Helper()
	opts := ClientOptions{
		BaseURL: server.URL + "/",
		Token:   config.NewSecret(fakeToken),
		Timeout: 2 * time.Second,
	}
	if mutate != nil {
		mutate(&opts)
	}
	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestNewClient_DefaultsAndValidation(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		client, err := NewClient(ClientOptions{Token: config.NewSecret(fakeToken)})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if client.baseURL != defaultBaseURL {
			t.Errorf("baseURL = %q, want %q", client.baseURL, defaultBaseURL)
		}
		if client.timeout != defaultTimeout {
			t.Errorf("timeout = %v, want %v", client.timeout, defaultTimeout)
		}
	})

	t.Run("trailing slash and custom timeout", func(t *testing.T) {
		client, err := NewClient(ClientOptions{
			BaseURL: "https://api.telegram.org/",
			Token:   config.NewSecret(fakeToken),
			Timeout: 3 * time.Second,
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if client.baseURL != "https://api.telegram.org" {
			t.Errorf("baseURL = %q, want trimmed official URL", client.baseURL)
		}
		if client.timeout != 3*time.Second {
			t.Errorf("timeout = %v, want 3s", client.timeout)
		}
	})

	cases := []struct {
		name string
		opts ClientOptions
	}{
		{"missing token", ClientOptions{}},
		{"blank token", ClientOptions{Token: config.NewSecret("   ")}},
		{"token with path separator", ClientOptions{Token: config.NewSecret("123:bad/token")}},
		{"token with space", ClientOptions{Token: config.NewSecret("123:bad token")}},
		{"negative timeout", ClientOptions{Token: config.NewSecret(fakeToken), Timeout: -time.Second}},
		{"relative base", ClientOptions{Token: config.NewSecret(fakeToken), BaseURL: "api.telegram.org"}},
		{"unsupported scheme", ClientOptions{Token: config.NewSecret(fakeToken), BaseURL: "ftp://example.test"}},
		{"missing host", ClientOptions{Token: config.NewSecret(fakeToken), BaseURL: "https://"}},
		{"base query", ClientOptions{Token: config.NewSecret(fakeToken), BaseURL: "https://example.test?x=1"}},
		{"base fragment", ClientOptions{Token: config.NewSecret(fakeToken), BaseURL: "https://example.test/#x"}},
		{"base userinfo", ClientOptions{Token: config.NewSecret(fakeToken), BaseURL: "https://user:pass@example.test"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient(tc.opts)
			if err == nil {
				t.Fatalf("NewClient succeeded for invalid options")
			}
			if client != nil {
				t.Fatalf("NewClient returned a client with an error")
			}
			if strings.Contains(err.Error(), fakeToken) {
				t.Fatalf("error leaks the token: %v", err)
			}
		})
	}
}

func TestGetUpdates_Success(t *testing.T) {
	server, requests := newFixtureServer(t, http.StatusOK, readFixture(t, "get_updates_ok.json"))
	client := newClientForServer(t, server, nil)

	updates, err := client.GetUpdates(context.Background(), 1000, 0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 3 {
		t.Fatalf("len(updates) = %d, want 3", len(updates))
	}

	request := <-requests
	if request.method != http.MethodPost {
		t.Errorf("method = %q, want POST", request.method)
	}
	if request.path != "/bot"+fakeToken+"/getUpdates" {
		t.Errorf("path = %q, want Bot API getUpdates path", request.path)
	}
	if !strings.HasPrefix(request.contentType, "application/json") {
		t.Errorf("content type = %q, want application/json", request.contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(request.body, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(payload) != 2 {
		t.Errorf("payload keys = %v, want only offset and timeout", payload)
	}
	if payload["offset"] != float64(1000) {
		t.Errorf("offset = %v, want 1000", payload["offset"])
	}
	if payload["timeout"] != float64(0) {
		t.Errorf("timeout = %v, want 0", payload["timeout"])
	}

	first := updates[0]
	if first.UpdateID != 1001 {
		t.Errorf("first update id = %d, want 1001", first.UpdateID)
	}
	if first.Message == nil {
		t.Fatalf("first update message is nil")
	}
	if first.Message.MessageID != 5 {
		t.Errorf("message id = %d, want 5", first.Message.MessageID)
	}
	if first.Message.Chat.ID != fakeChatID {
		t.Errorf("chat id = %d, want %d", first.Message.Chat.ID, fakeChatID)
	}
	if first.Message.Text != "/status agent-a" {
		t.Errorf("text = %q, want /status agent-a", first.Message.Text)
	}
	if first.Message.ReplyToMessage == nil {
		t.Fatalf("reply_to_message is nil")
	}
	if first.Message.ReplyToMessage.MessageID != 4 || first.Message.ReplyToMessage.Text != "What is your status?" {
		t.Errorf("reply metadata = %+v, want message 4 with text", first.Message.ReplyToMessage)
	}
	if first.Message.ReplyToMessage.ReplyToMessage != nil {
		t.Errorf("nested reply unexpectedly decoded")
	}

	// edited_message and callback_query are not messages; they survive only as
	// update ids so the caller can advance its offset.
	for i, wantID := range []int64{1002, 1003} {
		update := updates[i+1]
		if update.UpdateID != wantID {
			t.Errorf("update[%d].UpdateID = %d, want %d", i+1, update.UpdateID, wantID)
		}
		if update.Message != nil {
			t.Errorf("update[%d].Message = %+v, want nil for ignored update type", i+1, update.Message)
		}
	}

	encoded, err := json.Marshal(updates)
	if err != nil {
		t.Fatalf("marshal decoded updates: %v", err)
	}
	for _, forbidden := range []string{"username", "first_name", "synthetic_owner", "synthetic_bot"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("decoded updates retain profile field %q: %s", forbidden, encoded)
		}
	}
}

func TestGetUpdates_Empty(t *testing.T) {
	server, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_updates_empty.json"))
	client := newClientForServer(t, server, nil)

	updates, err := client.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("len(updates) = %d, want 0", len(updates))
	}
}

func TestGetUpdates_UnicodeAndEscapedText(t *testing.T) {
	server, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_updates_unicode.json"))
	client := newClientForServer(t, server, nil)

	updates, err := client.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 1 || updates[0].Message == nil {
		t.Fatalf("updates = %+v, want one message", updates)
	}
	want := "héllo ✅ 🚀 \"quoted\" \\ slash\nnew\tline"
	if updates[0].Message.Text != want {
		t.Errorf("text = %q, want %q", updates[0].Message.Text, want)
	}
	if updates[0].Message.ReplyToMessage == nil || updates[0].Message.ReplyToMessage.Text != "réponse échappée" {
		t.Errorf("reply text = %+v, want escaped Unicode decoded", updates[0].Message.ReplyToMessage)
	}
}

func TestGetUpdates_LongPollBounds(t *testing.T) {
	server, requests := newFixtureServer(t, http.StatusOK, readFixture(t, "get_updates_empty.json"))
	recorder := &contextRecorder{}
	client := newClientForServer(t, server, func(opts *ClientOptions) {
		opts.HTTPClient = &http.Client{Transport: recorder}
	})

	cases := []struct {
		name            string
		waitSeconds     int
		wantTimeout     int
		wantMinDeadline time.Duration
	}{
		{"short poll", 0, 0, 1 * time.Second},
		{"long poll", 2, 2, 10 * time.Second},
		{"clamped high", 120, 50, 50 * time.Second},
		{"negative clamped", -5, 0, 1 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.GetUpdates(context.Background(), 7, tc.waitSeconds); err != nil {
				t.Fatalf("GetUpdates: %v", err)
			}
			request := <-requests
			var payload map[string]any
			if err := json.Unmarshal(request.body, &payload); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if payload["timeout"] != float64(tc.wantTimeout) {
				t.Errorf("timeout = %v, want %d", payload["timeout"], tc.wantTimeout)
			}
			if payload["offset"] != float64(7) {
				t.Errorf("offset = %v, want 7", payload["offset"])
			}
			if !recorder.has {
				t.Fatalf("request has no deadline")
			}
			remaining := time.Until(recorder.deadline)
			if remaining < tc.wantMinDeadline {
				t.Errorf("deadline remaining = %v, want at least %v", remaining, tc.wantMinDeadline)
			}
		})
	}

	t.Run("negative offset preserved", func(t *testing.T) {
		if _, err := client.GetUpdates(context.Background(), -3, 0); err != nil {
			t.Fatalf("GetUpdates: %v", err)
		}
		request := <-requests
		var payload map[string]any
		if err := json.Unmarshal(request.body, &payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if payload["offset"] != float64(-3) {
			t.Errorf("offset = %v, want -3", payload["offset"])
		}
	})
}

func TestGetUpdates_CallerDeadlineWins(t *testing.T) {
	server, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_updates_empty.json"))
	recorder := &contextRecorder{}
	client := newClientForServer(t, server, func(opts *ClientOptions) {
		opts.Timeout = 30 * time.Second
		opts.HTTPClient = &http.Client{Transport: recorder}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.GetUpdates(ctx, 0, 0); err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}

	if !recorder.has {
		t.Fatalf("request has no deadline")
	}
	remaining := time.Until(recorder.deadline)
	if remaining > 5*time.Second {
		t.Errorf("deadline remaining = %v, want the caller's 2s deadline", remaining)
	}
}

func TestGetUpdates_TimeoutIsContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	t.Cleanup(server.Close)
	client := newClientForServer(t, server, func(opts *ClientOptions) {
		opts.Timeout = 30 * time.Millisecond
	})

	start := time.Now()
	_, err := client.GetUpdates(context.Background(), 0, 0)
	if err == nil {
		t.Fatalf("GetUpdates succeeded, want timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded identity", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("timeout took %v, want bounded", elapsed)
	}
	if strings.Contains(err.Error(), fakeToken) || strings.Contains(err.Error(), "api.telegram.org") {
		t.Errorf("error leaks endpoint or token: %v", err)
	}
}

func TestGetUpdates_PreCancelledContext(t *testing.T) {
	server, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_updates_empty.json"))
	client := newClientForServer(t, server, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.GetUpdates(ctx, 0, 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled identity", err)
	}
	if strings.Contains(err.Error(), fakeToken) {
		t.Errorf("error leaks the token: %v", err)
	}
}

func TestGetUpdates_CancelDuringBodyRead(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,`))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	t.Cleanup(server.Close)
	client := newClientForServer(t, server, nil)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.GetUpdates(ctx, 0, 0)
		result <- err
	}()
	<-started
	cancel()

	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled identity", err)
	}
	if strings.Contains(err.Error(), fakeToken) {
		t.Errorf("error leaks the token: %v", err)
	}
}

func TestSendMessage_Success(t *testing.T) {
	server, requests := newFixtureServer(t, http.StatusOK, readFixture(t, "send_message_ok.json"))
	client := newClientForServer(t, server, nil)

	messageID, err := client.SendMessage(context.Background(), fakeChatID, "héllo ✅", 77)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if messageID != 42 {
		t.Fatalf("message id = %d, want 42", messageID)
	}

	request := <-requests
	if request.method != http.MethodPost {
		t.Errorf("method = %q, want POST", request.method)
	}
	if request.path != "/bot"+fakeToken+"/sendMessage" {
		t.Errorf("path = %q, want Bot API sendMessage path", request.path)
	}
	if !strings.HasPrefix(request.contentType, "application/json") {
		t.Errorf("content type = %q, want application/json", request.contentType)
	}

	var payload struct {
		ChatID          int64  `json:"chat_id"`
		Text            string `json:"text"`
		ReplyParameters *struct {
			MessageID int64 `json:"message_id"`
		} `json:"reply_parameters"`
	}
	if err := json.Unmarshal(request.body, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if payload.ChatID != fakeChatID {
		t.Errorf("chat_id = %d, want %d", payload.ChatID, fakeChatID)
	}
	if payload.Text != "héllo ✅" {
		t.Errorf("text = %q, want Unicode text preserved", payload.Text)
	}
	if payload.ReplyParameters == nil || payload.ReplyParameters.MessageID != 77 {
		t.Errorf("reply_parameters = %+v, want message_id 77", payload.ReplyParameters)
	}
	raw := string(request.body)
	for _, forbidden := range []string{"reply_to_message_id", "username", "first_name"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("request body contains %q: %s", forbidden, raw)
		}
	}

	messageID, err = client.SendMessage(context.Background(), fakeChatID, "plain", 0)
	if err != nil || messageID != 42 {
		t.Fatalf("SendMessage without reply = %d, %v; want 42, nil", messageID, err)
	}
	request = <-requests
	if strings.Contains(string(request.body), "reply_parameters") {
		t.Errorf("request without reply sent reply_parameters: %s", request.body)
	}
}

func TestSendMessage_Validation(t *testing.T) {
	server, requests := newFixtureServer(t, http.StatusOK, readFixture(t, "send_message_ok.json"))
	client := newClientForServer(t, server, nil)

	cases := []struct {
		name    string
		ctx     context.Context
		chatID  int64
		text    string
		replyTo int64
		want    string
	}{
		{"zero chat", context.Background(), 0, "hello", 0, "chat id is required"},
		{"empty text", context.Background(), fakeChatID, "", 0, "text is required"},
		{"too long", context.Background(), fakeChatID, strings.Repeat("x", maxMessageRunes+1), 0, "text exceeds 4096 characters"},
		{"negative reply", context.Background(), fakeChatID, "hello", -1, "reply message id is invalid"},
		{"nil context", nil, fakeChatID, "hello", 0, "nil context"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			messageID, err := client.SendMessage(tc.ctx, tc.chatID, tc.text, tc.replyTo)
			if err == nil {
				t.Fatalf("SendMessage succeeded, want error")
			}
			if messageID != 0 {
				t.Errorf("message id = %d, want 0 on error", messageID)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
		})
	}

	if _, err := client.GetUpdates(nil, 0, 0); err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Errorf("GetUpdates(nil) error = %v, want nil context", err)
	}

	select {
	case request := <-requests:
		t.Fatalf("validation sent an HTTP request: %+v", request)
	default:
	}
}

func TestSendMessage_APIErrorIsSanitized(t *testing.T) {
	server, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "api_error.json"))
	client := newClientForServer(t, server, nil)

	messageID, err := client.SendMessage(context.Background(), fakeChatID, fakeText, 0)
	if err == nil {
		t.Fatalf("SendMessage succeeded, want API error")
	}
	if messageID != 0 {
		t.Errorf("message id = %d, want 0", messageID)
	}
	if !strings.Contains(err.Error(), "api error code 429") {
		t.Errorf("error = %v, want numeric API error code", err)
	}
	for _, forbidden := range []string{
		"Too Many Requests", "retry after", "description", "retry_after",
		fakeToken, fakeText, fmt.Sprintf("%d", fakeChatID), "api.telegram.org",
	} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("error leaks %q: %v", forbidden, err)
		}
	}
}

func TestEnvelopeFailures(t *testing.T) {
	cases := []struct {
		name     string
		response string
		send     bool
		want     string
	}{
		{"malformed json", string(readFixture(t, "malformed.json")), false, "decode response"},
		{"empty body", "", false, "decode response"},
		{"not ok without code", `{"ok":false}`, false, "api error code 0"},
		{"missing result", `{"ok":true}`, false, "missing result"},
		{"null result", `{"ok":true,"result":null}`, false, "missing result"},
		{"wrong result type", `{"ok":true,"result":{}}`, false, "decode result"},
		{"send missing message id", `{"ok":true,"result":{"message_id":0,"chat":{"id":9001}}}`, true, "missing message id"},
		{"send wrong result type", `{"ok":true,"result":[]}`, true, "decode result"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newFixtureServer(t, http.StatusOK, []byte(tc.response))
			client := newClientForServer(t, server, nil)
			var err error
			if tc.send {
				_, err = client.SendMessage(context.Background(), fakeChatID, fakeText, 0)
			} else {
				_, err = client.GetUpdates(context.Background(), 0, 0)
			}
			if err == nil {
				t.Fatalf("call succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
			for _, forbidden := range []string{"update_id", "message_id", fakeToken, fakeText} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("error leaks %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestResponseLimitsAndHTTPErrors(t *testing.T) {
	t.Run("oversize response", func(t *testing.T) {
		server, _ := newFixtureServer(t, http.StatusOK, []byte(strings.Repeat("x", maxResponseBytes+1)))
		client := newClientForServer(t, server, nil)

		_, err := client.GetUpdates(context.Background(), 0, 0)
		if err == nil {
			t.Fatalf("GetUpdates succeeded, want oversize error")
		}
		if !strings.Contains(err.Error(), "response exceeds 4194304 bytes") {
			t.Errorf("error = %v, want bounded response error", err)
		}
		if strings.Contains(err.Error(), "xxxx") {
			t.Errorf("error leaks response body: %v", err)
		}
	})

	t.Run("http status", func(t *testing.T) {
		server, _ := newFixtureServer(t, http.StatusServiceUnavailable, []byte("synthetic upstream detail"))
		client := newClientForServer(t, server, nil)

		_, err := client.GetUpdates(context.Background(), 0, 0)
		if err == nil {
			t.Fatalf("GetUpdates succeeded, want HTTP error")
		}
		if !strings.Contains(err.Error(), "unexpected HTTP status 503") {
			t.Errorf("error = %v, want HTTP status", err)
		}
		if strings.Contains(err.Error(), "synthetic upstream detail") {
			t.Errorf("error leaks response body: %v", err)
		}
	})

	t.Run("single attempt", func(t *testing.T) {
		var hits atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(server.Close)
		client := newClientForServer(t, server, nil)

		_, _ = client.GetUpdates(context.Background(), 0, 0)
		if got := hits.Load(); got != 1 {
			t.Errorf("requests = %d, want exactly 1 (no retries)", got)
		}
	})
}

func TestRedirectsAreDisabled(t *testing.T) {
	emptyResponse := readFixture(t, "get_updates_empty.json")
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(emptyResponse)
	}))
	t.Cleanup(target.Close)

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	permissive := func(*http.Request, []*http.Request) error { return nil }
	client := newClientForServer(t, redirect, func(opts *ClientOptions) {
		opts.HTTPClient = &http.Client{CheckRedirect: permissive}
	})

	_, err := client.GetUpdates(context.Background(), 0, 0)
	if err == nil || !strings.Contains(err.Error(), "unexpected HTTP status 302") {
		t.Fatalf("GetUpdates error = %v, want unexpected HTTP status 302", err)
	}
	_, err = client.SendMessage(context.Background(), fakeChatID, fakeText, 0)
	if err == nil || !strings.Contains(err.Error(), "unexpected HTTP status 302") {
		t.Fatalf("SendMessage error = %v, want unexpected HTTP status 302", err)
	}
	if got := targetHits.Load(); got != 0 {
		t.Errorf("redirect target hits = %d, want 0", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type timeoutTransportError struct{}

func (timeoutTransportError) Error() string   { return "i/o timeout with " + fakeToken }
func (timeoutTransportError) Timeout() bool   { return true }
func (timeoutTransportError) Temporary() bool { return false }

func TestTransportErrorsAreSanitized(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantCanceled bool
		wantDeadline bool
	}{
		{
			name: "credential-bearing transport error",
			err: &url.Error{
				Op:  "Post",
				URL: "https://api.telegram.org/bot" + fakeToken + "/getUpdates",
				Err: fmt.Errorf("dial for chat %d failed while sending %s", fakeChatID, fakeText),
			},
		},
		{
			name:         "wrapped cancellation",
			err:          fmt.Errorf("transport: %w", context.Canceled),
			wantCanceled: true,
		},
		{
			name:         "wrapped deadline",
			err:          fmt.Errorf("transport: %w", context.DeadlineExceeded),
			wantDeadline: true,
		},
		{
			name:         "net timeout",
			err:          &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + fakeToken + "/getUpdates", Err: timeoutTransportError{}},
			wantDeadline: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient(ClientOptions{
				Token:   config.NewSecret(fakeToken),
				Timeout: time.Second,
				HTTPClient: &http.Client{
					Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						return nil, tc.err
					}),
				},
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			_, err = client.GetUpdates(context.Background(), 0, 0)
			if err == nil {
				t.Fatalf("GetUpdates succeeded, want transport error")
			}
			if tc.wantCanceled && !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v, want context.Canceled identity", err)
			}
			if tc.wantDeadline && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("error = %v, want context.DeadlineExceeded identity", err)
			}
			for _, forbidden := range []string{
				fakeToken, fakeText, fmt.Sprintf("%d", fakeChatID),
				"https://api.telegram.org", "dial for chat", "i/o timeout",
			} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("error leaks %q: %v", forbidden, err)
				}
			}
		})
	}
}
