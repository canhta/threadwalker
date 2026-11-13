package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	t        *testing.T
	srv      *httptest.Server
	pub      ed25519.PublicKey
	priv     ed25519.PrivateKey
	tag      string
	body     string
	badSum   bool
	badSig   bool
	requests atomic.Int32
	notMod   atomic.Int32
	u        *Updater
	events   []string
	notes    []string
	restarts int
	checkErr error
	busy     []bool
	health   []error
}

func newFixture(t *testing.T, current, tag string) *fixture {
	t.Helper()
	f := &fixture{t: t, tag: tag}
	f.pub, f.priv, _ = ed25519.GenerateKey(rand.Reader)
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	dir := t.TempDir()
	bin := filepath.Join(dir, "threadz")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.u = &Updater{
		HTTP: f.srv.Client(), APIBase: f.srv.URL, Repo: "o/r", Current: current, Arch: "amd64",
		Binary: bin, CachePath: filepath.Join(dir, "cache.json"), PubKey: f.pub,
		Verify: func(_ context.Context, b string) error {
			if got, _ := os.ReadFile(b); string(got) != "new-"+f.tag {
				f.t.Errorf("verified binary = %q", got)
			}
			return f.checkErr
		},
		Busy: func(context.Context) (bool, error) {
			if len(f.busy) == 0 {
				return false, nil
			}
			b := f.busy[0]
			f.busy = f.busy[1:]
			return b, nil
		},
		Restart: func(context.Context) error { f.restarts++; return nil },
		Healthy: func(context.Context) error {
			if len(f.health) == 0 {
				return nil
			}
			e := f.health[0]
			f.health = f.health[1:]
			return e
		},
		Sleep:    func(context.Context, time.Duration) error { return nil },
		Event:    func(step, outcome, reason string) { f.events = append(f.events, step+":"+outcome+":"+reason) },
		Notify:   func(_ context.Context, s string) { f.notes = append(f.notes, s) },
		BusyWait: 30 * time.Second, BusyPoll: 10 * time.Second, HealthWait: 30 * time.Second, HealthPoll: 10 * time.Second,
	}
	return f
}

