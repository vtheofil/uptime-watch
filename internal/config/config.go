// Package config reads settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port          string // PORT, default 8080
	DatabaseURL   string // DATABASE_URL, no default
	RetentionDays int    // RETENTION_DAYS, default 30
}

func Load() (Config, error) {
	c := Config{
		Port:        getenv("PORT", "8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
	}

	days, err := intEnv("RETENTION_DAYS", 30)
	if err != nil {
		return Config{}, err
	}
	if days < 1 {
		return Config{}, errors.New("RETENTION_DAYS must be at least 1")
	}
	c.RetentionDays = days

	return c, nil
}

// RequireDatabase is called by the commands that talk to Postgres.
func (c Config) RequireDatabase() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is not set, e.g. postgres://uptime:uptime@localhost:5433/uptime?sslmode=disable")
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %q", key, v)
	}
	return n, nil
}
