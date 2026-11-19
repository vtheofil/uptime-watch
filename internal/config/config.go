// Package config reads settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port              string        // PORT, default 8080
	DatabaseURL       string        // DATABASE_URL, no default
	AdminToken        string        // ADMIN_TOKEN, empty turns the write API off
	RetentionDays     int           // RETENTION_DAYS, default 30
	WorkerConcurrency int           // WORKER_CONCURRENCY, default 10
	PollInterval      time.Duration // WORKER_POLL_INTERVAL, default 5s
	WorkerGrace       time.Duration // WORKER_GRACE, default 10s
	ShutdownDelay     time.Duration // SHUTDOWN_DELAY, default 0 (5s in Kubernetes)
	LogFormat         string        // LOG_FORMAT, text or json, default text
	LogLevel          slog.Level    // LOG_LEVEL, debug|info|warn|error, default info
}

func Load() (Config, error) {
	c := Config{
		Port:        getenv("PORT", "8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		AdminToken:  os.Getenv("ADMIN_TOKEN"),
		LogFormat:   getenv("LOG_FORMAT", "text"),
	}

	var err error
	if c.RetentionDays, err = intEnv("RETENTION_DAYS", 30, 1); err != nil {
		return Config{}, err
	}
	if c.WorkerConcurrency, err = intEnv("WORKER_CONCURRENCY", 10, 1); err != nil {
		return Config{}, err
	}
	if c.PollInterval, err = durationEnv("WORKER_POLL_INTERVAL", 5*time.Second, false); err != nil {
		return Config{}, err
	}
	if c.WorkerGrace, err = durationEnv("WORKER_GRACE", 10*time.Second, true); err != nil {
		return Config{}, err
	}
	if c.ShutdownDelay, err = durationEnv("SHUTDOWN_DELAY", 0, true); err != nil {
		return Config{}, err
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		return Config{}, fmt.Errorf("LOG_FORMAT must be text or json, got %q", c.LogFormat)
	}
	if err := c.LogLevel.UnmarshalText([]byte(getenv("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", os.Getenv("LOG_LEVEL"))
	}
	return c, nil
}

// Logger builds the logger every command uses. JSON in Kubernetes, so log
// tools (e.g. Loki) can filter by field; text locally, so people can read it.
func (c Config) Logger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// RequireDatabase is called by the commands that talk to Postgres.
func (c Config) RequireDatabase() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is not set, e.g. postgres://uptime:uptime@localhost:5433/uptime?sslmode=disable")
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func intEnv(key string, fallback, min int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %q", key, v)
	}
	if n < min {
		return 0, fmt.Errorf("%s must be at least %d, got %d", key, min, n)
	}
	return n, nil
}

func durationEnv(key string, fallback time.Duration, zeroOK bool) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration like 5s or 1m, got %q", key, v)
	}
	if d < 0 || (d == 0 && !zeroOK) {
		return 0, fmt.Errorf("%s must be positive, got %s", key, v)
	}
	return d, nil
}
