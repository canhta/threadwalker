// Package admin is the local control channel of the running service: HTTP over
// a Unix socket readable and writable by the service user only. Nothing is
// exposed on the network.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/canhta/threadz/internal/logging"
)

// Commands the live process serves. Everything else the operator CLI reads
// from the database or checks locally.
const (
	Status        = "status"
	Wake          = "wake"
	Pause         = "pause"
	Resume        = "resume"
	Kill          = "kill"
	MetricsRun    = "metrics_run"
	Prune         = "prune"
	TrendsRefresh = "trends_refresh"
	Context       = "context"
	OAuthLink     = "oauth_link"
	Cookies       = "cookies"
	ForgetLesson  = "forget_lesson"
)

var commands = map[string]bool{Status: true, Wake: true, Pause: true, Resume: true, Kill: true, MetricsRun: true, Prune: true, TrendsRefresh: true, Context: true, OAuthLink: true, Cookies: true, ForgetLesson: true}

// ErrBusy means another operator job is waiting or running.
var ErrBusy = errors.New("another operator job is waiting or running")

// ErrUnavailable means no service answers on the socket.
var ErrUnavailable = errors.New("admin socket is not answering; is the service running")

// Request is one operator command. Agent is empty only for commands that cover
// every agent.
type Request struct {
	Command string `json:"command"`
	Agent   string `json:"agent,omitempty"`
	Phase   string `json:"phase,omitempty"`
	DryRun  bool   `json:"dry_run,omitempty"`
	// Lesson is the lesson id for ForgetLesson.
	Lesson int64 `json:"lesson,omitempty"`
	// Cookies is the owner's cookie export for the Cookies command. It is never
	// logged: the audit record carries only the command, agent and outcome.
	Cookies string `json:"cookies,omitempty"`
}

// Handler executes one validated command.
type Handler func(context.Context, Request) (any, error)

type response struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
	Busy  bool            `json:"busy,omitempty"`
}

// Listen binds the socket with owner-only permissions. A socket file left by a
// dead process is replaced; a live one or any other file is an error.
func Listen(path string) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("binding admin socket: %s exists and is not a socket", path)
		}
		if conn, dialErr := net.DialTimeout("unix", path, time.Second); dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("binding admin socket: %s is in use", path)
		}
		if err = os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing stale admin socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("binding admin socket: %w", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("restricting admin socket: %w", err)
	}
	return ln, nil
}

// Serve answers commands until ctx ends, then closes the listener and removes
// the socket. Every command is audited with logging.AdminCommand.
func Serve(ctx context.Context, ln net.Listener, h Handler, logger *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin", func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(io.LimitReader(r.Body, 512<<10)).Decode(&req); err != nil || !commands[req.Command] {
			audit(r.Context(), logger, req, "error", "unknown_command")
			reply(w, http.StatusBadRequest, response{Error: "unknown command"})
			return
		}
		data, err := h(r.Context(), req)
		switch {
		case errors.Is(err, ErrBusy):
			audit(r.Context(), logger, req, "error", "busy")
			reply(w, http.StatusConflict, response{Error: ErrBusy.Error(), Busy: true})
		case err != nil:
			audit(r.Context(), logger, req, "error", "failed")
			reply(w, http.StatusUnprocessableEntity, response{Error: err.Error()})
		default:
			encoded, marshalErr := json.Marshal(data)
			if marshalErr != nil {
				audit(r.Context(), logger, req, "error", "failed")
				reply(w, http.StatusInternalServerError, response{Error: "encoding result failed"})
				return
			}
			audit(r.Context(), logger, req, "success", "completed")
			reply(w, http.StatusOK, response{Data: encoded})
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err := srv.Serve(ln)
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func audit(ctx context.Context, logger *slog.Logger, req Request, outcome, reason string) {
	level := slog.LevelInfo
	if outcome != "success" {
		level = slog.LevelWarn
	}
	op := req.Command
	if !commands[op] {
		op = "unknown"
	}
	logging.Emit(ctx, logger, level, logging.Record{Event: logging.AdminCommand, AgentID: req.Agent, Phase: req.Phase, Operation: op, Outcome: outcome, Reason: reason})
}

func reply(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Call sends one command to the service listening on path and decodes its
// result into out, which may be nil.
func Call(ctx context.Context, path string, req Request, out any) error {
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	defer client.CloseIdleConnections()
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://admin/admin", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sending admin command: %w", err)
	}
	res, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("sending admin command: %w", ctx.Err())
		}
		return fmt.Errorf("%w: %s", ErrUnavailable, path)
	}
	defer res.Body.Close()
	var parsed response
	if err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&parsed); err != nil {
		return fmt.Errorf("reading admin response: %w", err)
	}
	if parsed.Busy {
		return ErrBusy
	}
	if parsed.Error != "" {
		return errors.New(parsed.Error)
	}
	if out == nil {
		return nil
	}
	if err = json.Unmarshal(parsed.Data, out); err != nil {
		return fmt.Errorf("decoding admin result: %w", err)
	}
	return nil
}
