package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vtheofil/uptime-watch/internal/config"
)

func TestDefaults(t *testing.T) {
	for _, k := range []string{"PORT", "DATABASE_URL", "ADMIN_TOKEN", "RETENTION_DAYS", "WORKER_CONCURRENCY",
		"WORKER_POLL_INTERVAL", "WORKER_GRACE", "SHUTDOWN_DELAY", "LOG_FORMAT", "LOG_LEVEL"} {
		t.Setenv(k, "")
	}
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != "8080" || c.RetentionDays != 30 || c.WorkerConcurrency != 10 ||
		c.PollInterval != 5*time.Second || c.WorkerGrace != 10*time.Second || c.ShutdownDelay != 0 ||
		c.LogFormat != "text" || c.LogLevel != slog.LevelInfo {
		t.Errorf("defaults = %+v", c)
	}
	if err := c.RequireDatabase(); err == nil {
		t.Error("RequireDatabase with no DATABASE_URL should fail")
	}
}

func TestBadValuesAreRejectedWithTheVariableName(t *testing.T) {
	cases := map[string]string{
		"RETENTION_DAYS":       "0",
		"WORKER_CONCURRENCY":   "ten",
		"WORKER_POLL_INTERVAL": "0s",
		"WORKER_GRACE":         "-1s",
		"LOG_FORMAT":           "xml",
		"LOG_LEVEL":            "loud",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s=%s: err = %v, want an error naming %s", key, value, err, key)
			}
		})
	}
}
