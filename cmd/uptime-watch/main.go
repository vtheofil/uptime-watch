// Command uptime-watch is one binary with several commands. The same image
// runs the API, the worker and the database jobs; only the command differs.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/vtheofil/uptime-watch/internal/config"
	"github.com/vtheofil/uptime-watch/internal/store"
)

// version is set at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usageText = `usage:
  uptime-watch serve         start the status page and API
  uptime-watch worker        check due targets until stopped
  uptime-watch check <url>   check one URL once and print the result
  uptime-watch migrate       create or update the database tables
  uptime-watch prune         delete checks older than RETENTION_DAYS
  uptime-watch version       print the version`

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}

	// ctx is cancelled on Ctrl+C (SIGINT) or when Kubernetes stops the pod (SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "serve":
		err = runServe(ctx, cfg)
	case "worker":
		err = runWorker(ctx, cfg)
	case "check":
		if len(os.Args) != 3 {
			usage()
		}
		runCheck(ctx, os.Args[2])
	case "migrate":
		err = runMigrate(ctx, cfg)
	case "prune":
		err = runPrune(ctx, cfg)
	case "version":
		fmt.Println(version)
	default:
		usage()
	}

	if err != nil {
		stop()
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

// openStore is shared by every command that needs the database.
func openStore(ctx context.Context, cfg config.Config) (*store.Store, error) {
	if err := cfg.RequireDatabase(); err != nil {
		return nil, err
	}
	return store.Open(ctx, cfg.DatabaseURL)
}
