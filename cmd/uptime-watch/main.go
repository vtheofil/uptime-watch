package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/vtheofil/uptime-watch/internal/checker"
	"github.com/vtheofil/uptime-watch/internal/config"
	"github.com/vtheofil/uptime-watch/internal/store"
)

const usageText = `usage:
  uptime-watch serve         start the HTTP server
  uptime-watch check <url>   check one URL once and print the result
  uptime-watch migrate       create or update the database tables
  uptime-watch prune         delete checks older than RETENTION_DAYS`

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	ctx := context.Background()

	switch os.Args[1] {
	case "serve":
		err = serve(cfg)
	case "check":
		if len(os.Args) != 3 {
			usage()
		}
		check(ctx, os.Args[2])
	case "migrate":
		err = migrate(ctx, cfg)
	case "prune":
		err = prune(ctx, cfg)
	default:
		usage()
	}

	if err != nil {
		fail(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, usageText)
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func check(ctx context.Context, url string) {
	r := checker.Check(ctx, url, 5*time.Second)
	fmt.Printf("ok=%v status=%d latency=%v err=%v\n", r.OK, r.StatusCode, r.Latency, r.Err)
}

// openStore is shared by every command that needs the database.
func openStore(ctx context.Context, cfg config.Config) (*store.Store, error) {
	if err := cfg.RequireDatabase(); err != nil {
		return nil, err
	}
	return store.Open(ctx, cfg.DatabaseURL)
}

func migrate(ctx context.Context, cfg config.Config) error {
	s, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer s.Close()

	n, err := s.Migrate(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("applied %d migration(s)\n", n)
	return nil
}

func prune(ctx context.Context, cfg config.Config) error {
	s, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer s.Close()

	n, err := s.Prune(ctx, cfg.RetentionDays)
	if err != nil {
		return err
	}
	fmt.Printf("deleted %d check(s) older than %d day(s)\n", n, cfg.RetentionDays)
	return nil
}

func serve(cfg config.Config) error {
	addr := ":" + cfg.Port

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	log.Println("listening on", addr)
	return http.ListenAndServe(addr, mux)
}
