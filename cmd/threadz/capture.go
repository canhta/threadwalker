package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/stdx"
	"github.com/canhta/threadz/internal/threads"
)

const defaultCaptureDir = "internal/threads/testdata/live"

// seedToken serves the token the environment holds; capture-api never
// refreshes or stores one.
type seedToken config.Secret

func (s seedToken) Token(context.Context) (config.Secret, error) { return config.Secret(s), nil }

// ctlCaptureAPI makes read-only Threads API calls with the environment's token
// and writes the redacted responses to --out, for the contract tests.
func ctlCaptureAPI(ctx context.Context, getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	f := newCtlFlags("capture-api", getenv)
	out := f.fs.String("out", defaultCaptureDir, "directory for the redacted responses")
	if rest, ok := f.parse(args, true, stderr); !ok || len(rest) != 0 {
		return exitUsage
	}
	templates, err := f.load(config.Options{Getenv: getenv})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	var tmpl *config.Template
	for i := range templates {
		if templates[i].ID == *f.agent {
			tmpl = &templates[i]
		}
	}
	if tmpl == nil || tmpl.Threads.Backend != "api" || tmpl.Threads.AccessToken.Expose() == "" {
		fmt.Fprintln(stderr, "capture-api needs an agent on the api Threads backend with its access token in the environment")
		return exitFailure
	}
	api, err := threads.NewAPI(threads.APIOptions{UserID: tmpl.Threads.UserID, Tokens: seedToken(tmpl.Threads.AccessToken), Wait: stdx.Sleep, Timeout: 20 * time.Second})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	files, err := api.Capture(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	token := []byte(tmpl.Threads.AccessToken.Expose())
	for name, b := range files {
		if bytes.Contains(b, token) {
			fmt.Fprintln(stderr, "refusing to write: "+name+" contains the access token")
			return exitFailure
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(stderr, "creating the output directory failed")
		return exitFailure
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(*out, name), b, 0o644); err != nil {
			fmt.Fprintln(stderr, "writing "+name+" failed")
			return exitFailure
		}
		fmt.Fprintln(stdout, filepath.Join(*out, name))
	}
	// Live files replace the hand-written stand-ins the directory ships with.
	_ = os.Remove(filepath.Join(*out, "STANDIN.txt"))
	return exitOK
}
