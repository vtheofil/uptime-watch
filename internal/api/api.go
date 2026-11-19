// Package api serves the status page, the JSON API and the health endpoints.
package api

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vtheofil/uptime-watch/internal/store"
)

//go:embed templates/status.html
var templates embed.FS

type Server struct {
	store      *store.Store
	adminToken string
	log        *slog.Logger
	page       *template.Template
	draining   atomic.Bool
}

// SetDraining makes /readyz fail from now on. On shutdown this tells
// Kubernetes to stop sending traffic before the server actually closes.
func (s *Server) SetDraining() { s.draining.Store(true) }

// New builds the server. An empty adminToken turns the write endpoints off.
func New(s *store.Store, adminToken string, log *slog.Logger) *Server {
	page := template.Must(template.New("status.html").Funcs(template.FuncMap{
		"ago":    ago,
		"uptime": uptimePercent,
	}).ParseFS(templates, "templates/status.html"))

	return &Server{store: s, adminToken: adminToken, log: log, page: page}
}

// Routes returns the router. Go 1.22+ patterns carry the method and {wildcards}.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.statusPage) // {$} = exactly "/", not every path
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /api/targets", s.listTargets)
	mux.HandleFunc("POST /api/targets", s.requireToken(s.createTarget))
	mux.HandleFunc("DELETE /api/targets/{id}", s.requireToken(s.deleteTarget))
	mux.HandleFunc("GET /api/targets/{id}/checks", s.listChecks)
	return mux
}

// healthz answers "the process is alive". It never touches the database:
// if it did, a database outage would make Kubernetes restart every pod.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok\n"))
}

// readyz answers "I can serve requests", which needs the database.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		http.Error(w, "database unreachable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ready\n"))
}

// requireToken wraps a handler so it runs only with "Authorization: Bearer <ADMIN_TOKEN>".
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken == "" {
			writeError(w, http.StatusForbidden, "write API is off: set ADMIN_TOKEN to turn it on")
			return
		}
		got, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// ConstantTimeCompare takes the same time whatever the input, so an
		// attacker cannot guess the token one character at a time by timing.
		if !found || subtle.ConstantTimeCompare([]byte(got), []byte(s.adminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "missing or wrong admin token")
			return
		}
		next(w, r)
	}
}

// ---- JSON shapes ----

type targetJSON struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	URL             string     `json:"url"`
	IntervalSeconds int        `json:"interval_seconds"`
	TimeoutMS       int        `json:"timeout_ms"`
	Enabled         bool       `json:"enabled"`
	Source          string     `json:"source"`     // "api" or "git"
	LastCheck       *checkJSON `json:"last_check"` // null until the first check
	Uptime24h       *float64   `json:"uptime_24h"` // percent; null with no checks in 24h
}

type checkJSON struct {
	CheckedAt  time.Time `json:"checked_at"`
	OK         bool      `json:"ok"`
	StatusCode int       `json:"status_code,omitempty"`
	LatencyMS  int       `json:"latency_ms"`
	Error      string    `json:"error,omitempty"`
}

func toCheckJSON(c store.Check) checkJSON {
	return checkJSON{CheckedAt: c.CheckedAt, OK: c.OK, StatusCode: c.StatusCode, LatencyMS: c.LatencyMS, Error: c.Error}
}

func toTargetJSON(ts store.TargetStatus) targetJSON {
	t := targetJSON{
		ID: ts.ID, Name: ts.Name, URL: ts.URL,
		IntervalSeconds: ts.IntervalSeconds, TimeoutMS: ts.TimeoutMS, Enabled: ts.Enabled,
		Source: ts.Source,
	}
	if ts.LastCheck != nil {
		c := toCheckJSON(*ts.LastCheck)
		t.LastCheck = &c
	}
	if ts.Checks24h > 0 {
		p := uptimePercent(ts)
		t.Uptime24h = &p
	}
	return t
}

// ---- handlers ----

func (s *Server) listTargets(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListStatus(r.Context())
	if err != nil {
		s.internalError(w, "list targets", err)
		return
	}
	out := make([]targetJSON, 0, len(list)) // [] rather than null when empty
	for _, ts := range list {
		out = append(out, toTargetJSON(ts))
	}
	writeJSON(w, http.StatusOK, out)
}

type createRequest struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	IntervalSeconds int    `json:"interval_seconds"`
	TimeoutMS       int    `json:"timeout_ms"`
}

func (s *Server) createTarget(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10) // 64 KB is plenty for one target
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // a typo like "intervall" is an error, not silently ignored

	var req createRequest
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is larger than 64 KB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	nt := store.NewTarget{
		Name: strings.TrimSpace(req.Name), URL: req.URL,
		IntervalSeconds: req.IntervalSeconds, TimeoutMS: req.TimeoutMS,
	}
	if err := nt.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	t, err := s.store.CreateTarget(r.Context(), nt)
	switch {
	case errors.Is(err, store.ErrDuplicateName):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, store.ErrInvalidTarget):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		s.internalError(w, "create target", err)
		return
	}

	s.log.Info("target created", "id", t.ID, "name", t.Name, "url", t.URL)
	writeJSON(w, http.StatusCreated, toTargetJSON(store.TargetStatus{Target: t}))
}

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteTarget(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case err != nil:
		s.internalError(w, "delete target", err)
	default:
		s.log.Info("target deleted", "id", id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) listChecks(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "limit must be a number from 1 to 1000")
			return
		}
		limit = n
	}

	exists, err := s.store.TargetExists(r.Context(), id)
	if err != nil {
		s.internalError(w, "find target", err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, store.ErrNotFound.Error())
		return
	}

	checks, err := s.store.RecentChecks(r.Context(), id, limit)
	if err != nil {
		s.internalError(w, "list checks", err)
		return
	}
	out := make([]checkJSON, 0, len(checks))
	for _, c := range checks {
		out = append(out, toCheckJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListStatus(r.Context())
	if err != nil {
		s.internalError(w, "list targets", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct {
		Targets []store.TargetStatus
		Now     time.Time
	}{list, time.Now()}
	if err := s.page.Execute(w, data); err != nil {
		s.log.Error("render status page", "err", err)
	}
}

// ---- helpers ----

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "id must be a positive number")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// internalError logs the real cause and tells the client only that it failed:
// database details stay out of responses.
func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func uptimePercent(ts store.TargetStatus) float64 {
	if ts.Checks24h == 0 {
		return 0
	}
	return float64(ts.Up24h) * 100 / float64(ts.Checks24h)
}

// ago turns a time into "8s ago", "3m ago", "2h ago".
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s ago"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	default:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	}
}
