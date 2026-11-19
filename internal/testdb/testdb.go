// Package testdb gives tests a real Postgres in Docker.
// A test package calls testdb.Main from TestMain: one container serves all of
// that package's tests, and each test starts from empty tables with testdb.New.
package testdb

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/vtheofil/uptime-watch/internal/store"
)

var dsn string

// Main starts Postgres, runs the package's tests, stops Postgres and exits.
func Main(m *testing.M) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("uptime"),
		postgres.WithUsername("uptime"),
		postgres.WithPassword("uptime"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres container (is Docker running?):", err)
		os.Exit(1)
	}
	dsn, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	code := m.Run()
	_ = testcontainers.TerminateContainer(ctr)
	os.Exit(code)
}

// New opens the store, applies migrations and empties both tables.
func New(t testing.TB) *store.Store {
	t.Helper()
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	Exec(t, `TRUNCATE targets, checks RESTART IDENTITY`)
	return s
}

// Exec runs raw SQL for test setup, e.g. to move a check into the past.
func Exec(t testing.TB, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}
