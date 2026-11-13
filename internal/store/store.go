// Package store keeps targets and check results in Postgres.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// The SQL files are compiled into the binary, so the image needs no extra files.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Defaults used when a new target leaves the interval or timeout empty.
const (
	DefaultIntervalSeconds = 60
	DefaultTimeoutMS       = 5000
)

var (
	ErrNotFound      = errors.New("target not found")
	ErrDuplicateName = errors.New("a target with this name already exists")
	ErrInvalidTarget = errors.New("invalid target")
)

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and checks that it answers.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reach database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Ping reports whether the database answers. /readyz will use it.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies the migrations that have not run yet and returns how many ran.
func (s *Store) Migrate(ctx context.Context) (int, error) {
	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return 0, err
	}
	db := stdlib.OpenDBFromPool(s.pool) // goose speaks database/sql; this does not close the pool
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, fsys)
	if err != nil {
		return 0, fmt.Errorf("load migrations: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}
	return len(results), nil
}

// Target is one URL we monitor. The db tags name the matching columns.
type Target struct {
	ID              int64     `db:"id"`
	Name            string    `db:"name"`
	URL             string    `db:"url"`
	IntervalSeconds int       `db:"interval_seconds"`
	TimeoutMS       int       `db:"timeout_ms"`
	Enabled         bool      `db:"enabled"`
	NextCheckAt     time.Time `db:"next_check_at"`
	CreatedAt       time.Time `db:"created_at"`
}

type NewTarget struct {
	Name            string
	URL             string
	IntervalSeconds int // 0 means DefaultIntervalSeconds
	TimeoutMS       int // 0 means DefaultTimeoutMS
}

func (s *Store) CreateTarget(ctx context.Context, nt NewTarget) (Target, error) {
	if nt.IntervalSeconds == 0 {
		nt.IntervalSeconds = DefaultIntervalSeconds
	}
	if nt.TimeoutMS == 0 {
		nt.TimeoutMS = DefaultTimeoutMS
	}

	rows, _ := s.pool.Query(ctx, `
		INSERT INTO targets (name, url, interval_seconds, timeout_ms)
		VALUES ($1, $2, $3, $4)
		RETURNING *`,
		nt.Name, nt.URL, nt.IntervalSeconds, nt.TimeoutMS)
	t, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Target])
	if err != nil {
		return Target{}, translate(err)
	}
	return t, nil
}

func (s *Store) ListTargets(ctx context.Context) ([]Target, error) {
	rows, _ := s.pool.Query(ctx, `SELECT * FROM targets ORDER BY name`)
	return pgx.CollectRows(rows, pgx.RowToStructByName[Target])
}

// DeleteTarget removes a target; its checks go with it (ON DELETE CASCADE).
func (s *Store) DeleteTarget(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM targets WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DueTarget is what the worker needs to run one check.
type DueTarget struct {
	ID      int64
	URL     string
	Timeout time.Duration
}

// ClaimDue picks up to limit enabled targets whose time has come and moves
// their next_check_at forward in the same statement. FOR UPDATE SKIP LOCKED
// lets several workers claim at once without two of them getting the same target.
func (s *Store) ClaimDue(ctx context.Context, limit int) ([]DueTarget, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE targets t
		SET    next_check_at = now() + make_interval(secs => t.interval_seconds)
		WHERE  t.id IN (
			SELECT id FROM targets
			WHERE  enabled AND next_check_at <= now()
			ORDER  BY next_check_at
			LIMIT  $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING t.id, t.url, t.timeout_ms`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (DueTarget, error) {
		var d DueTarget
		var timeoutMS int
		err := row.Scan(&d.ID, &d.URL, &timeoutMS)
		d.Timeout = time.Duration(timeoutMS) * time.Millisecond
		return d, err
	})
}

// CheckRecord is one finished check, ready to be stored.
type CheckRecord struct {
	TargetID   int64
	OK         bool
	StatusCode int // 0 when no response arrived; stored as NULL
	Latency    time.Duration
	Error      string // empty when there was no error; stored as NULL
}

func (s *Store) RecordCheck(ctx context.Context, c CheckRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO checks (target_id, ok, status_code, latency_ms, error)
		VALUES ($1, $2, NULLIF($3, 0), $4, NULLIF($5, ''))`,
		c.TargetID, c.OK, c.StatusCode, c.Latency.Milliseconds(), c.Error)
	return err
}

// Check is one stored result.
type Check struct {
	CheckedAt  time.Time `db:"checked_at"`
	OK         bool      `db:"ok"`
	StatusCode int       `db:"status_code"`
	LatencyMS  int       `db:"latency_ms"`
	Error      string    `db:"error"`
}

// RecentChecks returns the newest checks of one target, newest first.
func (s *Store) RecentChecks(ctx context.Context, targetID int64, limit int) ([]Check, error) {
	rows, _ := s.pool.Query(ctx, `
		SELECT checked_at, ok, COALESCE(status_code, 0) AS status_code,
		       latency_ms, COALESCE(error, '') AS error
		FROM   checks
		WHERE  target_id = $1
		ORDER  BY checked_at DESC
		LIMIT  $2`, targetID, limit)
	return pgx.CollectRows(rows, pgx.RowToStructByName[Check])
}

// Prune deletes checks older than the given number of days and returns how many.
func (s *Store) Prune(ctx context.Context, days int) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM checks WHERE checked_at < now() - make_interval(days => $1)`, days)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// translate turns Postgres constraint errors into errors the API can act on.
func translate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrDuplicateName
		case "23514": // check_violation
			return fmt.Errorf("%w: %s", ErrInvalidTarget, pgErr.ConstraintName)
		}
	}
	return err
}
