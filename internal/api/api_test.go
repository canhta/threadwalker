package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
)

type fakeStore struct {
	mu     sync.Mutex
	events map[string]bool
	woken  []string
}

func (f *fakeStore) RecordWebhookEvent(_ context.Context, agent, field, id string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		f.events = map[string]bool{}
	}
	k := agent + field + id
	fresh := !f.events[k]
	f.events[k] = true
	return fresh, nil
}

func (f *fakeStore) WakeOnWebhook(_ context.Context, agent string, _ time.Time, _ time.Duration) error {
	f.woken = append(f.woken, agent)
	return nil
}

type fakeTokens struct {
	agent, token string
	expires      time.Duration
}

func (f *fakeTokens) Install(_ context.Context, agent string, t config.Secret, e time.Duration) error {
	f.agent, f.token, f.expires = agent, t.Expose(), e
	return nil
}

type counter struct{ n byte }

func (c *counter) Read(b []byte) (int, error) {
	for i := range b {
		c.n++
		b[i] = c.n
	}
	return len(b), nil
}

type rig struct {
	srv    *Server
	store  *fakeStore
	tokens *fakeTokens
	graph  *httptest.Server
	now    time.Time
	hits   []string
}

func newRig(t *testing.T, userID string) *rig {
	t.Helper()
	r := &rig{store: &fakeStore{}, tokens: &fakeTokens{}, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r.graph = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.hits = append(r.hits, q.URL.Path)
		switch q.URL.Path {
		case "/oauth/access_token":
			_ = q.ParseForm()
			if q.PostForm.Get("code") != "the-code" || q.PostForm.Get("client_secret") != "app-secret" || q.PostForm.Get("redirect_uri") != "https://api.example.com/threads/oauth/callback" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"short","user_id":`+userID+`}`)
		case "/access_token":
			if q.URL.Query().Get("access_token") != "short" || q.URL.Query().Get("grant_type") != "th_exchange_token" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"long","expires_in":5184000}`)
		}
	}))
	t.Cleanup(r.graph.Close)
	srv, err := New(Options{
		Agents: []Agent{{ID: "a1", UserID: "42", Username: "bot", MinWakeGap: time.Hour}}, AppID: "app", AppSecret: config.NewSecret("app-secret"),
		VerifyToken: config.NewSecret("verify"), PublicURL: "https://api.example.com", GraphURL: r.graph.URL, AuthorizeURL: "https://consent.example/authorize",
		Store: r.store, Tokens: r.tokens, Now: func() time.Time { return r.now }, Rand: &counter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.srv = srv
	return r
}

func (r *rig) do(method, target string, body string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1"
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	r.srv.ServeHTTP(w, req)
	return w
}

func (r *rig) startState(t *testing.T) string {
	t.Helper()
	link, err := r.srv.NewLink("a1")
	if err != nil {
		t.Fatal(err)
	}
	w := r.do("GET", strings.TrimPrefix(link, "https://api.example.com"), "")
	if w.Code != http.StatusFound {
		t.Fatalf("start status %d", w.Code)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	q := loc.Query()
	if loc.Host != "consent.example" || q.Get("client_id") != "app" || q.Get("redirect_uri") != r.srv.RedirectURI() || len(strings.Split(q.Get("scope"), ",")) != 8 || q.Get("state") == "" {
		t.Fatalf("bad authorize redirect %s", loc)
	}
	return q.Get("state")
}

func TestOAuthFlowStoresToken(t *testing.T) {
	r := newRig(t, "42")
	state := r.startState(t)
	w := r.do("GET", "/threads/oauth/callback?code=the-code%23_&state="+state, "")
	if w.Code != http.StatusOK || r.tokens.agent != "a1" || r.tokens.token != "long" || r.tokens.expires != 5184000*time.Second {
		t.Fatalf("callback %d %+v", w.Code, r.tokens)
	}
	if strings.Contains(w.Body.String(), "long") || strings.Contains(w.Body.String(), "short") {
		t.Fatal("token leaked into the page")
	}
	if again := r.do("GET", "/threads/oauth/callback?code=the-code&state="+state, ""); again.Code != http.StatusForbidden {
		t.Fatalf("state reuse status %d", again.Code)
	}
}

func TestOAuthRefusals(t *testing.T) {
	r := newRig(t, "42")
	state := r.startState(t)
	if w := r.do("GET", "/threads/oauth/callback?code=the-code&state=wrong", ""); w.Code != http.StatusForbidden {
		t.Fatalf("state mismatch status %d", w.Code)
	}
	if w := r.do("GET", "/threads/oauth/callback?code=the-code", ""); w.Code != http.StatusForbidden {
		t.Fatalf("missing state status %d", w.Code)
	}
	r.now = r.now.Add(stateTTL)
	if w := r.do("GET", "/threads/oauth/callback?code=the-code&state="+state, ""); w.Code != http.StatusForbidden {
		t.Fatalf("expired state status %d", w.Code)
	}
	if r.tokens.token != "" {
		t.Fatal("token stored after a refusal")
	}
	if w := r.do("GET", "/threads/oauth/start?k=guess", ""); w.Code != http.StatusForbidden {
		t.Fatalf("unknown link status %d", w.Code)
	}
	link, _ := r.srv.NewLink("a1")
	r.now = r.now.Add(linkTTL)
	if w := r.do("GET", strings.TrimPrefix(link, "https://api.example.com"), ""); w.Code != http.StatusForbidden {
		t.Fatalf("expired link status %d", w.Code)
	}
	link, _ = r.srv.NewLink("a1")
	path := strings.TrimPrefix(link, "https://api.example.com")
	r.do("GET", path, "")
	if w := r.do("GET", path, ""); w.Code != http.StatusForbidden {
		t.Fatalf("link reuse status %d", w.Code)
	}
	if _, err := r.srv.NewLink("nobody"); err == nil {
		t.Fatal("link for an unknown agent")
	}
}

func TestOAuthDeniedAndWrongAccountAndFailedExchange(t *testing.T) {
	r := newRig(t, "42")
	if w := r.do("GET", "/threads/oauth/callback?error=access_denied&state="+r.startState(t), ""); w.Code != http.StatusBadRequest {
		t.Fatalf("denied status %d", w.Code)
	}
	if w := r.do("GET", "/threads/oauth/callback?code=bad-code&state="+r.startState(t), ""); w.Code != http.StatusBadGateway || r.tokens.token != "" {
		t.Fatalf("failed exchange status %d", w.Code)
	}
	other := newRig(t, "99")
	if w := other.do("GET", "/threads/oauth/callback?code=the-code&state="+other.startState(t), ""); w.Code != http.StatusBadGateway || other.tokens.token != "" {
		t.Fatalf("wrong account status %d", w.Code)
	}
}

func TestLinkCapAndStateCap(t *testing.T) {
	r := newRig(t, "42")
	for i := 0; i < maxPending; i++ {
		if _, err := r.srv.NewLink("a1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.srv.NewLink("a1"); err == nil {
		t.Fatal("link cap not enforced")
	}
	for k := range r.srv.links {
		r.srv.states[k] = pending{"a1", r.now.Add(time.Hour)}
	}
	link := "/threads/oauth/start?k="
	for k := range r.srv.links {
		link += k
		break
	}
	if w := r.do("GET", link, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("state cap status %d", w.Code)
	}
}

func sign(body, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// delivery builds a documented Threads delivery for one value.
func delivery(target, field, id, username, extra string) string {
	return `{"app_id":"app","topic":"t","target_id":"` + target + `","values":{"field":"` + field + `","value":{"id":"` + id + `","username":"` + username + `"` + extra + `}}}`
}

func TestWebhookVerify(t *testing.T) {
	r := newRig(t, "42")
	if w := r.do("GET", "/threads/webhooks?hub.mode=subscribe&hub.verify_token=verify&hub.challenge=abc123", ""); w.Code != 200 || w.Body.String() != "abc123" {
		t.Fatalf("verify %d %q", w.Code, w.Body)
	}
	for _, q := range []string{"hub.mode=subscribe&hub.verify_token=nope&hub.challenge=x", "hub.mode=subscribe&hub.challenge=x", "hub.mode=other&hub.verify_token=verify"} {
		if w := r.do("GET", "/threads/webhooks?"+q, ""); w.Code != http.StatusForbidden {
			t.Fatalf("%s status %d", q, w.Code)
		}
	}
	if w := r.do("PUT", "/threads/webhooks", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status %d", w.Code)
	}
}

func TestWebhookSignature(t *testing.T) {
	r := newRig(t, "42")
	reply := fixture(t, "reply.json")
	for name, header := range map[string][]string{
		"missing":   nil,
		"invalid":   {"X-Hub-Signature-256", sign(reply, "other-secret")},
		"no-prefix": {"X-Hub-Signature-256", strings.TrimPrefix(sign(reply, "app-secret"), "sha256=")},
		"not-hex":   {"X-Hub-Signature-256", "sha256=zz"},
	} {
		if w := r.do("POST", "/threads/webhooks", reply, header...); w.Code != http.StatusForbidden {
			t.Fatalf("%s status %d", name, w.Code)
		}
	}
	if len(r.store.woken) != 0 || len(r.store.events) != 0 {
		t.Fatal("unsigned event had an effect")
	}
}

func TestWebhookEventWakesAndStoresIDsOnly(t *testing.T) {
	r := newRig(t, "42")
	reply := fixture(t, "reply.json")
	post := func(body string) int {
		return r.do("POST", "/threads/webhooks", body, "X-Hub-Signature-256", sign(body, "app-secret")).Code
	}
	if c := post(reply); c != 200 || len(r.store.woken) != 1 || r.store.woken[0] != "a1" {
		t.Fatalf("reply: %d %v", c, r.store.woken)
	}
	if c := post(reply); c != 200 || len(r.store.woken) != 1 {
		t.Fatalf("redelivery woke again: %d %v", c, r.store.woken)
	}
	if post(fixture(t, "mention.json")); len(r.store.woken) != 2 {
		t.Fatalf("mention did not wake: %v", r.store.woken)
	}
	for _, ignored := range []string{
		delivery("42", "replies", "r2", "BOT", ""),
		delivery("42", "likes", "l1", "x", ""),
		delivery("77", "replies", "r3", "x", ""),
		delivery("42", "replies", "", "x", ""),
	} {
		post(ignored)
	}
	if len(r.store.woken) != 2 || len(r.store.events) != 2 {
		t.Fatalf("ignored events had effect: woken %v events %v", r.store.woken, r.store.events)
	}
	post(delivery("42", "replies", "r9", "someone", `,"text":"secret words"`))
	for k := range r.store.events {
		if strings.Contains(k, "secret") {
			t.Fatal("reply text stored")
		}
	}
	if len(r.store.events) != 3 {
		t.Fatalf("text-bearing delivery not recorded: %v", r.store.events)
	}
	if c := post("{"); c != http.StatusBadRequest {
		t.Fatalf("bad json status %d", c)
	}
	big := strings.Repeat("x", maxBody+1)
	if c := post(big); c != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status %d", c)
	}
}

func TestPagesAndRouting(t *testing.T) {
	r := newRig(t, "42")
	for _, p := range []string{"/privacy", "/data-deletion"} {
		if w := r.do("GET", p, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Threadz") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	if w := r.do("POST", "/privacy", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("privacy POST %d", w.Code)
	}
	if w := r.do("GET", "/", ""); w.Code != http.StatusNotFound {
		t.Fatalf("root %d", w.Code)
	}
}

func TestRateLimitPerClient(t *testing.T) {
	r := newRig(t, "42")
	for i := 0; i < ratePerIP; i++ {
		if w := r.do("GET", "/privacy", "", "CF-Connecting-IP", "203.0.113.9"); w.Code != 200 {
			t.Fatalf("request %d status %d", i, w.Code)
		}
	}
	if w := r.do("GET", "/privacy", "", "CF-Connecting-IP", "203.0.113.9"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("over limit status %d", w.Code)
	}
	if w := r.do("GET", "/privacy", "", "CF-Connecting-IP", "203.0.113.10"); w.Code != 200 {
		t.Fatalf("other client blocked: %d", w.Code)
	}
	r.now = r.now.Add(time.Second)
	if w := r.do("GET", "/privacy", "", "CF-Connecting-IP", "203.0.113.9"); w.Code != 200 {
		t.Fatalf("bucket did not refill: %d", w.Code)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("empty options accepted")
	}
	r := newRig(t, "42")
	o := r.srv.o
	o.PublicURL = "http://insecure.example.com"
	if _, err := New(o); err == nil {
		t.Fatal("http public URL accepted")
	}
}
