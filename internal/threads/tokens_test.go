package threads

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
)

type memSlot struct {
	sealed  []byte
	expires time.Time
	ok      bool
}

func (m *memSlot) GetSecret(context.Context, string, string) ([]byte, time.Time, bool, error) {
	return m.sealed, m.expires, m.ok, nil
}
func (m *memSlot) PutSecret(_ context.Context, _, _ string, sealed []byte, exp time.Time) error {
	m.sealed, m.expires, m.ok = sealed, exp, true
	return nil
}

type recorded struct{ secrets []string }

func (r *recorded) Add(s config.Secret) { r.secrets = append(r.secrets, s.Expose()) }

var tokenNow = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

func newTokens(t *testing.T, slot *memSlot, status int, notices *[]string) (*Tokens, *recorded, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/refresh_access_token" || r.URL.Query().Get("grant_type") != "th_refresh_token" || strings.Contains(r.URL.String(), "OLD") || r.Header.Get("Authorization") == "" {
			t.Errorf("bad refresh request %s", r.URL)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = w.Write(apiFixture(t, "refresh.json"))
	}))
	t.Cleanup(srv.Close)
	red := &recorded{}
	tk, err := NewTokens(TokensOptions{Slot: slot, Key: []byte(strings.Repeat("k", 32)), Seed: map[string]config.Secret{"a": config.NewSecret("OLD_SEED_TOKEN")},
		BaseURL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return tokenNow }, Redact: red,
		Notify: func(_ context.Context, _, text string) { *notices = append(*notices, text) }})
	if err != nil {
		t.Fatal(err)
	}
	return tk, red, &calls
}

func current(t *testing.T, tk *Tokens) string {
	t.Helper()
	s, err := tk.Source("a").Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s.Expose()
}

func TestTokensRefreshAtExpiryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remaining time.Duration
		refreshed bool
	}{
		{"just outside the window", RefreshWindow + time.Second, false},
		{"exactly at the window", RefreshWindow, true},
		{"inside the window", RefreshWindow - time.Second, true},
		{"expired", -time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slot := &memSlot{}
			var notices []string
			tk, _, calls := newTokens(t, slot, http.StatusOK, &notices)
			sealed, _ := tk.seal("STORED_TOKEN")
			slot.sealed, slot.expires, slot.ok = sealed, tokenNow.Add(tc.remaining), true
			if err := tk.Ensure(context.Background(), "a"); err != nil {
				t.Fatal(err)
			}
			want, wantCalls := "STORED_TOKEN", 0
			if tc.refreshed {
				want, wantCalls = "REFRESHED_SYNTHETIC_TOKEN", 1
			}
			if got := current(t, tk); got != want || *calls != wantCalls {
				t.Fatalf("token=%q calls=%d", got, *calls)
			}
		})
	}
}

func TestTokensRefreshedTokenIsSealedExpiresAndRedacted(t *testing.T) {
	slot := &memSlot{}
	var notices []string
	tk, red, _ := newTokens(t, slot, http.StatusOK, &notices)
	if err := tk.Ensure(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(slot.sealed), "REFRESHED_SYNTHETIC_TOKEN") || !slot.expires.Equal(tokenNow.Add(5184000*time.Second)) {
		t.Fatalf("slot not sealed or wrong expiry: %v", slot.expires)
	}
	if plain, err := tk.open(slot.sealed); err != nil || plain != "REFRESHED_SYNTHETIC_TOKEN" {
		t.Fatalf("round trip %q %v", plain, err)
	}
	if !contains(red.secrets, "REFRESHED_SYNTHETIC_TOKEN") || !contains(red.secrets, "OLD_SEED_TOKEN") {
		t.Fatalf("redactions=%v", red.secrets)
	}
	// A later wake uses the stored token and does not refresh again.
	tk2, _, calls := newTokens(t, slot, http.StatusOK, &notices)
	tk2.o.Key = tk.o.Key
	if err := tk2.Ensure(context.Background(), "a"); err != nil || *calls != 0 || current(t, tk2) != "REFRESHED_SYNTHETIC_TOKEN" {
		t.Fatalf("second wake calls=%d err=%v", *calls, err)
	}
}

func TestTokensFailedRefreshNotifiesAndKeepsCurrentToken(t *testing.T) {
	slot := &memSlot{}
	var notices []string
	tk, _, _ := newTokens(t, slot, http.StatusBadRequest, &notices)
	if err := tk.Ensure(context.Background(), "a"); err != nil {
		t.Fatalf("a failed refresh must not fail the wake: %v", err)
	}
	if len(notices) != 1 || current(t, tk) != "OLD_SEED_TOKEN" || slot.ok {
		t.Fatalf("notices=%v token=%q stored=%v", notices, current(t, tk), slot.ok)
	}
}

func TestTokensRejectWrongKeyAndMissingToken(t *testing.T) {
	slot := &memSlot{}
	var notices []string
	tk, _, _ := newTokens(t, slot, http.StatusOK, &notices)
	sealed, _ := tk.seal("X")
	slot.sealed, slot.expires, slot.ok = sealed, tokenNow.Add(time.Hour*24*30), true
	tk.o.Key = []byte(strings.Repeat("z", 32))
	if err := tk.Ensure(context.Background(), "a"); err == nil {
		t.Fatal("token sealed under another key was opened")
	}
	tk, _, _ = newTokens(t, &memSlot{}, http.StatusOK, &notices)
	if err := tk.Ensure(context.Background(), "unknown-agent"); err == nil {
		t.Fatal("agent without a token accepted")
	}
	if _, err := tk.Source("never-loaded").Token(context.Background()); err == nil {
		t.Fatal("unloaded token returned")
	}
	if _, err := NewTokens(TokensOptions{Slot: slot, Key: []byte("short"), Now: time.Now}); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := DecodeKey("not base64"); err == nil {
		t.Fatal("bad key accepted")
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// The recorded fixtures hold no credential, owner id or username of the
// tester account.
func TestAPIFixturesCarryNoSecrets(t *testing.T) {
	files, _ := filepath.Glob("testdata/api/*.json")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"THAA", "IGAA", "EAA", "access_token\":\"T", "fbcdn"} {
			if strings.Contains(string(raw), bad) {
				t.Errorf("%s contains %q", f, bad)
			}
		}
	}
}

func TestTokensInstallSealsRedactsAndReplacesStoredToken(t *testing.T) {
	slot := &memSlot{}
	var notices []string
	tk, red, calls := newTokens(t, slot, http.StatusOK, &notices)
	if err := tk.Install(context.Background(), "a", config.NewSecret("OAUTH_TOKEN"), 60*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(slot.sealed), "OAUTH_TOKEN") || !slot.expires.Equal(tokenNow.Add(60*24*time.Hour)) || !contains(red.secrets, "OAUTH_TOKEN") || current(t, tk) != "OAUTH_TOKEN" {
		t.Fatalf("install: expires=%v redactions=%v", slot.expires, red.secrets)
	}
	if err := tk.Ensure(context.Background(), "a"); err != nil || *calls != 0 || current(t, tk) != "OAUTH_TOKEN" {
		t.Fatalf("installed token not used: calls=%d err=%v", *calls, err)
	}
}
