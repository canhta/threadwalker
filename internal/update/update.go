// Package update replaces the installed binary with the latest signed GitHub
// release: verify, check against the live data, swap, restart, roll back.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ReleasePublicKey is the base64 ed25519 public key whose private half signs
// release tarballs in .github/workflows/release.yml.
const ReleasePublicKey = "IfoEPnAVoi9TG/u8GJ9W3ImNKBvHoKicxjWL9eFOSwA="

// ManualMarker, at the start of a line in the release notes (any case), marks a
// release the updater must not install by itself.
const ManualMarker = "manual step:"

// Steps and outcomes reported to Updater.Event.
const (
	StepFetch    = "fetch"
	StepVerify   = "verify"
	StepCheck    = "check"
	StepWait     = "wait"
	StepSwap     = "swap"
	StepRestart  = "restart"
	StepHealth   = "health"
	StepRollback = "rollback"

	OutcomeOK     = "success"
	OutcomeFailed = "error"
)

var (
	ErrDowngrade  = errors.New("refusing a downgrade")
	ErrMajor      = errors.New("refusing a major-version jump")
	ErrManual     = errors.New("release needs a manual step")
	ErrDevBuild   = errors.New("a development build cannot compare versions")
	ErrChecksum   = errors.New("sha256 mismatch")
	ErrSignature  = errors.New("signature mismatch")
	ErrBusy       = errors.New("a wake is still running")
	ErrCheck      = errors.New("new binary failed its check")
	ErrRolledBack = errors.New("new binary did not become healthy; rolled back")
)

// Updater holds every outside effect so tests run against fakes.
type Updater struct {
	HTTP      *http.Client
	APIBase   string // https://api.github.com
	Repo      string // owner/name
	Current   string
	Arch      string
	Binary    string // installed binary path
	CachePath string // ETag and release body of the last "latest" answer
	PubKey    ed25519.PublicKey

	// Verify runs the new binary's check against the live agents dir and database.
	Verify func(ctx context.Context, binary string) error
	// Busy reports whether a wake is running now.
	Busy func(ctx context.Context) (bool, error)
	// Restart restarts the services; Healthy is nil once they are active and
	// doctor passes.
	Restart func(ctx context.Context) error
	Healthy func(ctx context.Context) error
	Sleep   func(ctx context.Context, d time.Duration) error
	Event   func(step, outcome, reason string)
	Notify  func(ctx context.Context, text string)

	BusyWait, BusyPoll     time.Duration
	HealthWait, HealthPoll time.Duration
}

// Options selects what Run does.
type Options struct {
	CheckOnly bool
	Version   string // vX.Y.Z; empty means the latest release
}

