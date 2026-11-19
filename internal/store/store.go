// Package store keeps targets and check results in Postgres.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
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

// Where a target came from (the source column).
const (
	SourceAPI = "api" // added with POST /api/targets
	SourceGit = "git" // listed in the targets file; owned by SyncGitTargets
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
	Source          string    `db:"source"` // SourceAPI or SourceGit
}

type NewTarget struct {
	Name            string
	URL             string
	IntervalSeconds int // 0 means DefaultIntervalSeconds
	TimeoutMS       int // 0 means DefaultTimeoutMS
}

// Validate checks what the database cannot: the shape of the name and URL.
// Interval and timeout ranges live in the database (CHECK constraints).
// Both the API and the targets file use it, so the rules are the same.
func (nt NewTarget) Validate() error {
	if nt.Name == "" || len(nt.Name) > 100 {
		return fmt.Errorf("%w: name is required and must be at most 100 characters", ErrInvalidTarget)
	}
	u, err := url.Parse(nt.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: url must be an absolute http:// or https:// URL", ErrInvalidTarget)
	}
	if nt.IntervalSeconds < 0 || nt.TimeoutMS < 0 {
		return fmt.Errorf("%w: interval_seconds and timeout_ms cannot be negative", ErrInvalidTarget)
	}
	return nil
}

func (nt NewTarget) withDefaults() NewTarget {
	if nt.IntervalSeconds == 0 {
		nt.IntervalSeconds = DefaultIntervalSeconds
	}
	if nt.TimeoutMS == 0 {
		nt.TimeoutMS = DefaultTimeoutMS
	}
	return nt
}

