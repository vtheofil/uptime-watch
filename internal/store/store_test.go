package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vtheofil/uptime-watch/internal/store"
	"github.com/vtheofil/uptime-watch/internal/testdb"
)

// One real Postgres container serves every test in this package.
func TestMain(m *testing.M) { testdb.Main(m) }

func mustCreate(t *testing.T, s *store.Store, nt store.NewTarget) store.Target {
	t.Helper()
	tg, err := s.CreateTarget(context.Background(), nt)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func TestMigrateTwiceAppliesNothingTheSecondTime(t *testing.T) {
	s := testdb.New(t) // already migrated once
	n, err := s.Migrate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second migrate applied %d migrations, want 0", n)
	}
}

func TestCreateTargetUsesDefaults(t *testing.T) {
	s := testdb.New(t)
	tg := mustCreate(t, s, store.NewTarget{Name: "go-dev", URL: "https://go.dev"})

	if tg.ID == 0 || tg.IntervalSeconds != 60 || tg.TimeoutMS != 5000 || !tg.Enabled {
		t.Errorf("got %+v, want an id, interval 60, timeout 5000, enabled", tg)
	}
}

func TestCreateTargetRejectsBadInput(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	mustCreate(t, s, store.NewTarget{Name: "go-dev", URL: "https://go.dev"})

	_, err := s.CreateTarget(ctx, store.NewTarget{Name: "go-dev", URL: "https://other.example"})
	if !errors.Is(err, store.ErrDuplicateName) {
		t.Errorf("duplicate name: err = %v, want ErrDuplicateName", err)
	}

	_, err = s.CreateTarget(ctx, store.NewTarget{Name: "fast", URL: "https://x.example", IntervalSeconds: 5})
	if !errors.Is(err, store.ErrInvalidTarget) {
		t.Errorf("interval 5s: err = %v, want ErrInvalidTarget", err)
	}

	_, err = s.CreateTarget(ctx, store.NewTarget{Name: "slow", URL: "https://x.example", IntervalSeconds: 10, TimeoutMS: 10000})
	if !errors.Is(err, store.ErrInvalidTarget) {
		t.Errorf("timeout equal to interval: err = %v, want ErrInvalidTarget", err)
	}
}

func TestClaimDueGivesEachTargetOncePerInterval(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b", "c"} {
		mustCreate(t, s, store.NewTarget{Name: name, URL: "https://" + name + ".example"})
	}
	disabled := mustCreate(t, s, store.NewTarget{Name: "off", URL: "https://off.example"})
	testdb.Exec(t, `UPDATE targets SET enabled = false WHERE id = $1`, disabled.ID)

	first, err := s.ClaimDue(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := s.ClaimDue(ctx, 2)
	third, _ := s.ClaimDue(ctx, 2)

	if len(first) != 2 || len(second) != 1 || len(third) != 0 {
		t.Errorf("claimed %d, %d, %d targets; want 2, 1, 0 (the disabled one never)",
			len(first), len(second), len(third))
	}
	if len(first) > 0 && first[0].Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", first[0].Timeout)
	}
}

func TestConcurrentClaimsNeverShareATarget(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	const targets = 60
	for i := range targets {
		mustCreate(t, s, store.NewTarget{Name: fmt.Sprintf("t%02d", i), URL: "https://x.example"})
	}

	// Five "workers" claim in parallel until nothing is due.
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				due, err := s.ClaimDue(ctx, 4)
				if err != nil {
					t.Error(err)
					return
				}
				if len(due) == 0 {
					return
				}
				mu.Lock()
				for _, d := range due {
					seen[d.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != targets {
		t.Errorf("claimed %d distinct targets, want %d", len(seen), targets)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("target %d was claimed %d times", id, n)
		}
	}
}

func TestRecordAndReadChecks(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	tg := mustCreate(t, s, store.NewTarget{Name: "go-dev", URL: "https://go.dev"})

	if err := s.RecordCheck(ctx, store.CheckRecord{TargetID: tg.ID, OK: true, StatusCode: 200, Latency: 142 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCheck(ctx, store.CheckRecord{TargetID: tg.ID, OK: false, Latency: 5 * time.Second, Error: "timeout"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.RecentChecks(ctx, tg.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d checks, want 2", len(got))
	}
	newest, oldest := got[0], got[1]
	if newest.OK || newest.StatusCode != 0 || newest.Error != "timeout" || newest.LatencyMS != 5000 {
		t.Errorf("newest = %+v, want the failed check with no status", newest)
	}
	if !oldest.OK || oldest.StatusCode != 200 || oldest.LatencyMS != 142 {
		t.Errorf("oldest = %+v, want the 200 in 142ms", oldest)
	}
}

func TestDeleteTargetRemovesItsChecks(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	tg := mustCreate(t, s, store.NewTarget{Name: "go-dev", URL: "https://go.dev"})
	_ = s.RecordCheck(ctx, store.CheckRecord{TargetID: tg.ID, OK: true, StatusCode: 200})

	if err := s.DeleteTarget(ctx, tg.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.RecentChecks(ctx, tg.ID, 10); len(left) != 0 {
		t.Errorf("%d checks left after delete, want 0", len(left))
	}
	if err := s.DeleteTarget(ctx, tg.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

func TestPruneDeletesOnlyOldChecks(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	tg := mustCreate(t, s, store.NewTarget{Name: "go-dev", URL: "https://go.dev"})
	_ = s.RecordCheck(ctx, store.CheckRecord{TargetID: tg.ID, OK: true, StatusCode: 200})
	_ = s.RecordCheck(ctx, store.CheckRecord{TargetID: tg.ID, OK: true, StatusCode: 200})
	testdb.Exec(t, `UPDATE checks SET checked_at = now() - interval '40 days' WHERE id = 1`)

	n, err := s.Prune(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := s.RecentChecks(ctx, tg.ID, 10)
	if n != 1 || len(left) != 1 {
		t.Errorf("pruned %d and kept %d, want 1 and 1", n, len(left))
	}
}
