package dashboard

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"github.com/canhta/threadz/internal/ratelimit"
	"html/template"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/logging"
)

//go:embed templates/index.html static/dashboard.css static/local-time.js static/fonts/* static/brand/*
var assets embed.FS

var pageTemplate = template.Must(template.New("index.html").Funcs(template.FuncMap{
	"statusText":  statusText,
	"formatCount": formatCount,
	"deltaTone":   deltaTone,
	"formatUSD":   formatUSD,
	"plural":      plural,
	"cssHref":     func() string { return cssHref },
	"jsHref":      func() string { return jsHref },
	"iso":         func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"tm":          tm,
}).ParseFS(assets, "templates/index.html"))
var stylesheet = mustAsset("static/dashboard.css")
var localTimeJS = mustAsset("static/local-time.js")
var jsHref = "/local-time.js?v=" + strings.Trim(strings.TrimPrefix(etagFor(localTimeJS), "W/"), `"`)

// tm marks a displayed moment: the server text stays as the no-script
// fallback and local-time.js rewrites it in the viewer's zone.
func tm(format string, t time.Time, text string) template.HTML {
	if t.IsZero() {
		return template.HTML(template.HTMLEscapeString(text))
	}
	return template.HTML(`<time datetime="` + t.UTC().Format(time.RFC3339) + `" data-fmt="` + format + `">` + template.HTMLEscapeString(text) + `</time>`)
}

// cssHref carries the stylesheet's content hash, so a deploy that changes it
// never pairs new markup with a cached old stylesheet.
var cssHref = "/dashboard.css?v=" + strings.Trim(strings.TrimPrefix(etagFor(stylesheet), "W/"), `"`)

var fontAssets = map[string]string{
	"/fonts/bvp-latin-400.woff2":      "static/fonts/bvp-latin-400.woff2",
	"/fonts/bvp-latin-700.woff2":      "static/fonts/bvp-latin-700.woff2",
	"/fonts/bvp-vietnamese-400.woff2": "static/fonts/bvp-vietnamese-400.woff2",
	"/fonts/bvp-vietnamese-700.woff2": "static/fonts/bvp-vietnamese-700.woff2",
}

var brandAssets = map[string]struct{ file, contentType string }{
	"/favicon.svg":          {"static/brand/favicon.svg", "image/svg+xml"},
	"/favicon.ico":          {"static/brand/favicon.ico", "image/x-icon"},
	"/apple-touch-icon.png": {"static/brand/threadz-app-icon-180.png", "image/png"},
	"/og.png":               {"static/brand/og.png", "image/png"},
}

func mustAsset(path string) []byte {
	data, err := assets.ReadFile(path)
	if err != nil {
		panic("embedded dashboard asset unavailable")
	}
	return data
}

type pageQuery struct {
	agent     Agent
	days      int
	view      postView
	canonical string
	// origin is the scheme and host this request arrived on, for the canonical
	// link and the Open Graph tags. It joins the cache key, so one cached page
	// never carries another hostname's URL.
	origin string
}

// requestOrigin trusts X-Forwarded-Proto because the dashboard is only reachable
// through the tunnel that sets it; a direct loopback request is plain http.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		scheme = "https"
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func (h *Handler) parseQuery(u *url.URL) (pageQuery, error) {
	q := pageQuery{agent: h.agents[0], days: 7}
	if len(u.RawQuery) > 1024 {
		return q, errors.New("query too long")
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return q, err
	}
	for key, list := range values {
		// Threads and Facebook append fbclid to every outbound link, and a
		// link in bio carries utm_*. Refusing them makes the shared link
		// unreachable; only agent and range are ever read, and the cache key
		// is built from those alone, so the rest ride along ignored.
		if key != "agent" && key != "range" && key != "sort" && key != "show" {
			continue
		}
		if len(list) != 1 {
			return q, errors.New("duplicate parameter")
		}
		switch key {
		case "agent":
			a, ok := h.agent(list[0])
			if !ok {
				return q, errors.New("unknown agent")
			}
			q.agent = a
		case "range":
			days, err := strconv.Atoi(list[0])
			if err != nil || !slices.Contains(rangeDays, days) || strconv.Itoa(days) != list[0] {
				return q, errors.New("invalid range")
			}
			q.days = days
		case "sort":
			if !slices.Contains(postOrders, list[0]) {
				return q, errors.New("invalid sort")
			}
			q.view.order = list[0]
		case "show":
			n, err := strconv.Atoi(list[0])
			if err != nil || !slices.Contains(postShows, n) || strconv.Itoa(n) != list[0] {
				return q, errors.New("invalid show")
			}
			q.view.show = n
		}
	}
	q.canonical = "agent=" + url.QueryEscape(q.agent.ID) + "&range=" + strconv.Itoa(q.days) +
		"&sort=" + q.view.orderOr() + "&show=" + strconv.Itoa(q.view.showOr())
	return q, nil
}