func (s *Store) CreateTarget(ctx context.Context, nt NewTarget) (Target, error) {
	nt = nt.withDefaults()
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

// SyncResult says what SyncGitTargets changed, by target name.
type SyncResult struct {
	Created   []string // new targets
	Updated   []string // git targets whose url, interval or timeout changed
	Adopted   []string // API targets that the file now lists: they become git targets and keep their checks
	Deleted   []string // git targets no longer in the file, with their checks
	Unchanged int
}

// SyncGitTargets makes the git targets match want. It runs in one
// transaction, so either the whole list applies or nothing does: a bad entry
// leaves the database as it was. Targets added through the API are never
// deleted; if want lists one by name, git takes it over (Adopted).
func (s *Store) SyncGitTargets(ctx context.Context, want []NewTarget) (SyncResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	defer tx.Rollback(ctx) // undoes everything on an early return; does nothing after Commit

	// FOR UPDATE locks the rows we read until the commit, so the API cannot
	// change or delete one of them between our read and our write.
	rows, _ := tx.Query(ctx, `SELECT * FROM targets ORDER BY name FOR UPDATE`)
	current, err := pgx.CollectRows(rows, pgx.RowToStructByName[Target])
	if err != nil {
		return SyncResult{}, err
	}
	byName := make(map[string]Target, len(current))
	for _, t := range current {
		byName[t.Name] = t
	}

	var res SyncResult
	listed := make(map[string]bool, len(want))
	for _, nt := range want {
		nt = nt.withDefaults()
		listed[nt.Name] = true
		cur, exists := byName[nt.Name]

		switch {
		case !exists:
			_, err = tx.Exec(ctx, `
				INSERT INTO targets (name, url, interval_seconds, timeout_ms, source)
				VALUES ($1, $2, $3, $4, 'git')`,
				nt.Name, nt.URL, nt.IntervalSeconds, nt.TimeoutMS)
			res.Created = append(res.Created, nt.Name)
		case cur.Source == SourceGit && cur.URL == nt.URL &&
			cur.IntervalSeconds == nt.IntervalSeconds && cur.TimeoutMS == nt.TimeoutMS:
			res.Unchanged++
		default:
			// Same id, so the checks stay attached. enabled is not touched.
			_, err = tx.Exec(ctx, `
				UPDATE targets
				SET    url = $2, interval_seconds = $3, timeout_ms = $4, source = 'git'
				WHERE  id = $1`,
				cur.ID, nt.URL, nt.IntervalSeconds, nt.TimeoutMS)
			if cur.Source == SourceGit {
				res.Updated = append(res.Updated, nt.Name)
			} else {
				res.Adopted = append(res.Adopted, nt.Name)
			}
		}
		if err != nil {
			return SyncResult{}, fmt.Errorf("target %q: %w", nt.Name, translate(err))
		}
	}

	var stale []int64
	for _, t := range current {
		if t.Source == SourceGit && !listed[t.Name] {
			stale = append(stale, t.ID)
			res.Deleted = append(res.Deleted, t.Name)
		}
	}
	if len(stale) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM targets WHERE id = ANY($1)`, stale); err != nil {
			return SyncResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return SyncResult{}, err
	}
	return res, nil
}

// DueTarget is what the worker needs to run one check.
type DueTarget struct {
	ID      int64
	Name    string // used as the "target" label in metrics
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
		RETURNING t.id, t.name, t.url, t.timeout_ms`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (DueTarget, error) {
		var d DueTarget
		var timeoutMS int
		err := row.Scan(&d.ID, &d.Name, &d.URL, &timeoutMS)
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

// TargetExists reports whether a target with this id exists.
func (s *Store) TargetExists(ctx context.Context, id int64) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM targets WHERE id = $1)`, id).Scan(&exists)
	return exists, err
}

// TargetStatus is a target with its latest check and its last 24 hours.
type TargetStatus struct {
	Target
	LastCheck *Check // nil until the first check is stored
	Checks24h int    // checks in the last 24 hours
	Up24h     int    // of those, how many were up
}

// ListStatus returns every target with its latest check and 24-hour counts,
// in one query. LATERAL runs the two small subqueries once per target, and
// both use the (target_id, checked_at DESC) index.
func (s *Store) ListStatus(ctx context.Context) ([]TargetStatus, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.name, t.url, t.interval_seconds, t.timeout_ms, t.enabled,
		       t.next_check_at, t.created_at, t.source,
		       l.checked_at, l.ok, l.status_code, l.latency_ms, l.error,
		       d.total, d.up
		FROM targets t
		LEFT JOIN LATERAL (
			SELECT checked_at, ok, status_code, latency_ms, error
			FROM   checks
			WHERE  target_id = t.id
			ORDER  BY checked_at DESC
			LIMIT  1
		) l ON true
		CROSS JOIN LATERAL (
			SELECT count(*) AS total, count(*) FILTER (WHERE ok) AS up
			FROM   checks
			WHERE  target_id = t.id AND checked_at > now() - interval '24 hours'
		) d
		ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TargetStatus, error) {
		var ts TargetStatus
		// The latest-check columns are NULL for a target never checked,
		// so they are read into pointers first.
		var (
			checkedAt *time.Time
			ok        *bool
			status    *int
			latency   *int
			errText   *string
		)
		err := row.Scan(
			&ts.ID, &ts.Name, &ts.URL, &ts.IntervalSeconds, &ts.TimeoutMS, &ts.Enabled,
			&ts.NextCheckAt, &ts.CreatedAt, &ts.Source,
			&checkedAt, &ok, &status, &latency, &errText,
			&ts.Checks24h, &ts.Up24h,
		)
		if err != nil || checkedAt == nil {
			return ts, err
		}
		c := Check{CheckedAt: *checkedAt, OK: *ok, LatencyMS: *latency}
		if status != nil {
			c.StatusCode = *status
		}
		if errText != nil {
			c.Error = *errText
		}
		ts.LastCheck = &c
		return ts, nil
	})
}

// constraintMessages explains each CHECK constraint in words a user can act on.
var constraintMessages = map[string]string{
	"targets_interval_seconds_check": "interval_seconds must be at least 10",
	"targets_timeout_ms_check":       "timeout_ms must be between 100 and 30000",
	"timeout_below_interval":         "timeout_ms must be shorter than interval_seconds",
	"targets_source_check":           "source must be api or git",
}

// translate turns Postgres constraint errors into errors the API can act on.
func translate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrDuplicateName
		case "23514": // check_violation
			msg, known := constraintMessages[pgErr.ConstraintName]
			if !known {
				msg = pgErr.ConstraintName
			}
			return fmt.Errorf("%w: %s", ErrInvalidTarget, msg)
		}
	}
	return err
}
