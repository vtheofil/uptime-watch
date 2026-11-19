package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vtheofil/uptime-watch/internal/api"
	"github.com/vtheofil/uptime-watch/internal/store"
	"github.com/vtheofil/uptime-watch/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const token = "test-token"

// newServer starts the real router on a real HTTP server, backed by a real database.
func newServer(t *testing.T, adminToken string) (*httptest.Server, *store.Store) {
	t.Helper()
	s := testdb.New(t)
	srv := httptest.NewServer(api.New(s, adminToken, slog.New(slog.DiscardHandler)).Routes())
	t.Cleanup(srv.Close)
	return srv, s
}

func do(t *testing.T, method, url, body, bearer string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestHealthAndReadiness(t *testing.T) {
	srv, _ := newServer(t, token)
	if code, _ := do(t, "GET", srv.URL+"/healthz", "", ""); code != 200 {
		t.Errorf("/healthz = %d, want 200", code)
	}
	if code, _ := do(t, "GET", srv.URL+"/readyz", "", ""); code != 200 {
		t.Errorf("/readyz = %d, want 200", code)
	}
}

func TestDrainingFailsReadinessButNotLiveness(t *testing.T) {
	s := testdb.New(t)
	a := api.New(s, token, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	a.SetDraining()
	if code, _ := do(t, "GET", srv.URL+"/readyz", "", ""); code != 503 {
		t.Errorf("/readyz while draining = %d, want 503", code)
	}
	if code, _ := do(t, "GET", srv.URL+"/healthz", "", ""); code != 200 {
		t.Errorf("/healthz while draining = %d, want 200", code)
	}
}

func TestWriteEndpointsNeedTheToken(t *testing.T) {
	srv, _ := newServer(t, token)
	body := `{"name":"go-dev","url":"https://go.dev"}`

	if code, _ := do(t, "POST", srv.URL+"/api/targets", body, ""); code != 401 {
		t.Errorf("no token: %d, want 401", code)
	}
	if code, _ := do(t, "POST", srv.URL+"/api/targets", body, "wrong"); code != 401 {
		t.Errorf("wrong token: %d, want 401", code)
	}
	if code, _ := do(t, "DELETE", srv.URL+"/api/targets/1", "", ""); code != 401 {
		t.Errorf("delete without token: %d, want 401", code)
	}

	off, _ := newServer(t, "")
	if code, _ := do(t, "POST", off.URL+"/api/targets", body, "anything"); code != 403 {
		t.Errorf("ADMIN_TOKEN unset: %d, want 403", code)
	}
}

func TestCreateTarget(t *testing.T) {
	srv, _ := newServer(t, token)
	url := srv.URL + "/api/targets"

	code, body := do(t, "POST", url, `{"name":"go-dev","url":"https://go.dev","interval_seconds":30}`, token)
	if code != 201 {
		t.Fatalf("create: %d %s, want 201", code, body)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(body), &got)
	if got["name"] != "go-dev" || got["interval_seconds"] != 30.0 || got["timeout_ms"] != 5000.0 || got["last_check"] != nil {
		t.Errorf("created target = %s", body)
	}

	cases := []struct {
		name, body string
		want       int
		wantInBody string
	}{
		{"duplicate name", `{"name":"go-dev","url":"https://go.dev"}`, 409, "already exists"},
		{"not http", `{"name":"ftp","url":"ftp://x.example"}`, 400, "http://"},
		{"no scheme", `{"name":"bare","url":"go.dev"}`, 400, "http://"},
		{"empty name", `{"name":"  ","url":"https://x.example"}`, 400, "name"},
		{"interval too short", `{"name":"fast","url":"https://x.example","interval_seconds":5}`, 400, "at least 10"},
		{"timeout not below interval", `{"name":"slow","url":"https://x.example","interval_seconds":10,"timeout_ms":10000}`, 400, "shorter than"},
		{"unknown field", `{"name":"typo","url":"https://x.example","intervall":30}`, 400, "unknown field"},
		{"not JSON", `name=go`, 400, "invalid JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := do(t, "POST", url, c.body, token)
			if code != c.want || !strings.Contains(body, c.wantInBody) {
				t.Errorf("got %d %s, want %d containing %q", code, body, c.want, c.wantInBody)
			}
		})
	}
}

func TestBodyOver64KBIsRejected(t *testing.T) {
	srv, _ := newServer(t, token)
	big := `{"name":"big","url":"https://x.example/` + strings.Repeat("a", 70<<10) + `"}`
	if code, _ := do(t, "POST", srv.URL+"/api/targets", big, token); code != 413 {
		t.Errorf("70 KB body: %d, want 413", code)
	}
	// Just under the limit is still parsed (and then rejected only for being an invalid target).
	edge := `{"name":"edge","url":"https://x.example/` + strings.Repeat("a", 60<<10) + `"}`
	if code, _ := do(t, "POST", srv.URL+"/api/targets", edge, token); code != 201 {
		t.Errorf("60 KB body: %d, want 201", code)
	}
}

func TestListShowsLatestCheckAndUptime(t *testing.T) {
	srv, s := newServer(t, token)
	ctx := context.Background()
	tg, _ := s.CreateTarget(ctx, store.NewTarget{Name: "go-dev", URL: "https://go.dev"})
	_, _ = s.CreateTarget(ctx, store.NewTarget{Name: "never-checked", URL: "https://x.example"})
	for _, ok := range []bool{true, true, true, false} {
		rec := store.CheckRecord{TargetID: tg.ID, OK: ok, StatusCode: 200, Latency: 100 * time.Millisecond}
		if !ok {
			rec.StatusCode, rec.Error = 0, "timeout"
		}
		_ = s.RecordCheck(ctx, rec)
	}

	code, body := do(t, "GET", srv.URL+"/api/targets", "", "")
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	var list []struct {
		Name      string   `json:"name"`
		Uptime24h *float64 `json:"uptime_24h"`
		LastCheck *struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"last_check"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 2 {
		t.Fatalf("list = %s (%v)", body, err)
	}
	goDev, never := list[0], list[1]
	if goDev.Uptime24h == nil || *goDev.Uptime24h != 75 {
		t.Errorf("uptime = %v, want 75 (3 of 4 up)", goDev.Uptime24h)
	}
	if goDev.LastCheck == nil || goDev.LastCheck.OK || goDev.LastCheck.Error != "timeout" {
		t.Errorf("last check = %+v, want the failed one", goDev.LastCheck)
	}
	if never.LastCheck != nil || never.Uptime24h != nil {
		t.Errorf("never-checked target should have null last_check and uptime_24h: %s", body)
	}
}

func TestChecksAndDelete(t *testing.T) {
	srv, s := newServer(t, token)
	ctx := context.Background()
	tg, _ := s.CreateTarget(ctx, store.NewTarget{Name: "go-dev", URL: "https://go.dev"})
	_ = s.RecordCheck(ctx, store.CheckRecord{TargetID: tg.ID, OK: true, StatusCode: 200})
	base := srv.URL + "/api/targets/"

	if code, body := do(t, "GET", base+"1/checks?limit=10", "", ""); code != 200 || !strings.Contains(body, `"status_code":200`) {
		t.Errorf("checks: %d %s", code, body)
	}
	if code, _ := do(t, "GET", base+"999/checks", "", ""); code != 404 {
		t.Errorf("checks of unknown target: %d, want 404", code)
	}
	if code, _ := do(t, "GET", base+"abc/checks", "", ""); code != 400 {
		t.Errorf("checks with bad id: %d, want 400", code)
	}
	if code, _ := do(t, "GET", base+"1/checks?limit=5000", "", ""); code != 400 {
		t.Errorf("limit 5000: %d, want 400", code)
	}
	if code, _ := do(t, "DELETE", base+"1", "", token); code != 204 {
		t.Errorf("delete: %d, want 204", code)
	}
	if code, _ := do(t, "DELETE", base+"1", "", token); code != 404 {
		t.Errorf("second delete: %d, want 404", code)
	}
}

func TestStatusPage(t *testing.T) {
	srv, s := newServer(t, token)
	ctx := context.Background()

	if code, body := do(t, "GET", srv.URL+"/", "", ""); code != 200 || !strings.Contains(body, "No targets yet") {
		t.Errorf("empty page: %d, missing empty state", code)
	}

	up, _ := s.CreateTarget(ctx, store.NewTarget{Name: "go-dev", URL: "https://go.dev"})
	down, _ := s.CreateTarget(ctx, store.NewTarget{Name: "<script>", URL: "https://x.example"})
	_ = s.RecordCheck(ctx, store.CheckRecord{TargetID: up.ID, OK: true, StatusCode: 200, Latency: 142 * time.Millisecond})
	_ = s.RecordCheck(ctx, store.CheckRecord{TargetID: down.ID, OK: false, Error: "timeout"})

	code, body := do(t, "GET", srv.URL+"/", "", "")
	for _, want := range []string{"go-dev", "UP", "DOWN", "200 · 142 ms", "100.00%", "&lt;script&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("page (status %d) is missing %q", code, want)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Error("a target name was put into the page without escaping")
	}
	if code, _ := do(t, "GET", srv.URL+"/nothing-here", "", ""); code != 404 {
		t.Errorf("unknown path: %d, want 404", code)
	}
}
