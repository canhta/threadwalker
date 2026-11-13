package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/telegram"
	"github.com/canhta/threadz/internal/update"
)

const (
	updateRepo    = "canhta/threadz"
	updateAPI     = "https://api.github.com"
	updateService = "threadz.service"
	// maxWake is how long an open wake row is believed; an older one is a crashed wake.
	maxWake = 30 * time.Minute
)

const updateUsage = `usage: threadz update [--check] [--version vX.Y.Z] [--channel stable]
                          [--scheduled] [--bin PATH] [--agents DIR] [--db PATH]

Install the latest signed GitHub release: verify sha256 and the ed25519
signature, run the new binary's check against the live agents and database,
wait for any running wake, swap the binary (the old one stays as <bin>.prev),
restart threadz.service and roll back if it is not healthy. Run as root.
Refuses a downgrade, a major-version jump and a release whose notes carry a
"Manual step:" line. --check only reports. --scheduled is for the systemd
timer: it obeys THREADZ_UPDATE_AUTO and THREADZ_UPDATE_WINDOW (local
hours, e.g. 2-5) unless an update request file exists.

Example:
  sudo threadz update --check
`

// scheduledAllowed applies THREADZ_UPDATE_AUTO and _WINDOW to a timer run.
func scheduledAllowed(getenv func(string) string, now time.Time) (bool, error) {
	auto, err := strconv.ParseBool(envOr(getenv, "THREADZ_UPDATE_AUTO", "false"))
	if err != nil {
		return false, errors.New("THREADZ_UPDATE_AUTO must be true or false")
	}
	window, err := update.ParseWindow(getenv("THREADZ_UPDATE_WINDOW"))
	if err != nil {
		return false, err
	}
	return auto && window.Allows(now), nil
}

func runUpdate(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	logger, _ := logging.New(stdout, "", "")
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	common := addCommonFlags(fs, getenv, "data/threadz.db")
	check := fs.Bool("check", false, "report only")
	version := fs.String("version", "", "install this release tag")
	channel := fs.String("channel", envOr(getenv, "THREADZ_UPDATE_CHANNEL", "stable"), "release channel")
	scheduled := fs.Bool("scheduled", false, "run from the timer")
	self, _ := os.Executable()
	bin := fs.String("bin", self, "installed binary to replace")
	help, err := parseFlags(fs, args)
	switch {
	case help:
		fmt.Fprint(stdout, updateUsage)
		return exitOK
	case err != nil || fs.NArg() != 0:
		fmt.Fprint(stderr, updateUsage)
		return exitUsage
	case *channel != "stable":
		fmt.Fprintln(stderr, "only the stable channel exists")
		return exitUsage
	}

	requested := false
	if *scheduled {
		requested = update.ConsumeRequest(update.DefaultRequestPath)
		if !requested {
			ok, err := scheduledAllowed(getenv, time.Now())
			if err != nil {
				fmt.Fprintln(stderr, err)
				return exitUsage
			}
			if !ok {
				return exitOK
			}
		}
	}

	pub, err := update.DecodePublicKey(update.ReleasePublicKey)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	httpClient := &http.Client{Timeout: 5 * time.Minute}
	notify := func(ctx context.Context, text string) { notifyOwner(ctx, httpClient, common, getenv, logger, text) }
	u := &update.Updater{
		HTTP: httpClient, APIBase: updateAPI, Repo: updateRepo,
		Current: versionText(), Arch: runtime.GOARCH, Binary: *bin,
		CachePath: *bin + ".release.json", PubKey: pub,
		Verify: func(ctx context.Context, newBin string) error {
			return runAs(ctx, *common.db, newBin, "check", "--agents", *common.agents, "--db", *common.db)
		},
		Busy: func(ctx context.Context) (bool, error) {
			if _, err := os.Stat(*common.db); err != nil {
				return false, nil
			}
			st, err := store.OpenReadOnly(ctx, *common.db)
			if err != nil {
				return false, fmt.Errorf("opening database: %w", err)
			}
			defer st.Close()
			return st.WakeOpenSince(ctx, time.Now().Add(-maxWake))
		},
		Restart: func(ctx context.Context) error {
			return exec.CommandContext(ctx, "systemctl", "restart", updateService).Run()
		},
		Healthy: func(ctx context.Context) error {
			if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", updateService).Run(); err != nil {
				return errors.New("service is not active")
			}
			return runAs(ctx, *common.db, *bin, "ctl", "doctor", "--agents", *common.agents, "--db", *common.db)
		},
		Sleep:    sleepCtx,
		Event:    func(step, outcome, reason string) { emitUpdate(ctx, logger, step, outcome, reason) },
		Notify:   notify,
		BusyWait: 10 * time.Minute, BusyPoll: 15 * time.Second,
		HealthWait: 2 * time.Minute, HealthPoll: 5 * time.Second,
	}
	tag, err := u.Run(ctx, update.Options{CheckOnly: *check, Version: *version})
	switch {
	case errors.Is(err, update.ErrRolledBack):
		fmt.Fprintln(stderr, err)
		return exitFailure
	case err != nil:
		fmt.Fprintln(stderr, err)
		if requested {
			notify(ctx, "Update failed: "+err.Error())
		}
		return exitFailure
	case tag == "":
		fmt.Fprintln(stdout, "up to date: "+versionText())
		if requested {
			notify(ctx, "Already up to date: "+versionText())
		}
	case *check:
		fmt.Fprintln(stdout, "update available: "+tag)
	default:
		fmt.Fprintln(stdout, "updated to "+tag)
	}
	return exitOK
}