func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	ip := ratelimit.ClientIP(r)
	if ok, retry := h.limiter.Allow(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeError(w, r, http.StatusTooManyRequests, "Too many requests. Try again shortly.\n", "rate_limited")
		h.emitRequest(r, "request", "rate_limited", http.StatusTooManyRequests)
		logging.Emit(r.Context(), h.logger, slog.LevelWarn, logging.Record{Event: logging.DashboardRateLimited, Operation: "request", Outcome: "blocked", Reason: "rate_limited", HTTPStatus: http.StatusTooManyRequests})
		return
	}
	path := r.URL.EscapedPath()
	switch {
	case path == "/":
		h.servePage(w, r)
	case path == "/dashboard.css":
		h.serveStatic(w, r, "style", "text/css; charset=utf-8", stylesheet)
	case path == "/local-time.js":
		h.serveStatic(w, r, "script", "text/javascript; charset=utf-8", localTimeJS)
	case fontAssets[path] != "":
		h.serveStatic(w, r, "font", "font/woff2", mustAsset(fontAssets[path]))
	case brandAssets[path].file != "":
		h.serveStatic(w, r, "brand", brandAssets[path].contentType, mustAsset(brandAssets[path].file))
	default:
		writeError(w, r, http.StatusNotFound, "Page not found.\n", "unknown_path")
		h.emitRequest(r, "unknown", "unknown_path", http.StatusNotFound)
	}
}

func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request, operation, contentType string, body []byte) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, r, http.StatusMethodNotAllowed, "Read-only page; use GET or HEAD.\n", "method_not_allowed")
		h.emitRequest(r, operation, "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	etag := etagFor(body)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != "HEAD" {
		_, _ = w.Write(body)
	}
	h.emitRequest(r, operation, "served", http.StatusOK)
}

func (h *Handler) servePage(w http.ResponseWriter, r *http.Request) {
	operation := "overview"
	status, reason := http.StatusOK, "served"
	defer func() {
		logging.Emit(r.Context(), h.logger, slog.LevelInfo, logging.Record{Event: logging.DashboardRequestFinished, Operation: operation, Outcome: map[bool]string{true: "success", false: "error"}[status < 400], Reason: reason, HTTPStatus: status})
	}()
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		status, reason = http.StatusMethodNotAllowed, "method_not_allowed"
		writeError(w, r, status, "Read-only page; use GET or HEAD.\n", reason)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	q, err := h.parseQuery(r.URL)
	q.origin = requestOrigin(r)
	if err != nil {
		status, reason = http.StatusBadRequest, "query_not_supported"
		writeError(w, r, status, "Only agent and range are accepted.\n", reason)
		return
	}
	now := h.now().UTC()
	var resp pageResponse
	var stale bool
	resp, stale, err = h.cache.do(q.origin+"/?"+q.canonical, func() (pageResponse, error) {
		if !h.acquire() {
			return pageResponse{}, errBusy
		}
		defer h.release()
		return h.render(ctxWithRequest(r), q, now)
	})
	if err != nil {
		if errors.Is(err, errBusy) {
			w.Header().Set("Retry-After", "1")
			status, reason = http.StatusServiceUnavailable, "busy"
			writeError(w, r, status, "The dashboard is busy. Try again.\n", reason)
			return
		}
		status, reason = http.StatusServiceUnavailable, "state_unavailable"
		writeError(w, r, status, "Activity is temporarily unavailable. Please try again.\n", reason)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(h.ttl.Seconds())))
	w.Header().Set("ETag", resp.etag)
	w.Header().Set("X-Cache", map[bool]string{true: "stale", false: "miss"}[stale])
	if stale {
		w.Header().Set("Warning", `110 - "Response is stale"`)
		reason = "stale_served"
	}
	if match := r.Header.Get("If-None-Match"); match != "" && match == resp.etag {
		status, reason = http.StatusNotModified, "not_modified"
		w.WriteHeader(status)
		return
	}
	status = resp.status
	w.Header().Set("Content-Type", resp.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(resp.body)))
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		_, _ = w.Write(resp.body)
	}
}

func (h *Handler) emitRequest(r *http.Request, operation, reason string, status int) {
	logging.Emit(r.Context(), h.logger, slog.LevelInfo, logging.Record{Event: logging.DashboardRequestFinished, Operation: operation, Outcome: map[bool]string{true: "success", false: "error"}[status < 400], Reason: reason, HTTPStatus: status})
}

func (h *Handler) render(ctx context.Context, q pageQuery, now time.Time) (pageResponse, error) {
	p, err := h.readPage(ctx, q.agent, q.days, q.view, now)
	if err != nil {
		return pageResponse{}, queryError(ctx)
	}
	if q.origin != "" {
		p.Canonical = q.origin + "/?" + q.canonical
		p.OGImage = q.origin + "/og.png"
	}
	var buffer bytes.Buffer
	if err = pageTemplate.ExecuteTemplate(&buffer, "page", p); err != nil {
		return pageResponse{}, err
	}
	body := buffer.Bytes()
	logging.Emit(ctx, h.logger, slog.LevelDebug, logging.Record{Event: logging.DashboardPageRendered, AgentID: q.agent.ID, Operation: "render", Outcome: "success", Reason: "cache_miss", Steps: q.days})
	return pageResponse{status: http.StatusOK, body: body, contentType: "text/html; charset=utf-8", etag: etagFor(body)}, nil
}

func (h *Handler) acquire() bool {
	select {
	case h.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (h *Handler) release() { <-h.sem }

func writeError(w http.ResponseWriter, r *http.Request, status int, body, reason string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		_, _ = w.Write([]byte(body))
	}
}

var errBusy = errors.New("dashboard busy")

func ctxWithRequest(r *http.Request) context.Context { return r.Context() }

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
	listener = newLimitedListener(listener, h.maxConns)
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(discard{}, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
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

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
