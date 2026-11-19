package worker_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/vtheofil/uptime-watch/internal/metrics"
	"github.com/vtheofil/uptime-watch/internal/store"
	"github.com/vtheofil/uptime-watch/internal/testdb"
	"github.com/vtheofil/uptime-watch/internal/worker"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// startWorker runs a worker in the background with a fast poll interval and
// no grace period. The returned stop function cancels it and waits for Run.
func startWorker(t *testing.T, s *store.Store, concurrency int) (stop func()) {
	t.Helper()
	return startWith(t, s, worker.Options{Concurrency: concurrency})
}

func startWith(t *testing.T, s *store.Store, o worker.Options) (stop func()) {
	t.Helper()
	o.Poll = 20 * time.Millisecond
	o.Log = slog.New(slog.DiscardHandler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	w := worker.New(s, o)
	go func() {
		w.Run(ctx)
		close(done)
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func create(t *testing.T, s *store.Store, name, url string) store.Target {
	t.Helper()
	tg, err := s.CreateTarget(context.Background(), store.NewTarget{Name: name, URL: url})
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func checksOf(t *testing.T, s *store.Store, id int64) []store.Check {
	t.Helper()
	c, err := s.RecentChecks(context.Background(), id, 100)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWorkerChecksTargetsAndStoresResults(t *testing.T) {
	s := testdb.New(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()

	a := create(t, s, "up", up.URL)
	b := create(t, s, "down", down.URL)

	reg := prometheus.NewRegistry()
	m := metrics.NewWorker(reg)
	stop := startWith(t, s, worker.Options{Concurrency: 5, Metrics: m})
	waitFor(t, "both targets to be checked", func() bool {
		return len(checksOf(t, s, a.ID)) > 0 && len(checksOf(t, s, b.ID)) > 0
	})
	stop()

	if c := checksOf(t, s, a.ID)[0]; !c.OK || c.StatusCode != 200 {
		t.Errorf("up target: got %+v, want ok with 200", c)
	}
	if c := checksOf(t, s, b.ID)[0]; c.OK || c.StatusCode != 503 {
		t.Errorf("down target: got %+v, want down with 503", c)
	}

	want := `
# HELP uptime_checks_total Checks finished, by target and result (up or down).
# TYPE uptime_checks_total counter
uptime_checks_total{result="down",target="down"} 1
uptime_checks_total{result="up",target="up"} 1
# HELP uptime_worker_inflight Checks running right now in this worker.
# TYPE uptime_worker_inflight gauge
uptime_worker_inflight 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "uptime_checks_total", "uptime_worker_inflight"); err != nil {
		t.Error(err)
	}
}

func TestStopLetsRunningChecksFinishWithinGrace(t *testing.T) {
	s := testdb.New(t)
	started := make(chan struct{}, 1)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		time.Sleep(300 * time.Millisecond)
	}))
	defer slow.Close()
	tg := create(t, s, "slow", slow.URL)

	stop := startWith(t, s, worker.Options{Concurrency: 1, Grace: 5 * time.Second})
	<-started
	stop() // SIGTERM arrives while the check is running

	c := checksOf(t, s, tg.ID)
	if len(c) != 1 || !c[0].OK {
		t.Errorf("got %+v, want the running check to finish and be stored as up", c)
	}
}

func TestWorkerNeverRunsMoreChecksThanItsLimit(t *testing.T) {
	s := testdb.New(t)
	var inflight, peak atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
		inflight.Add(-1)
	}))
	defer slow.Close()

	var ids []int64
	for i := range 9 {
		ids = append(ids, create(t, s, fmt.Sprintf("t%d", i), slow.URL).ID)
	}

	startWorker(t, s, 3)
	waitFor(t, "all 9 targets to be checked", func() bool {
		for _, id := range ids {
			if len(checksOf(t, s, id)) == 0 {
				return false
			}
		}
		return true
	})

	if got := peak.Load(); got != 3 {
		t.Errorf("peak parallel checks = %d, want exactly 3 (the limit)", got)
	}
}

func TestTwoWorkersNeverCheckTheSameTargetTwice(t *testing.T) {
	s := testdb.New(t)
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
	}))
	defer srv.Close()

	const n = 20
	var ids []int64
	for i := range n {
		ids = append(ids, create(t, s, fmt.Sprintf("t%02d", i), fmt.Sprintf("%s/t%02d", srv.URL, i)).ID)
	}

	// Two workers share one database, like two replicas in Kubernetes.
	startWorker(t, s, 4)
	startWorker(t, s, 4)
	waitFor(t, "all targets to be checked", func() bool {
		for _, id := range ids {
			if len(checksOf(t, s, id)) == 0 {
				return false
			}
		}
		return true
	})
	time.Sleep(200 * time.Millisecond) // give a duplicate the chance to show up

	mu.Lock()
	defer mu.Unlock()
	if len(hits) != n {
		t.Errorf("%d distinct targets were hit, want %d", len(hits), n)
	}
	for path, count := range hits {
		if count != 1 {
			t.Errorf("%s was checked %d times within one interval, want 1", path, count)
		}
	}
}

// With no grace left, a hanging check is cancelled at once and not stored.
func TestWorkerStopsPromptlyAndDoesNotStoreCancelledChecks(t *testing.T) {
	s := testdb.New(t)
	started := make(chan struct{}, 1)
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done() // never answers
	}))
	defer hang.Close()

	tg, err := s.CreateTarget(context.Background(), store.NewTarget{
		Name: "hang", URL: hang.URL, IntervalSeconds: 60, TimeoutMS: 30000,
	})
	if err != nil {
		t.Fatal(err)
	}

	stop := startWorker(t, s, 1)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never started the check")
	}

	begin := time.Now()
	stop()
	if took := time.Since(begin); took > time.Second {
		t.Errorf("worker took %v to stop, want well under a second", took)
	}
	if c := checksOf(t, s, tg.ID); len(c) != 0 {
		t.Errorf("stored %d checks for a check we cancelled, want 0", len(c))
	}
}