func emitUpdate(ctx context.Context, logger *slog.Logger, step, outcome, reason string) {
	level := slog.LevelInfo
	if outcome != update.OutcomeOK {
		level = slog.LevelWarn
	}
	logging.Emit(ctx, logger, level, logging.Record{Event: logging.UpdateStep, Operation: step, Outcome: outcome, Reason: reason})
}

// runAs runs bin as the owner of the database path so root creates no files in
// the service's data directory.
func runAs(ctx context.Context, dbPath, bin string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = ownerAttr(dbPath)
	// The root-only env file is already loaded into this process; the child gets
	// its values through the environment and must not try to open the file.
	cmd.Env = append(os.Environ(), "THREADZ_ENV_FILE=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", filepath.Base(bin), args[0], err, tail(string(out), 400))
	}
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// notifyOwner sends text through the first enabled Telegram integration. A
// missing or broken integration is logged, never fatal: the update outcome
// stands either way.
func notifyOwner(ctx context.Context, client *http.Client, common commonFlags, getenv func(string) string, logger *slog.Logger, text string) {
	templates, err := common.load(config.Options{Getenv: getenv})
	if err == nil {
		for _, t := range templates {
			if !t.Telegram.Enabled {
				continue
			}
			var chat int64
			if chat, err = strconv.ParseInt(t.Telegram.OwnerChatID.Expose(), 10, 64); err != nil {
				break
			}
			var bot *telegram.Client
			if bot, err = telegram.NewClient(telegram.ClientOptions{Token: t.Telegram.BotToken, HTTPClient: client}); err != nil {
				break
			}
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err = bot.SendMessage(sendCtx, chat, text, 0)
			cancel()
			break
		}
	}
	outcome, reason := "success", "owner_notified"
	if err != nil {
		outcome, reason = "error", "owner_notice_failed"
	}
	emitUpdate(ctx, logger, "notify", outcome, reason)
}

// ctlUpdate runs the update when root, and otherwise asks the root updater
// through the request file the service user may write.
func ctlUpdate(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if os.Geteuid() == 0 {
		return runUpdate(ctx, args, getenv, stdout, stderr)
	}
	if len(args) > 0 {
		fmt.Fprintln(stderr, "update flags need root; run `sudo threadz update`")
		return exitUsage
	}
	if err := update.RequestFile(); err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	fmt.Fprintln(stdout, "update requested; the result arrives on Telegram")
	return exitOK
}
