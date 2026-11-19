package metrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/vtheofil/uptime-watch/internal/metrics"
	"github.com/vtheofil/uptime-watch/internal/store"
	"github.com/vtheofil/uptime-watch/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func TestStatusCollectorReportsEnabledTargetsFromTheDatabase(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()
	up, _ := s.CreateTarget(ctx, store.NewTarget{Name: "up", URL: "https://a.example"})
	down, _ := s.CreateTarget(ctx, store.NewTarget{Name: "down", URL: "https://b.example", IntervalSeconds: 30})
	_, _ = s.CreateTarget(ctx, store.NewTarget{Name: "never", URL: "https://c.example"})
	off, _ := s.CreateTarget(ctx, store.NewTarget{Name: "off", URL: "https://d.example"})
	_ = s.RecordCheck(ctx, store.CheckRecord{TargetID: up.ID, OK: true, StatusCode: 200})
	_ = s.RecordCheck(ctx, store.CheckRecord{TargetID: down.ID, OK: false, Error: "timeout"})
	_ = s.RecordCheck(ctx, store.CheckRecord{TargetID: off.ID, OK: false, Error: "timeout"})
	testdb.Exec(t, `UPDATE targets SET enabled = false WHERE id = $1`, off.ID)

	c := metrics.NewStatusCollector(s)
	want := `
# HELP uptime_target_interval_seconds How often the target should be checked.
# TYPE uptime_target_interval_seconds gauge
uptime_target_interval_seconds{target="down"} 30
uptime_target_interval_seconds{target="never"} 60
uptime_target_interval_seconds{target="up"} 60
# HELP uptime_target_up 1 if the latest check of the target was up, 0 if down.
# TYPE uptime_target_up gauge
uptime_target_up{target="down"} 0
uptime_target_up{target="up"} 1
`
	// "never" has no up value yet, and the disabled "off" target is left out.
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "uptime_target_up", "uptime_target_interval_seconds"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(c, "uptime_target_last_check_timestamp_seconds"); n != 2 {
		t.Errorf("last-check timestamps = %d, want 2", n)
	}
}

func TestHTTPMetricsUseTheRoutePatternNotThePath(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewHTTP(reg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/targets/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	srv := httptest.NewServer(m.Wrap(mux))
	defer srv.Close()

	for _, path := range []string{"/api/targets/1", "/api/targets/2", "/nope"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	want := `
# HELP http_requests_total HTTP requests served, by route and status code.
# TYPE http_requests_total counter
http_requests_total{code="404",route="unmatched"} 1
http_requests_total{code="418",route="GET /api/targets/{id}"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "http_requests_total"); err != nil {
		t.Error(err)
	}
}
