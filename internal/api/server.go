// Package api is the read-only REST API. The contract lives in
// api/openapi.yaml; contract tests keep code and spec in step.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	apispec "github.com/helmerznl/nas-storage-analyzer/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/buildinfo"
	"github.com/helmerznl/nas-storage-analyzer/internal/query"
)

// Config tunes the server. Zero values pick defaults.
type Config struct {
	// QueryTimeout bounds one JSON request (default 15s).
	QueryTimeout time.Duration
	// CSVTimeout bounds one CSV export (default 30m).
	CSVTimeout time.Duration
	// CSVMaxRows caps the rows of one CSV export (default 1,000,000).
	CSVMaxRows int
	// Now is the clock for relative filters such as unused_days (tests).
	Now func() time.Time
	// Build says which version and release channel this is; GET /api/v1/version reports it.
	Build buildinfo.Info
}

// Server implements http.Handler.
type Server struct {
	q    *query.DB
	pool *pgxpool.Pool
	log  *slog.Logger
	cfg  Config
	m    *metrics
	mux  *http.ServeMux
	auth *auth.Service
	opts Options
	// patterns lists the registered routes ("GET /api/v1/shares"), which the
	// contract test compares with the OpenAPI document.
	patterns []string

	catMu sync.Mutex
	cats  map[int16]string
}

// New builds the API on top of a read-only pool. Pass Options to require
// sign-in; without them every endpoint is open (tests and benchmarks only).
func New(pool *pgxpool.Pool, log *slog.Logger, cfg Config, opts ...Options) *Server {
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = 15 * time.Second
	}
	if cfg.CSVTimeout <= 0 {
		cfg.CSVTimeout = 30 * time.Minute
	}
	if cfg.CSVMaxRows <= 0 {
		cfg.CSVMaxRows = 1_000_000
	}
	q := query.New(pool)
	s := &Server{q: q, pool: pool, log: log, cfg: cfg, mux: http.NewServeMux()}
	if len(opts) > 0 {
		s.opts, s.auth = opts[0], opts[0].Auth
	}
	s.m = newMetrics(q, pool, log)
	s.routes()
	return s
}

func (s *Server) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// Routes returns the registered "METHOD /path" patterns, without the web UI.
func (s *Server) Routes() []string { return append([]string(nil), s.patterns...) }

func (s *Server) handleFunc(pattern string, f http.HandlerFunc) {
	s.patterns = append(s.patterns, pattern)
	s.mux.HandleFunc(pattern, f)
}

func (s *Server) routes() {
	h := func(pattern string, lv level, fn func(http.ResponseWriter, *http.Request) error) {
		s.handleFunc(pattern, s.guard(lv, fn))
	}
	s.handleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})
	s.handleFunc("GET /readyz", s.ready)
	// Public on purpose: the sign-in screen already shows the beta label, and nothing here is secret.
	s.handleFunc("GET /api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		build := s.cfg.Build
		if build.Version == "" && build.Channel == "" {
			build = buildinfo.New("", "", "")
		}
		if err := writeJSON(w, build); err != nil {
			s.log.Error("writing the version", "error", err)
		}
	})
	metrics := promhttp.HandlerFor(s.m.reg, promhttp.HandlerOpts{})
	s.handleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if s.auth != nil && !s.metricsAllowed(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			s.problemCode(w, r, http.StatusUnauthorized, "unauthorized", "Send the metrics token as a Bearer token.", "unauthenticated")
			return
		}
		metrics.ServeHTTP(w, r)
	})
	s.handleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(apispec.Spec)
	})

	const v = "/api/v1"
	h("GET "+v+"/shares", lvViewer, s.listShares)
	h("GET "+v+"/shares/{share}", lvViewer, s.getShare)
	h("GET "+v+"/shares/{share}/scans", lvViewer, s.listScans)
	h("GET "+v+"/shares/{share}/scans/{scan}/errors", lvViewer, s.listScanErrors)
	h("GET "+v+"/shares/{share}/categories", lvViewer, s.shareCategories)
	h("GET "+v+"/shares/{share}/extensions", lvViewer, s.shareExtensions)
	h("GET "+v+"/shares/{share}/dirs/{dir}", lvViewer, s.getDir)
	h("GET "+v+"/shares/{share}/dirs/{dir}/children", lvViewer, s.dirChildren)
	h("GET "+v+"/shares/{share}/dirs/{dir}/treemap", lvViewer, s.dirTreemap)
	h("GET "+v+"/shares/{share}/files", lvViewer, s.listFiles)
	h("GET "+v+"/shares/{share}/top/files", lvViewer, s.topFiles)
	h("GET "+v+"/shares/{share}/top/dirs", lvViewer, s.topDirs)
	h("GET "+v+"/shares/{share}/cold/files", lvViewer, s.coldFiles)
	h("GET "+v+"/shares/{share}/cold/dirs", lvViewer, s.coldDirs)
	h("GET "+v+"/search", lvViewer, s.search)
	h("GET "+v+"/categories", lvViewer, s.listCategories)
	h("GET "+v+"/extension-map", lvViewer, s.listExtensionMap)
	if s.auth != nil {
		s.authRoutes(h, v)
		s.adminRoutes(h, v)
		s.handleFunc("GET "+v+"/events", s.guardStream(lvViewer, s.events))
	}
	if s.opts.Static != nil {
		s.mux.HandleFunc(v+"/", func(w http.ResponseWriter, r *http.Request) {
			s.problemCode(w, r, http.StatusNotFound, "not found", "", "not_found")
		})
		s.mux.Handle("/", s.opts.Static)
	}
}

