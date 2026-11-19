package main

import (
	"context"
	"net/http"
	"time"

	"github.com/vtheofil/uptime-watch/internal/config"
	"github.com/vtheofil/uptime-watch/internal/metrics"
	"github.com/vtheofil/uptime-watch/internal/worker"
)

// runWorker runs the check loop, plus a small HTTP server that only answers
// /healthz (is the loop turning?) and /metrics (for Prometheus).
func runWorker(ctx context.Context, cfg config.Config) error {
	log := cfg.Logger()
	s, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer s.Close()

	reg := metrics.NewRegistry()
	w := worker.New(s, worker.Options{
		Concurrency: cfg.WorkerConcurrency,
		Poll:        cfg.PollInterval,
		Grace:       cfg.WorkerGrace,
		Log:         log,
		Metrics:     metrics.NewWorker(reg),
	})

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler(reg, log))
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, r *http.Request) {
		if !w.Healthy() {
			http.Error(rw, "check loop is stuck", http.StatusServiceUnavailable)
			return
		}
		rw.Write([]byte("ok\n"))
	})
	srv := &http.Server{Addr: ":" + cfg.Port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	// The HTTP server stops when ctx is cancelled; the worker as well.
	httpDone := make(chan error, 1)
	go func() { httpDone <- serveUntilDone(ctx, srv, log, 0, nil) }()

	log.Info("worker started", "version", version, "concurrency", cfg.WorkerConcurrency,
		"poll", cfg.PollInterval.String(), "grace", cfg.WorkerGrace.String())
	w.Run(ctx)
	log.Info("worker stopped")
	return <-httpDone
}
