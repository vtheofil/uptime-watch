package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/vtheofil/uptime-watch/internal/api"
	"github.com/vtheofil/uptime-watch/internal/config"
	"github.com/vtheofil/uptime-watch/internal/metrics"
)

func runServe(ctx context.Context, cfg config.Config) error {
	log := cfg.Logger()
	s, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer s.Close()

	log.Info("uptime-watch serve", "version", version)
	if cfg.AdminToken == "" {
		log.Warn("ADMIN_TOKEN is not set: the write API is off")
	}

	reg := metrics.NewRegistry()
	httpMetrics := metrics.NewHTTP(reg)
	reg.MustRegister(metrics.NewStatusCollector(s))

	a := api.New(s, cfg.AdminToken, log)
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler(reg, log))
	mux.Handle("/", httpMetrics.Wrap(a.Routes()))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second, // a client that never finishes its headers is cut off
	}
	return serveUntilDone(ctx, srv, log, cfg.ShutdownDelay, a.SetDraining)
}

// serveUntilDone runs srv until ctx is cancelled, then shuts it down the way
// Kubernetes needs:
//
//  1. beforeDrain() — /readyz starts failing, so the pod leaves the Service.
//  2. wait delay     — Kubernetes needs a few seconds to notice and stop
//     sending traffic; requests that still arrive are served normally.
//  3. Shutdown       — stop accepting connections and let the requests in
//     flight finish, for at most 10 seconds.
func serveUntilDone(ctx context.Context, srv *http.Server, log *slog.Logger, delay time.Duration, beforeDrain func()) error {
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err // e.g. the port is already in use
	case <-ctx.Done():
	}

	log.Info("shutting down", "delay", delay.String())
	if beforeDrain != nil {
		beforeDrain()
	}
	time.Sleep(delay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stopped")
	return nil
}