type release struct {
	Tag        string `json:"tag_name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

type cache struct {
	ETag    string  `json:"etag"`
	Release release `json:"release"`
}

// Run returns the tag it installed, or, with CheckOnly, the tag available. An
// empty tag with a nil error means the installation is current.
func (u *Updater) Run(ctx context.Context, o Options) (string, error) {
	rel, err := u.fetch(ctx, o.Version)
	if err != nil {
		return "", u.fail(StepFetch, "fetch_failed", err)
	}
	cur, err := parseVersion(u.Current)
	if err != nil {
		return "", u.fail(StepFetch, "dev_build", ErrDevBuild)
	}
	next, err := parseVersion(rel.Tag)
	if err != nil {
		return "", u.fail(StepFetch, "bad_tag", err)
	}
	switch {
	case next == cur:
		return "", nil
	case next[0] != cur[0]:
		return "", u.fail(StepFetch, "major_jump", ErrMajor)
	case next[1] < cur[1] || next[1] == cur[1] && next[2] < cur[2]:
		return "", u.fail(StepFetch, "downgrade", ErrDowngrade)
	}
	for _, line := range strings.Split(rel.Body, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), ManualMarker) {
			return "", u.fail(StepFetch, "manual_step", ErrManual)
		}
	}
	if o.CheckOnly {
		return rel.Tag, nil
	}

	newBin, err := u.stage(ctx, rel)
	if err != nil {
		return "", err
	}
	defer os.Remove(newBin)
	if err = u.Verify(ctx, newBin); err != nil {
		return "", u.fail(StepCheck, "check_failed", fmt.Errorf("%w: %v", ErrCheck, err))
	}
	u.event(StepCheck, OutcomeOK, "check_passed")
	if err = u.waitIdle(ctx); err != nil {
		return "", err
	}

	prev := u.Binary + ".prev"
	_ = os.Remove(prev)
	if err = os.Link(u.Binary, prev); err != nil {
		return "", u.fail(StepSwap, "swap_failed", fmt.Errorf("keeping previous binary: %w", err))
	}
	if err = os.Rename(newBin, u.Binary); err != nil {
		return "", u.fail(StepSwap, "swap_failed", fmt.Errorf("replacing binary: %w", err))
	}
	u.event(StepSwap, OutcomeOK, "swapped")

	if err = u.restartHealthy(ctx); err != nil {
		u.event(StepHealth, OutcomeFailed, "unhealthy")
		if rbErr := os.Rename(prev, u.Binary); rbErr != nil {
			return "", u.fail(StepRollback, "rollback_failed", rbErr)
		}
		if rsErr := u.Restart(context.WithoutCancel(ctx)); rsErr != nil {
			return "", u.fail(StepRollback, "rollback_restart_failed", rsErr)
		}
		u.event(StepRollback, OutcomeOK, "rolled_back")
		u.notify(ctx, fmt.Sprintf("Update to %s failed and was rolled back.", rel.Tag))
		return "", ErrRolledBack
	}
	u.event(StepHealth, OutcomeOK, "healthy")
	u.notify(ctx, fmt.Sprintf("Updated from v%s to %s.", strings.TrimPrefix(u.Current, "v"), rel.Tag))
	return rel.Tag, nil
}

func (u *Updater) restartHealthy(ctx context.Context) error {
	if err := u.Restart(ctx); err != nil {
		return err
	}
	u.event(StepRestart, OutcomeOK, "restarted")
	deadline := time.Duration(0)
	for {
		if err := u.Sleep(ctx, u.HealthPoll); err != nil {
			return err
		}
		deadline += u.HealthPoll
		err := u.Healthy(ctx)
		if err == nil {
			return nil
		}
		if deadline >= u.HealthWait {
			return err
		}
	}
}

func (u *Updater) waitIdle(ctx context.Context) error {
	for waited := time.Duration(0); ; waited += u.BusyPoll {
		busy, err := u.Busy(ctx)
		if err != nil {
			return u.fail(StepWait, "busy_check_failed", err)
		}
		if !busy {
			return nil
		}
		if waited >= u.BusyWait {
			return u.fail(StepWait, "wake_running", ErrBusy)
		}
		if err = u.Sleep(ctx, u.BusyPoll); err != nil {
			return err
		}
	}
}

// stage downloads the tarball, verifies sha256 and signature, and writes the
// binary beside the installed one so the swap is a rename.
func (u *Updater) stage(ctx context.Context, rel release) (string, error) {
	base := "threadz_" + strings.TrimPrefix(rel.Tag, "v") + "_linux_" + u.Arch + ".tar.gz"
	tgz, err := u.asset(ctx, rel, base)
	if err != nil {
		return "", u.fail(StepFetch, "download_failed", err)
	}
	sum, err := u.asset(ctx, rel, base+".sha256")
	if err != nil {
		return "", u.fail(StepFetch, "download_failed", err)
	}
	sig, err := u.asset(ctx, rel, base+".sig")
	if err != nil {
		return "", u.fail(StepFetch, "download_failed", err)
	}
	digest := sha256.Sum256(tgz)
	if f := strings.Fields(string(sum)); len(f) == 0 || !strings.EqualFold(f[0], hex.EncodeToString(digest[:])) {
		return "", u.fail(StepVerify, "checksum_mismatch", ErrChecksum)
	}
	if !ed25519.Verify(u.PubKey, tgz, sig) {
		return "", u.fail(StepVerify, "signature_mismatch", ErrSignature)
	}
	u.event(StepVerify, OutcomeOK, "verified")
	bin, err := extractBinary(tgz)
	if err != nil {
		return "", u.fail(StepVerify, "bad_archive", err)
	}
	path := u.Binary + ".new"
	if err = os.WriteFile(path, bin, 0o755); err != nil {
		return "", u.fail(StepVerify, "write_failed", fmt.Errorf("writing new binary: %w", err))
	}
	return path, nil
}

func extractBinary(tgz []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, fmt.Errorf("reading archive: %w", err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("finding threadz in archive: %w", err)
		}
		if strings.TrimPrefix(h.Name, "./") == "threadz" && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 256<<20))
		}
	}
}

func (u *Updater) asset(ctx context.Context, rel release, name string) ([]byte, error) {
	for _, a := range rel.Assets {
		if a.Name == name {
			res, err := u.get(ctx, a.URL, "")
			if err != nil {
				return nil, err
			}
			defer res.Body.Close()
			return io.ReadAll(io.LimitReader(res.Body, 256<<20))
		}
	}
	return nil, fmt.Errorf("release has no asset %s", name)
}

func (u *Updater) get(ctx context.Context, url, etag string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("requesting %s: %w", url, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := u.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting %s: %w", url, err)
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNotModified {
		res.Body.Close()
		return nil, fmt.Errorf("requesting %s: status %d", url, res.StatusCode)
	}
	return res, nil
}

// fetch reads the latest release with an ETag, or a tagged one. Draft and
// pre-releases are refused.
func (u *Updater) fetch(ctx context.Context, version string) (release, error) {
	url := u.APIBase + "/repos/" + u.Repo + "/releases/latest"
	var c cache
	if version != "" {
		url = u.APIBase + "/repos/" + u.Repo + "/releases/tags/" + version
	} else if b, err := os.ReadFile(u.CachePath); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	res, err := u.get(ctx, url, c.ETag)
	if err != nil {
		return release{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		return c.Release, nil
	}
	var rel release
	if err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&rel); err != nil {
		return release{}, fmt.Errorf("decoding release: %w", err)
	}
	if rel.Draft || rel.Prerelease {
		return release{}, fmt.Errorf("release %s is not a stable release", rel.Tag)
	}
	if version == "" && res.Header.Get("ETag") != "" {
		if b, err := json.Marshal(cache{ETag: res.Header.Get("ETag"), Release: rel}); err == nil {
			_ = os.WriteFile(u.CachePath, b, 0o644)
		}
	}
	return rel, nil
}

func (u *Updater) fail(step, reason string, err error) error {
	u.event(step, OutcomeFailed, reason)
	return err
}

func (u *Updater) event(step, outcome, reason string) {
	if u.Event != nil {
		u.Event(step, outcome, reason)
	}
}

func (u *Updater) notify(ctx context.Context, text string) {
	if u.Notify != nil {
		u.Notify(context.WithoutCancel(ctx), text)
	}
}

// parseVersion reads vX.Y.Z or X.Y.Z; anything else (dev, pre-release) is an error.
func parseVersion(s string) ([3]int, error) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("version %q is not X.Y.Z", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("version %q is not X.Y.Z", s)
		}
		v[i] = n
	}
	return v, nil
}

// Window is the local hours [Start, End) in which scheduled updates run; the
// zero value allows every hour.
type Window struct{ Start, End int }

// ParseWindow reads "H-H" (0-24, wrapping past midnight when Start > End) or "".
func ParseWindow(s string) (Window, error) {
	if s == "" {
		return Window{}, nil
	}
	a, b, ok := strings.Cut(s, "-")
	start, err1 := strconv.Atoi(a)
	end, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || start < 0 || start > 23 || end < 1 || end > 24 || start == end {
		return Window{}, fmt.Errorf("update window %q must be H-H with hours 0-24, e.g. 2-5", s)
	}
	return Window{start, end}, nil
}

func (w Window) Allows(t time.Time) bool {
	if w == (Window{}) {
		return true
	}
	h := t.Hour()
	if w.Start < w.End {
		return h >= w.Start && h < w.End
	}
	return h >= w.Start || h < w.End
}

// DecodePublicKey decodes ReleasePublicKey.
func DecodePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("release public key is malformed")
	}
	return raw, nil
}

// DefaultRequestPath is where the unprivileged service and ctl drop an update
// request; deploy/threadz-update.path starts the root updater when it appears.
const DefaultRequestPath = "/run/threadz/update-request"

// RequestFile writes an update request to DefaultRequestPath.
func RequestFile() error { return Request(DefaultRequestPath) }

// Request writes an update request at path.
func Request(path string) error {
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return fmt.Errorf("writing update request: %w", err)
	}
	return nil
}

// ConsumeRequest removes the request file and reports whether it existed.
func ConsumeRequest(path string) bool { return os.Remove(path) == nil }