func (f *fixture) tarball() []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range []struct{ n, c string }{{"./threadz.service", "unit"}, {"./threadz", "new-" + f.tag}} {
		tw.WriteHeader(&tar.Header{Name: e.n, Mode: 0o755, Size: int64(len(e.c)), Typeflag: tar.TypeReg})
		tw.Write([]byte(e.c))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	base := "threadz_" + strings.TrimPrefix(f.tag, "v") + "_linux_amd64.tar.gz"
	tgz := f.tarball()
	sum := sha256.Sum256(tgz)
	sumText := hex.EncodeToString(sum[:]) + "  " + base
	if f.badSum {
		sumText = strings.Repeat("0", 64) + "  " + base
	}
	sig := ed25519.Sign(f.priv, tgz)
	if f.badSig {
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		sig = ed25519.Sign(other, tgz)
	}
	switch r.URL.Path {
	case "/repos/o/r/releases/latest", "/repos/o/r/releases/tags/" + f.tag:
		if r.Header.Get("If-None-Match") == `"e1"` {
			f.notMod.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"e1"`)
		fmt.Fprintf(w, `{"tag_name":%q,"body":%q,"assets":[{"name":%q,"browser_download_url":%q},{"name":%q,"browser_download_url":%q},{"name":%q,"browser_download_url":%q}]}`,
			f.tag, f.body, base, f.srv.URL+"/a/tgz", base+".sha256", f.srv.URL+"/a/sum", base+".sig", f.srv.URL+"/a/sig")
	case "/a/tgz":
		w.Write(tgz)
	case "/a/sum":
		fmt.Fprint(w, sumText)
	case "/a/sig":
		w.Write(sig)
	default:
		http.NotFound(w, r)
	}
}

func (f *fixture) binary() string {
	b, _ := os.ReadFile(f.u.Binary)
	return string(b)
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	t.Run("newer installs and notifies", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		tag, err := f.u.Run(ctx, Options{})
		if err != nil || tag != "v0.2.4" {
			t.Fatalf("Run = %q, %v", tag, err)
		}
		if f.binary() != "new-v0.2.4" || f.restarts != 1 {
			t.Fatalf("binary %q restarts %d", f.binary(), f.restarts)
		}
		if prev, _ := os.ReadFile(f.u.Binary + ".prev"); string(prev) != "old" {
			t.Fatalf(".prev = %q", prev)
		}
		if len(f.notes) != 1 || f.notes[0] != "Updated from v0.2.3 to v0.2.4." {
			t.Fatalf("notes = %v", f.notes)
		}
	})
	t.Run("same version does nothing", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.3")
		if tag, err := f.u.Run(ctx, Options{}); tag != "" || err != nil || f.binary() != "old" {
			t.Fatalf("Run = %q, %v", tag, err)
		}
	})
	for name, c := range map[string]struct {
		current, tag, body string
		want               error
	}{
		"older":       {"0.2.3", "v0.2.2", "", ErrDowngrade},
		"older minor": {"0.3.0", "v0.2.9", "", ErrDowngrade},
		"major":       {"0.2.3", "v1.0.0", "", ErrMajor},
		"manual":      {"0.2.3", "v0.2.4", "fixes\nManual step: fresh DB", ErrManual},
		"dev":         {"dev", "v0.2.4", "", ErrDevBuild},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, c.current, c.tag)
			f.body = c.body
			if _, err := f.u.Run(ctx, Options{}); !errors.Is(err, c.want) || f.binary() != "old" {
				t.Fatalf("err = %v, binary %q", err, f.binary())
			}
		})
	}
	t.Run("bad checksum", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		f.badSum = true
		if _, err := f.u.Run(ctx, Options{}); !errors.Is(err, ErrChecksum) || f.binary() != "old" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad signature", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		f.badSig = true
		if _, err := f.u.Run(ctx, Options{}); !errors.Is(err, ErrSignature) || f.binary() != "old" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("failing check leaves the binary and no staged file", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		f.checkErr = errors.New("schema version 1, this build needs 2")
		if _, err := f.u.Run(ctx, Options{}); !errors.Is(err, ErrCheck) || f.binary() != "old" || f.restarts != 0 {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(f.u.Binary + ".new"); !os.IsNotExist(err) {
			t.Fatal("staged binary left behind")
		}
	})
	t.Run("failing doctor rolls back", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		f.health = []error{errors.New("x"), errors.New("x"), errors.New("x")}
		if _, err := f.u.Run(ctx, Options{}); !errors.Is(err, ErrRolledBack) {
			t.Fatalf("err = %v", err)
		}
		if f.binary() != "old" || f.restarts != 2 {
			t.Fatalf("binary %q restarts %d", f.binary(), f.restarts)
		}
		if len(f.notes) != 1 || f.notes[0] != "Update to v0.2.4 failed and was rolled back." {
			t.Fatalf("notes = %v", f.notes)
		}
	})
	t.Run("doctor recovering in time keeps the update", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		f.health = []error{errors.New("starting")}
		if _, err := f.u.Run(ctx, Options{}); err != nil || f.binary() != "new-v0.2.4" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("waits for a wake then installs", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		f.busy = []bool{true, true, false}
		if _, err := f.u.Run(ctx, Options{}); err != nil || f.binary() != "new-v0.2.4" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a wake that never ends defers the update", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		f.busy = []bool{true, true, true, true, true}
		if _, err := f.u.Run(ctx, Options{}); !errors.Is(err, ErrBusy) || f.binary() != "old" || f.restarts != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("check only downloads nothing", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		if tag, err := f.u.Run(ctx, Options{CheckOnly: true}); tag != "v0.2.4" || err != nil || f.requests.Load() != 1 || f.binary() != "old" {
			t.Fatalf("Run = %q, %v, %d requests", tag, err, f.requests.Load())
		}
	})
	t.Run("pinned version", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.5")
		if tag, err := f.u.Run(ctx, Options{Version: "v0.2.5"}); tag != "v0.2.5" || err != nil {
			t.Fatalf("Run = %q, %v", tag, err)
		}
	})
	t.Run("second check reuses the cached release on 304", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		for range 2 {
			if tag, err := f.u.Run(ctx, Options{CheckOnly: true}); tag != "v0.2.4" || err != nil {
				t.Fatalf("Run = %q, %v", tag, err)
			}
		}
		if f.notMod.Load() != 1 {
			t.Fatalf("304 count = %d", f.notMod.Load())
		}
	})
	t.Run("server error", func(t *testing.T) {
		f := newFixture(t, "0.2.3", "v0.2.4")
		f.u.APIBase += "/nope"
		if _, err := f.u.Run(ctx, Options{}); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestWindow(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 13, h, 30, 0, 0, time.UTC) }
	for _, c := range []struct {
		spec    string
		hour    int
		allowed bool
	}{{"", 12, true}, {"2-5", 2, true}, {"2-5", 4, true}, {"2-5", 5, false}, {"22-3", 23, true}, {"22-3", 2, true}, {"22-3", 3, false}, {"0-24", 13, true}} {
		w, err := ParseWindow(c.spec)
		if err != nil || w.Allows(at(c.hour)) != c.allowed {
			t.Errorf("%q at %d: err %v", c.spec, c.hour, err)
		}
	}
	for _, bad := range []string{"x", "5-5", "2-", "-3", "25-3", "2-25"} {
		if _, err := ParseWindow(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPublicKey(t *testing.T) {
	if _, err := DecodePublicKey(ReleasePublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePublicKey("abc"); err == nil {
		t.Fatal("malformed key accepted")
	}
}

func TestRequestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-request")
	if ConsumeRequest(path) {
		t.Fatal("consumed a request that was never written")
	}
	if err := Request(path); err != nil {
		t.Fatal(err)
	}
	if !ConsumeRequest(path) || ConsumeRequest(path) {
		t.Fatal("a request must be consumed exactly once")
	}
	if err := Request(filepath.Join(path, "missing", "x")); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
}
