// Package metrics defines what uptime-watch exposes to Prometheus on /metrics.
package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vtheofil/uptime-watch/internal/store"
)

// NewRegistry returns a registry with the standard Go runtime and process
// metrics (memory, goroutines, CPU, open files) already in it.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// Handler serves the registry. ContinueOnError keeps /metrics working when
// one collector fails (e.g. the database is down): the rest still show.
func Handler(reg *prometheus.Registry, log *slog.Logger) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		ErrorLog:      promLogger{log},
	})
}

type promLogger struct{ log *slog.Logger }

func (l promLogger) Println(v ...any) { l.log.Error("metrics", "err", v) }

// checkBuckets are the latency buckets for checks, in seconds: from 50 ms
// up to the 30 s maximum timeout.
var checkBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// ---- worker ----

// Worker holds the worker's metrics. A nil *Worker is valid and records nothing,
// so tests and callers that do not care can pass nil.
type Worker struct {
	checks   *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inflight prometheus.Gauge
}

func NewWorker(reg prometheus.Registerer) *Worker {
	m := &Worker{
		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "uptime_checks_total",
			Help: "Checks finished, by target and result (up or down).",
		}, []string{"target", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "uptime_check_duration_seconds",
			Help:    "How long each check took.",
			Buckets: checkBuckets,
		}, []string{"target"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "uptime_worker_inflight",
			Help: "Checks running right now in this worker.",
		}),
	}
	reg.MustRegister(m.checks, m.duration, m.inflight)
	return m
}

func (m *Worker) CheckStarted() {
	if m != nil {
		m.inflight.Inc()
	}
}

func (m *Worker) CheckEnded() {
	if m != nil {
		m.inflight.Dec()
	}
}

func (m *Worker) Observe(target string, ok bool, took time.Duration) {
	if m == nil {
		return
	}
	result := "down"
	if ok {
		result = "up"
	}
	m.checks.WithLabelValues(target, result).Inc()
	m.duration.WithLabelValues(target).Observe(took.Seconds())
}

// ---- HTTP server ----

type HTTP struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func NewHTTP(reg prometheus.Registerer) *HTTP {
	m := &HTTP{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests served, by route and status code.",
		}, []string{"route", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "How long requests took to serve, by route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// Wrap counts and times every request that next serves. The route label is
// the matched pattern (e.g. "GET /api/targets/{id}"), never the raw path:
// one label value per id would make a new time series for every target.
func (m *HTTP) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := r.Pattern // filled in by ServeMux while routing
		if route == "" {
			route = "unmatched"
		}
		m.requests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		m.duration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// ---- target status, read from the database on every scrape ----

// StatusCollector exposes the current state of every enabled target. It reads
// the database when Prometheus scrapes, so the numbers are right no matter how
// many workers there are: each worker only sees the targets it checked.
type StatusCollector struct {
	store    *store.Store
	up       *prometheus.Desc
	last     *prometheus.Desc
	interval *prometheus.Desc
}

func NewStatusCollector(s *store.Store) *StatusCollector {
	labels := []string{"target"}
	return &StatusCollector{
		store: s,
		up: prometheus.NewDesc("uptime_target_up",
			"1 if the latest check of the target was up, 0 if down.", labels, nil),
		last: prometheus.NewDesc("uptime_target_last_check_timestamp_seconds",
			"When the target was last checked (Unix time).", labels, nil),
		interval: prometheus.NewDesc("uptime_target_interval_seconds",
			"How often the target should be checked.", labels, nil),
	}
}

func (c *StatusCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up
	ch <- c.last
	ch <- c.interval
}

func (c *StatusCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	list, err := c.store.ListStatus(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.up, err)
		return
	}
	for _, t := range list {
		if !t.Enabled {
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.interval, prometheus.GaugeValue, float64(t.IntervalSeconds), t.Name)
		if t.LastCheck == nil {
			continue // never checked yet: no up/down to report
		}
		up := 0.0
		if t.LastCheck.OK {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, up, t.Name)
		ch <- prometheus.MustNewConstMetric(c.last, prometheus.GaugeValue, float64(t.LastCheck.CheckedAt.Unix()), t.Name)
	}
}
