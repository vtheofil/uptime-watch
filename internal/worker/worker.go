// Package worker runs the check loop: claim due targets, check them in
// parallel, store the results.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vtheofil/uptime-watch/internal/checker"
	"github.com/vtheofil/uptime-watch/internal/metrics"
	"github.com/vtheofil/uptime-watch/internal/store"
)

type Options struct {
	Concurrency int             // most checks running at once
	Poll        time.Duration   // how often to look for due targets
	Grace       time.Duration   // how long running checks may finish after a stop
	Log         *slog.Logger    // required
	Metrics     *metrics.Worker // optional
}

type Worker struct {
	store   *store.Store
	poll    time.Duration
	grace   time.Duration
	log     *slog.Logger
	metrics *metrics.Worker

	// slots holds one token per running check. Its capacity is the
	// concurrency limit: a check takes a token to start and gives it back
	// when it ends, so no more than cap(slots) checks ever run at once.
	slots chan struct{}
	wg    sync.WaitGroup

	lastLoop atomic.Int64 // Unix nanoseconds of the latest loop pass, for /healthz
}

func New(s *store.Store, o Options) *Worker {
	return &Worker{
		store:   s,
		poll:    o.Poll,
		grace:   o.Grace,
		log:     o.Log,
		metrics: o.Metrics,
		slots:   make(chan struct{}, o.Concurrency),
	}
}

// Run claims and checks due targets every poll interval until ctx is cancelled.
//
// Stopping happens in two steps, which is what Kubernetes expects on SIGTERM:
// first no new targets are claimed, then the checks already running get up to
// Grace to finish and store their results. Whatever is still running after
// that is cancelled. Run returns when every check has ended.
func (w *Worker) Run(ctx context.Context) {
	// Checks get their own context: it survives ctx so they can finish
	// during the grace period, and is cancelled when the grace runs out.
	checkCtx, cancelChecks := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelChecks()

	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	for ctx.Err() == nil {
		w.lastLoop.Store(time.Now().UnixNano())
		w.claimAndStart(ctx, checkCtx)
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(w.grace):
		w.log.Warn("grace period over, cancelling running checks", "grace", w.grace.String())
		cancelChecks()
		<-done
	}
}

// Healthy reports whether the loop is still turning. If it has not passed
// for three poll intervals, something is stuck and the pod should restart.
func (w *Worker) Healthy() bool {
	last := time.Unix(0, w.lastLoop.Load())
	return time.Since(last) < 3*w.poll+time.Second
}

// claimAndStart claims only as many targets as there are free slots, so a
// claimed target never waits in a queue: every claimed check starts now.
func (w *Worker) claimAndStart(ctx, checkCtx context.Context) {
	free := cap(w.slots) - len(w.slots)
	if free == 0 {
		return
	}

	due, err := w.store.ClaimDue(ctx, free)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("claim due targets", "err", err)
		}
		return // try again on the next tick
	}

	for _, d := range due {
		// Never blocks: we claimed at most `free` targets, and only this
		// loop adds tokens while running checks can only remove them.
		w.slots <- struct{}{}
		w.wg.Add(1)
		w.metrics.CheckStarted()
		go func() {
			defer w.wg.Done()
			defer func() { <-w.slots }()
			defer w.metrics.CheckEnded()
			w.checkOne(checkCtx, d)
		}()
	}
}

func (w *Worker) checkOne(ctx context.Context, d store.DueTarget) {
	r := checker.Check(ctx, d.URL, d.Timeout)

	if ctx.Err() != nil {
		// The grace period ran out and we cut the check short ourselves, so
		// storing it as "down" would be wrong. The target was claimed, so it
		// is checked again one interval later.
		return
	}

	w.metrics.Observe(d.Name, r.OK, r.Latency)

	rec := store.CheckRecord{
		TargetID:   d.ID,
		OK:         r.OK,
		StatusCode: r.StatusCode,
		Latency:    r.Latency,
	}
	if r.Err != nil {
		rec.Error = r.Err.Error()
	}

	if err := w.store.RecordCheck(ctx, rec); err != nil {
		w.log.Error("record check", "target", d.Name, "err", err)
		return
	}

	attrs := []any{
		"target", d.Name,
		"url", d.URL,
		"ok", r.OK,
		"status", r.StatusCode,
		"latency_ms", r.Latency.Milliseconds(),
	}
	if rec.Error != "" {
		attrs = append(attrs, "err", rec.Error)
	}
	w.log.Info("checked", attrs...)
}
