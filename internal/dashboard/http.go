package dashboard

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/canhta/threadwalker/internal/logging"
)

//go:embed templates/index.html static/dashboard.css
var assets embed.FS
var pageTemplate = template.Must(template.ParseFS(assets, "templates/index.html"))
var stylesheet = mustAsset("static/dashboard.css")

func mustAsset(path string) []byte {
	data, err := assets.ReadFile(path)
	if err != nil {
		panic("embedded dashboard asset unavailable")
	}
	return data
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	status, reason, operation := http.StatusOK, "served", "page"
	var body []byte
	defer func() {
		logging.Emit(r.Context(), h.logger, slog.LevelInfo, logging.Record{Event: logging.DashboardRequestFinished, Operation: operation, Outcome: map[bool]string{true: "success", false: "error"}[status < 400], Reason: reason, HTTPStatus: status})
	}()
	switch r.URL.EscapedPath() {
	case "/":
	case "/dashboard.css":
		operation = "style"
	default:
		operation = "unknown"
		status, reason = http.StatusNotFound, "unknown_path"
	}
	if status == 200 && r.Method != "GET" && r.Method != "HEAD" {
		status, reason = http.StatusMethodNotAllowed, "method_not_allowed"
		w.Header().Set("Allow", "GET, HEAD")
	}
	if status == 200 && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		status, reason = http.StatusBadRequest, "query_not_supported"
	}
	if status == 200 {
		if operation == "style" {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			body = stylesheet
		} else {
			p, err := h.Page(r.Context())
			if err != nil {
				status, reason = http.StatusServiceUnavailable, "state_unavailable"
			} else {
				var buffer bytes.Buffer
				if err = pageTemplate.ExecuteTemplate(&buffer, "page", p); err != nil {
					status, reason = http.StatusServiceUnavailable, "render_unavailable"
				} else {
					body = buffer.Bytes()
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
				}
			}
		}
	}
	if status != 200 {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		switch status {
		case 404:
			body = []byte("Page not found.\n")
		case 405:
			body = []byte("Read-only page; use GET or HEAD.\n")
		case 400:
			body = []byte("Query parameters are not supported.\n")
		default:
			body = []byte("Activity is temporarily unavailable. Please try again.\n")
		}
	}
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		_, _ = w.Write(body)
	}
}

// Serve accepts only a loopback TCP listener. Cancellation closes the HTTP
// server gracefully; no request can trigger an agent action or schema change.
func Serve(ctx context.Context, listener net.Listener, h *Handler) error {
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		return errors.New("serving dashboard: loopback listener required")
	}
	if h == nil {
		listener.Close()
		return errors.New("serving dashboard: handler required")
	}
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
	logging.Emit(ctx, h.logger, slog.LevelInfo, logging.Record{Event: logging.DashboardServerStarted, Operation: "server", Outcome: "started", Reason: "loopback"})
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if server.Shutdown(shutdown) != nil {
				server.Close()
			}
		case <-stopped:
		}
	}()
	err := server.Serve(listener)
	reason, outcome := "shutdown", "success"
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		reason, outcome = "serve_failed", "error"
	}
	logging.Emit(ctx, h.logger, slog.LevelInfo, logging.Record{Event: logging.DashboardServerFinished, Operation: "server", Outcome: outcome, Reason: reason})
	if outcome == "error" {
		return errors.New("serving dashboard: unavailable")
	}
	return nil
}