// ServeHTTP wraps the router with request ids, panic recovery, logging and metrics.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := r.Header.Get("X-Request-Id")
	if len(id) == 0 || len(id) > 64 {
		id = newRequestID()
	}
	w.Header().Set("X-Request-Id", id)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	r = r.WithContext(context.WithValue(r.Context(), ctxRequestID{}, id))
	s.m.inflight.Inc()
	defer func() {
		s.m.inflight.Dec()
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			s.log.Error("panic in handler", "request_id", id, "panic", p, "path", r.URL.Path)
			if !rec.wrote {
				s.problem(rec, r, http.StatusInternalServerError, "internal error", "")
			}
		}
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		d := time.Since(start)
		s.m.observe(route, r.Method, rec.status, d)
		if route != "GET /healthz" && route != "GET /metrics" {
			s.log.Info("request", "request_id", id, "method", r.Method, "route", route, "path", r.URL.Path,
				"status", rec.status, "duration_ms", d.Milliseconds(), "bytes", rec.bytes)
		}
	}()
	s.mux.ServeHTTP(rec, r)
}

type ctxRequestID struct{}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(ctxRequestID{}).(string)
	return id
}

func newRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach Flush and deadlines.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// handle runs an endpoint with a deadline and turns its error into a problem response.
func (s *Server) handle(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		timeout := s.cfg.QueryTimeout
		if csv, _ := wantsCSV(r); csv {
			timeout = s.cfg.CSVTimeout
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := fn(w, r.WithContext(ctx)); err != nil {
			s.fail(w, r, err)
		}
	}
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		s.problem(w, r, http.StatusServiceUnavailable, "database not reachable", "")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ready\n"))
}

// fail maps an error to an RFC 9457 problem response.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if rec, ok := w.(*statusRecorder); ok && rec.wrote {
		// Output already started (CSV): abort so the client sees a broken
		// transfer instead of a silently truncated file.
		s.log.Error("response failed after it started", "request_id", requestID(r), "error", err)
		panic(http.ErrAbortHandler)
	}
	var br badRequest
	var inv *query.InvalidError
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &br):
		s.problem(w, r, http.StatusBadRequest, "invalid request", err.Error())
	case errors.As(err, &inv):
		s.problem(w, r, http.StatusBadRequest, "invalid request", inv.Msg)
	case errors.Is(err, query.ErrNotFound):
		s.problem(w, r, http.StatusNotFound, "not found", "")
	case errors.Is(err, query.ErrNoData):
		s.problemCode(w, r, http.StatusConflict, "no data yet", err.Error(), "no_data")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &pgErr) && pgErr.Code == "57014":
		w.Header().Set("Retry-After", "5")
		s.problem(w, r, http.StatusServiceUnavailable, "query took too long",
			"Narrow the filters (a smaller folder, a category, a size range) and try again.")
	case errors.Is(err, context.Canceled):
		// The client went away; nothing to answer.
	case s.authFail(w, r, err), s.adminFail(w, r, err), s.backupFail(w, r, err):
	default:
		s.log.Error("request failed", "request_id", requestID(r), "path", r.URL.Path, "error", err)
		s.problem(w, r, http.StatusInternalServerError, "internal error", "")
	}
}

type problemBody struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Code is a stable machine-readable reason (unauthenticated, mfa_required,
	// enroll_required, forbidden, csrf, locked, ...) the UI switches on.
	Code string `json:"code,omitempty"`
	// Field names the form field a validation error is about.
	Field string `json:"field,omitempty"`
	// Params are the values a message needs (a version, a limit), for the UI to put in a sentence in the reader's language.
	Params    map[string]string `json:"params,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

func writeProblem(w http.ResponseWriter, p problemBody) { json.NewEncoder(w).Encode(p) }

func (s *Server) problem(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(problemBody{Type: "about:blank", Title: title, Status: status, Detail: detail, RequestID: requestID(r)})
}

func writeJSON(w http.ResponseWriter, v any) error { return writeJSONStatus(w, http.StatusOK, v) }

// writeJSONStatus sets the content type before the status line: headers set
// after WriteHeader are silently dropped.
func writeJSONStatus(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// items wraps plain lists so every collection response is an object.
type items[T any] struct {
	Items []T `json:"items"`
}

func (s *Server) categoryKeys(r *http.Request) (map[int16]string, error) {
	s.catMu.Lock()
	defer s.catMu.Unlock()
	if s.cats != nil {
		return s.cats, nil
	}
	list, err := s.q.CategoryList(r.Context())
	if err != nil {
		return nil, err
	}
	m := make(map[int16]string, len(list))
	for _, c := range list {
		m[c.ID] = c.Key
	}
	s.cats = m
	return m, nil
}
