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
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vtheofil/uptime-watch/internal/store"
)

//go:embed templates/*.html
var templates embed.FS

type Server struct {
	store      *store.Store
	adminToken string
	log        *slog.Logger
	pages      *template.Template // status.html, add.html and the shared style
	draining   atomic.Bool
}

// SetDraining makes /readyz fail from now on. On shutdown this tells
// Kubernetes to stop sending traffic before the server actually closes.
func (s *Server) SetDraining() { s.draining.Store(true) }

// New builds the server. An empty adminToken turns the write endpoints off.
func New(s *store.Store, adminToken string, log *slog.Logger) *Server {
	pages := template.Must(template.New("").Funcs(template.FuncMap{
		"ago":    ago,
		"uptime": uptimePercent,
	}).ParseFS(templates, "templates/*.html"))

	return &Server{store: s, adminToken: adminToken, log: log, pages: pages}
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
	mux.HandleFunc("GET /targets/new", s.newTargetPage) // the add form (HTML)
	mux.HandleFunc("POST /targets", s.addTargetForm)    // the form posts here
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
		if !found || !s.tokenOK(got) {
			writeError(w, http.StatusUnauthorized, "missing or wrong admin token")
			return
		}
		next(w, r)
	}
}

// tokenOK reports whether got is the admin token. ConstantTimeCompare takes
// the same time whatever the input, so an attacker cannot guess the token one
// character at a time by timing. The API and the add form both use it.
func (s *Server) tokenOK(got string) bool {
	return s.adminToken != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.adminToken)) == 1
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
	data := statusData{Targets: list, Now: time.Now(), FormOn: s.adminToken != ""}
	// After the form adds a target it redirects to /?added=<name>. The note is
	// shown only for a target that exists, so the link cannot make the page
	// say something else.
	if added := r.URL.Query().Get("added"); added != "" {
		for _, t := range list {
			if t.Name == added {
				data.Added = added
			}
		}
	}
	s.render(w, http.StatusOK, "status.html", data)
}

type statusData struct {
	Targets []store.TargetStatus
	Now     time.Time
	FormOn  bool   // ADMIN_TOKEN is set, so the add form works
	Added   string // name of the target the form just added
}

// formValues are put back into the add form after an error, so nothing
// typed is lost. The token is never put back.
type formValues struct{ Name, URL, Interval, Timeout string }

type addData struct {
	FormOn bool
	Form   formValues
	Error  string
}

func (s *Server) newTargetPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "add.html", addData{FormOn: s.adminToken != ""})
}

// addTargetForm handles the add form. The admin token is a field of the form,
// not a cookie or a session: nothing is stored in the browser, and a page on
// another site cannot post the form for you without knowing the token
// (no cross-site request forgery). After success it redirects (303) to the
// status page, so reloading that page does not post the form again.
func (s *Server) addTargetForm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		s.renderAddError(w, http.StatusBadRequest, formValues{}, "the form could not be read (is it larger than 64 KB?)")
		return
	}
	f := formValues{
		Name:     strings.TrimSpace(r.PostFormValue("name")),
		URL:      strings.TrimSpace(r.PostFormValue("url")),
		Interval: strings.TrimSpace(r.PostFormValue("interval_seconds")),
		Timeout:  strings.TrimSpace(r.PostFormValue("timeout_ms")),
	}
	if s.adminToken == "" {
		s.renderAddError(w, http.StatusForbidden, f, "adding targets is off: set ADMIN_TOKEN to turn it on")
		return
	}
	if !s.tokenOK(r.PostFormValue("token")) {
		s.renderAddError(w, http.StatusUnauthorized, f, "wrong admin token")
		return
	}
	interval, errI := optionalInt(f.Interval)
	timeout, errT := optionalInt(f.Timeout)
	if errI != nil || errT != nil {
		s.renderAddError(w, http.StatusBadRequest, f, "interval and timeout must be whole numbers, or empty for the defaults")
		return
	}

	nt := store.NewTarget{Name: f.Name, URL: f.URL, IntervalSeconds: interval, TimeoutMS: timeout}
	if err := nt.Validate(); err != nil {
		s.renderAddError(w, http.StatusBadRequest, f, err.Error())
		return
	}
	t, err := s.store.CreateTarget(r.Context(), nt)
	switch {
	case errors.Is(err, store.ErrDuplicateName):
		s.renderAddError(w, http.StatusConflict, f, err.Error())
		return
	case errors.Is(err, store.ErrInvalidTarget):
		s.renderAddError(w, http.StatusBadRequest, f, err.Error())
		return
	case err != nil:
		s.log.Error("create target", "err", err)
		s.renderAddError(w, http.StatusInternalServerError, f, "internal error")
		return
	}

	s.log.Info("target created", "id", t.ID, "name", t.Name, "url", t.URL, "via", "form")
	http.Redirect(w, r, "/?added="+url.QueryEscape(t.Name), http.StatusSeeOther)
}

func (s *Server) renderAddError(w http.ResponseWriter, status int, f formValues, msg string) {
	s.render(w, status, "add.html", addData{FormOn: s.adminToken != "", Form: f, Error: msg})
}

// render writes one of the HTML pages. html/template escapes every value, so a
// target name like "<script>" is shown as text, never run.
func (s *Server) render(w http.ResponseWriter, status int, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.pages.ExecuteTemplate(w, page, data); err != nil {
		s.log.Error("render page", "page", page, "err", err)
	}
}

// optionalInt reads a form number; an empty field is 0, which means "use the default".
func optionalInt(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	return strconv.Atoi(v)
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
