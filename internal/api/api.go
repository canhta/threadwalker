// Package api is the loopback HTTP server behind the public origin given by -api-url:
// the Threads OAuth flow, Meta's webhooks and the static pages the Meta app
// requires. The Cloudflare tunnel is its only client.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/ratelimit"
)

// Scopes the OAuth start requests: every Threads permission the agent uses.
var Scopes = []string{"threads_basic", "threads_content_publish", "threads_manage_insights", "threads_manage_replies", "threads_read_replies", "threads_manage_mentions", "threads_keyword_search", "threads_profile_discovery"}

// The webhook fields that wake the agent: a reply to its post, or a mention.
const (
	fieldReplies  = "replies"
	fieldMentions = "mentions"
)

// Route names are the constant Operation of each request event.
const (
	opOAuthStart    = "oauth_start"
	opOAuthCallback = "oauth_callback"
	opWebhookVerify = "webhook_verify"
	opWebhookEvent  = "webhook_event"
	opPage          = "page"
	opUnknown       = "unknown"
)

const (
	linkTTL      = 15 * time.Minute
	stateTTL     = 10 * time.Minute
	maxBody      = 256 << 10
	maxPending   = 16
	ratePerIP    = 60
	maxTrackedIP = 4096
)

// Store is the persistence the server needs.
type Store interface {
	RecordWebhookEvent(ctx context.Context, agentID, field, objectID string, at time.Time) (bool, error)
	WakeOnWebhook(ctx context.Context, agentID string, at time.Time, minGap time.Duration) error
}

// Tokens keeps the token the OAuth flow obtains.
type Tokens interface {
	Install(ctx context.Context, agentID string, token config.Secret, expiresIn time.Duration) error
}

// Agent is one account the server connects and listens for.
type Agent struct {
	ID       string
	UserID   string
	Username string
	// MinWakeGap is the agent's minimum time between wakes.
	MinWakeGap time.Duration
}

// Options configures a Server. GraphURL is the Graph origin without a version
// ("https://graph.threads.net"); AuthorizeURL is the Threads consent page.
type Options struct {
	Agents       []Agent
	AppID        string
	AppSecret    config.Secret
	VerifyToken  config.Secret
	PublicURL    string
	GraphURL     string
	AuthorizeURL string
	HTTP         *http.Client
	Store        Store
	Tokens       Tokens
	Now          func() time.Time
	Rand         io.Reader
	Logger       *slog.Logger
}

// Server serves the routes. It is an http.Handler.
type Server struct {
	o       Options
	mu      sync.Mutex
	links   map[string]pending
	states  map[string]pending
	limiter *ratelimit.Limiter
	byUser  map[string]Agent
	byAgent map[string]Agent
}

type pending struct {
	agentID string
	expires time.Time
}

