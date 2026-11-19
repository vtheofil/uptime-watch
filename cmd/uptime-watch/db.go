package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vtheofil/uptime-watch/internal/checker"
	"github.com/vtheofil/uptime-watch/internal/config"
	"github.com/vtheofil/uptime-watch/internal/targetsfile"
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

// runSyncTargets makes the git targets match the file. In Kubernetes it runs
// as a Job after every install and upgrade (a PostSync hook in Argo CD).
func runSyncTargets(ctx context.Context, cfg config.Config, path string) error {
	// Read and check the whole file before connecting: a typo fails here,
	// with the database untouched.
	want, err := targetsfile.Load(path)
	if err != nil {
		return err
	}

	s, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer s.Close()

	res, err := s.SyncGitTargets(ctx, want)
	if err != nil {
		return err
	}
	fmt.Printf("synced %d target(s) from %s\n", len(want), path)
	for _, line := range []struct {
		what  string
		names []string
	}{
		{"created", res.Created},
		{"updated", res.Updated},
		{"adopted from the API", res.Adopted},
		{"deleted", res.Deleted},
	} {
		if len(line.names) > 0 {
			fmt.Printf("  %s: %s\n", line.what, strings.Join(line.names, ", "))
		}
	}
	fmt.Printf("  unchanged: %d\n", res.Unchanged)
	return nil
}
