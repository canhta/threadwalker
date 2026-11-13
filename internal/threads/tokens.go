package threads

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/canhta/threadz/internal/config"
)

// RefreshWindow is how close to expiry the long-lived token is refreshed.
const RefreshWindow = 7 * 24 * time.Hour

// SecretKeyEnv names the environment variable holding the base64 32-byte key
// that seals the stored token.
const SecretKeyEnv = "THREADZ_SECRET_KEY"

// SecretSlot is the DB slot holding one agent's sealed token and its expiry.
type SecretSlot interface {
	GetSecret(ctx context.Context, agentID, name string) (sealed []byte, expires time.Time, ok bool, err error)
	PutSecret(ctx context.Context, agentID, name string, sealed []byte, expires time.Time) error
}

// Redactions learns the refreshed token so logs and the model context never
// show it.
type Redactions interface{ Add(config.Secret) }

// TokensOptions configures the token manager. Seed holds each agent's token
// from the environment; it is used until a refreshed token is stored.
type TokensOptions struct {
	Slot    SecretSlot
	Key     []byte
	Seed    map[string]config.Secret
	BaseURL string
	HTTP    *http.Client
	Now     func() time.Time
	Redact  Redactions
	// Notify tells the owner a refresh failed; best effort.
	Notify func(ctx context.Context, agentID, text string)
}

// Tokens refreshes the long-lived token through refresh_access_token when it is
// within RefreshWindow of expiry and keeps the refreshed one sealed in the DB.
type Tokens struct {
	o   TokensOptions
	mu  sync.Mutex
	cur map[string]config.Secret
}

const tokenSlot = "threads_access_token"

// NewTokens validates the key (AES-256).
func NewTokens(o TokensOptions) (*Tokens, error) {
	if o.Slot == nil || len(o.Key) != 32 || o.Now == nil {
		return nil, errors.New("configuring threads tokens: slot, 32-byte key and clock required")
	}
	if o.BaseURL == "" {
		o.BaseURL = APIBaseURL
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{}
	}
	return &Tokens{o: o, cur: map[string]config.Secret{}}, nil
}

// DecodeKey decodes the base64 key from SecretKeyEnv.
func DecodeKey(raw string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(k) != 32 {
		return nil, fmt.Errorf("%s must be base64 of 32 bytes", SecretKeyEnv)
	}
	return k, nil
}

func (t *Tokens) seal(plain string) ([]byte, error) {
	block, err := aes.NewCipher(t.o.Key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (t *Tokens) open(sealed []byte) (string, error) {
	block, err := aes.NewCipher(t.o.Key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("sealed token too short")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	return string(plain), err
}

// ErrNoToken means neither the sealed slot nor the environment seed holds a token.
var ErrNoToken = errors.New("no threads access token")

// Ensure loads the agent's token (the stored one wins over the seed) and
// refreshes it when its expiry is unknown or within RefreshWindow. A failed
// refresh notifies the owner and leaves the current token in use; it is not an
// error, because the token may still be valid.
func (t *Tokens) Ensure(ctx context.Context, agentID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	token := t.o.Seed[agentID]
	var expires time.Time
	sealed, exp, ok, err := t.o.Slot.GetSecret(ctx, agentID, tokenSlot)
	if err != nil {
		return fmt.Errorf("reading stored threads token: %w", err)
	}
	if ok {
		plain, err := t.open(sealed)
		if err != nil {
			return errors.New("opening stored threads token failed")
		}
		token, expires = config.NewSecret(plain), exp
	}
	if token.Expose() == "" {
		return ErrNoToken
	}
	t.cur[agentID] = token
	if t.o.Redact != nil {
		t.o.Redact.Add(token)
	}
	if !expires.IsZero() && expires.Sub(t.o.Now()) > RefreshWindow {
		return nil
	}
	if err := t.refresh(ctx, agentID, token); err != nil && t.o.Notify != nil {
		t.o.Notify(ctx, agentID, "Threads token refresh failed; renew the token before it expires.")
	}
	return nil
}

func (t *Tokens) refresh(ctx context.Context, agentID string, token config.Secret) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	u := t.o.BaseURL + "/refresh_access_token?" + url.Values{"grant_type": {"th_refresh_token"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token.Expose())
	resp, err := t.o.HTTP.Do(req)
	if err != nil {
		return errors.New("threads token refresh request failed")
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out) != nil || out.AccessToken == "" || out.ExpiresIn <= 0 {
		return fmt.Errorf("%w: token refresh status %d", ErrAPI, resp.StatusCode)
	}
	return t.install(ctx, agentID, out.AccessToken, time.Duration(out.ExpiresIn)*time.Second)
}

// Install stores a long-lived token the OAuth flow obtained, replacing the
// stored one.
func (t *Tokens) Install(ctx context.Context, agentID string, token config.Secret, expiresIn time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.install(ctx, agentID, token.Expose(), expiresIn)
}

func (t *Tokens) install(ctx context.Context, agentID, token string, expiresIn time.Duration) error {
	sealed, err := t.seal(token)
	if err != nil {
		return err
	}
	next := config.NewSecret(token)
	if t.o.Redact != nil {
		t.o.Redact.Add(next)
	}
	if err = t.o.Slot.PutSecret(ctx, agentID, tokenSlot, sealed, t.o.Now().Add(expiresIn)); err != nil {
		return err
	}
	t.cur[agentID] = next
	return nil
}

type tokenSource struct {
	t  *Tokens
	id string
}

// Source is the TokenSource of one agent.
func (t *Tokens) Source(agentID string) TokenSource { return tokenSource{t, agentID} }

func (s tokenSource) Token(context.Context) (config.Secret, error) {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	tok, ok := s.t.cur[s.id]
	if !ok {
		return config.Secret{}, errors.New("threads token not loaded")
	}
	return tok, nil
}