// New validates the options.
func New(o Options) (*Server, error) {
	if len(o.Agents) == 0 || o.AppID == "" || o.AppSecret.Expose() == "" || o.VerifyToken.Expose() == "" || o.Store == nil || o.Tokens == nil || o.Now == nil || o.Rand == nil {
		return nil, errors.New("configuring api server: agents, app id, app secret, verify token, store, tokens, clock and randomness required")
	}
	if u, err := url.Parse(o.PublicURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("configuring api server: -api-url must be an https origin")
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if o.GraphURL == "" {
		o.GraphURL = "https://graph.threads.net"
	}
	if o.AuthorizeURL == "" {
		o.AuthorizeURL = "https://threads.net/oauth/authorize"
	}
	s := &Server{o: o, links: map[string]pending{}, states: map[string]pending{}, limiter: ratelimit.New(float64(ratePerIP)/60, ratePerIP, maxTrackedIP, o.Now), byUser: map[string]Agent{}, byAgent: map[string]Agent{}}
	for _, a := range o.Agents {
		s.byUser[a.UserID] = a
		s.byAgent[a.ID] = a
	}
	return s, nil
}

// RedirectURI is the OAuth callback registered in the Meta app.
func (s *Server) RedirectURI() string {
	return strings.TrimRight(s.o.PublicURL, "/") + "/threads/oauth/callback"
}

func (s *Server) token() (string, error) {
	b := make([]byte, 24)
	if _, err := io.ReadFull(s.o.Rand, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewLink returns the owner-only start URL for one agent. It works once.
func (s *Server) NewLink(agentID string) (string, error) {
	return s.newLink(s.links, "oauth", "/threads/oauth/start", agentID)
}

func (s *Server) newLink(m map[string]pending, what, path, agentID string) (string, error) {
	if _, ok := s.byAgent[agentID]; !ok {
		return "", errors.New("creating " + what + " link: unknown agent")
	}
	k, err := s.token()
	if err != nil {
		return "", errors.New("creating " + what + " link: randomness failed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.o.Now()
	sweep(m, now)
	if len(m) >= maxPending {
		return "", errors.New("creating " + what + " link: too many open links")
	}
	m[k] = pending{agentID, now.Add(linkTTL)}
	return strings.TrimRight(s.o.PublicURL, "/") + path + "?k=" + k, nil
}

func sweep(m map[string]pending, now time.Time) {
	for k, p := range m {
		if !now.Before(p.expires) {
			delete(m, k)
		}
	}
}

// take removes and returns an unexpired entry, so each works once.
func (s *Server) take(m map[string]pending, key string) (pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.o.Now()
	sweep(m, now)
	p, ok := m[key]
	delete(m, key)
	return p, ok
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

// ServeHTTP routes and logs one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	op, reason := opUnknown, "unknown_path"
	if !s.allow(r) {
		http.Error(sw, "Too many requests.", http.StatusTooManyRequests)
		op, reason = opUnknown, "rate_limited"
	} else {
		switch r.URL.Path {
		case "/privacy":
			op, reason = opPage, servePage(sw, r, privacyPage)
		case "/data-deletion":
			op, reason = opPage, servePage(sw, r, dataDeletionPage)
		case "/threads/oauth/start":
			op, reason = opOAuthStart, s.oauthStart(sw, r)
		case "/threads/oauth/callback":
			op, reason = opOAuthCallback, s.oauthCallback(sw, r)
		case "/threads/webhooks":
			op, reason = s.webhook(sw, r)
		default:
			http.NotFound(sw, r)
		}
	}
	level := slog.LevelInfo
	if sw.status >= 400 {
		level = slog.LevelWarn
	}
	logging.Emit(r.Context(), s.o.Logger, level, logging.Record{Event: logging.APIRequestFinished, Operation: op, Outcome: outcome(sw.status), Reason: reason, HTTPStatus: sw.status})
}

func (s *Server) allow(r *http.Request) bool {
	ok, _ := s.limiter.Allow(ratelimit.ClientIP(r))
	return ok
}

func outcome(status int) string {
	if status >= 400 {
		return "refused"
	}
	return "success"
}

func servePage(w http.ResponseWriter, r *http.Request, body string) string {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return "method"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, body)
	return "served"
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) (op, reason string) {
	switch r.Method {
	case http.MethodGet:
		return opWebhookVerify, s.webhookVerify(w, r)
	case http.MethodPost:
		return opWebhookEvent, s.webhookEvent(w, r)
	}
	w.Header().Set("Allow", "GET, POST")
	http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
	return opWebhookEvent, "method"
}

func (s *Server) webhookVerify(w http.ResponseWriter, r *http.Request) string {
	q := r.URL.Query()
	if q.Get("hub.mode") != "subscribe" || subtle.ConstantTimeCompare([]byte(q.Get("hub.verify_token")), []byte(s.o.VerifyToken.Expose())) != 1 {
		http.Error(w, "Forbidden.", http.StatusForbidden)
		return "verify_token"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, q.Get("hub.challenge"))
	return "verified"
}

// webhookPayload decodes only what the agent needs from a Threads delivery
// (topic, target_id, values.field, values.value.id and .username). The text and
// every other field are never decoded, so they cannot be stored or logged.
type webhookPayload struct {
	TargetID string `json:"target_id"`
	Values   struct {
		Field string `json:"field"`
		Value struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"value"`
	} `json:"values"`
}

func (s *Server) webhookEvent(w http.ResponseWriter, r *http.Request) string {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "Bad request.", http.StatusRequestEntityTooLarge)
		return "body"
	}
	sig, ok := strings.CutPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
	got, decodeErr := hex.DecodeString(sig)
	mac := hmac.New(sha256.New, []byte(s.o.AppSecret.Expose()))
	mac.Write(body)
	if !ok || decodeErr != nil || !hmac.Equal(got, mac.Sum(nil)) {
		http.Error(w, "Forbidden.", http.StatusForbidden)
		return "signature"
	}
	var p webhookPayload
	if json.Unmarshal(body, &p) != nil {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return "payload"
	}
	now := s.o.Now()
	agent, known := s.byUser[p.TargetID]
	v := p.Values
	if known && (v.Field == fieldReplies || v.Field == fieldMentions) && v.Value.ID != "" && !strings.EqualFold(v.Value.Username, agent.Username) {
		fresh, err := s.o.Store.RecordWebhookEvent(r.Context(), agent.ID, v.Field, v.Value.ID, now)
		if err == nil && fresh {
			err = s.o.Store.WakeOnWebhook(r.Context(), agent.ID, now, agent.MinWakeGap)
		}
		if err != nil {
			http.Error(w, "Unavailable.", http.StatusServiceUnavailable)
			return "store"
		}
	}
	w.WriteHeader(http.StatusOK)
	return "accepted"
}
