package main

import (
	"context"
	"fmt"
	"time"

	"github.com/vtheofil/uptime-watch/internal/checker"
	"github.com/vtheofil/uptime-watch/internal/config"
)

// runCheck checks one URL once. Handy for trying the checker by hand.
func runCheck(ctx context.Context, url string) {
	r := checker.Check(ctx, url, 5*time.Second)
	fmt.Printf("ok=%v status=%d latency=%v err=%v\n", r.OK, r.StatusCode, r.Latency, r.Err)
}

// runMigrate applies pending migrations. In Kubernetes it runs as a Job
// before the new version of the app starts.
func runMigrate(ctx context.Context, cfg config.Config) error {
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

// runPrune deletes old checks. In Kubernetes it runs as a daily CronJob.
func runPrune(ctx context.Context, cfg config.Config) error {
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
