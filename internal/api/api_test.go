package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// postForm submits the add form like a browser does, without following the
// redirect, and returns the status, the body and the Location header.
func postForm(t *testing.T, base string, form url.Values) (int, string, string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.PostForm(base+"/targets", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header.Get("Location")
}

func TestAddFormCreatesATargetAndRedirects(t *testing.T) {
	srv, s := newServer(t, token)

	if code, body := do(t, "GET", srv.URL+"/", "", ""); !strings.Contains(body, `href="/targets/new"`) {
		t.Errorf("status page (%d) has no link to the form", code)
	}
	if code, body := do(t, "GET", srv.URL+"/targets/new", "", ""); code != 200 || !strings.Contains(body, `name="token"`) {
		t.Errorf("form page: %d, want 200 with a token field", code)
	}

	code, _, location := postForm(t, srv.URL, url.Values{
		"name": {" go-dev "}, "url": {"https://go.dev"}, "interval_seconds": {"30"}, "timeout_ms": {""}, "token": {token},
	})
	if code != http.StatusSeeOther || location != "/?added=go-dev" {
		t.Fatalf("post: %d to %q, want 303 to /?added=go-dev", code, location)
	}
	list, _ := s.ListTargets(context.Background())
	if len(list) != 1 || list[0].Name != "go-dev" || list[0].IntervalSeconds != 30 || list[0].TimeoutMS != 5000 || list[0].Source != store.SourceAPI {
		t.Errorf("stored %+v, want go-dev, interval 30, default timeout, source api", list)
	}
	if _, body := do(t, "GET", srv.URL+location, "", ""); !strings.Contains(body, "Added <b>go-dev</b>") {
		t.Error("the status page does not confirm the new target")
	}
	// The note only names targets that exist: a crafted link shows nothing.
	if _, body := do(t, "GET", srv.URL+"/?added=you+were+hacked", "", ""); strings.Contains(body, "hacked") {
		t.Error("the status page repeats text from the link")
	}
}

func TestAddFormRejectsMistakesAndKeepsWhatWasTyped(t *testing.T) {
	srv, s := newServer(t, token)
	_, _ = s.CreateTarget(context.Background(), store.NewTarget{Name: "taken", URL: "https://x.example"})

	good := func() url.Values {
		return url.Values{"name": {"go-dev"}, "url": {"https://go.dev"}, "token": {token}}
	}
	cases := []struct {
		name   string
		change func(url.Values)
		want   int
		inBody string
	}{
		{"wrong token", func(v url.Values) { v.Set("token", "nope") }, 401, "wrong admin token"},
		{"no token", func(v url.Values) { v.Del("token") }, 401, "wrong admin token"},
		{"bad url", func(v url.Values) { v.Set("url", "ftp://x.example") }, 400, "http://"},
		{"interval not a number", func(v url.Values) { v.Set("interval_seconds", "30s") }, 400, "whole numbers"},
		{"interval too short", func(v url.Values) { v.Set("interval_seconds", "5") }, 400, "at least 10"},
		{"name taken", func(v url.Values) { v.Set("name", "taken") }, 409, "already exists"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := good()
			c.change(v)
			code, body, _ := postForm(t, srv.URL, v)
			if code != c.want || !strings.Contains(body, c.inBody) {
				t.Errorf("got %d, want %d with %q in the page", code, c.want, c.inBody)
			}
			if !strings.Contains(body, `value="https://go.dev"`) && c.name != "bad url" {
				t.Error("the form lost the URL that was typed")
			}
			if strings.Contains(body, token) {
				t.Error("the admin token was put back into the page")
			}
		})
	}
	if list, _ := s.ListTargets(context.Background()); len(list) != 1 {
		t.Errorf("%d targets stored, want only the existing one", len(list))
	}
}

func TestAddFormIsOffWithoutAToken(t *testing.T) {
	srv, s := newServer(t, "")
	if _, body := do(t, "GET", srv.URL+"/", "", ""); strings.Contains(body, `href="/targets/new"`) {
		t.Error("the status page links to a form that cannot work")
	}
	if _, body := do(t, "GET", srv.URL+"/targets/new", "", ""); !strings.Contains(body, "Adding targets is off") {
		t.Error("the form page does not say it is off")
	}
	code, _, _ := postForm(t, srv.URL, url.Values{"name": {"go-dev"}, "url": {"https://go.dev"}, "token": {""}})
	if code != 403 {
		t.Errorf("post with ADMIN_TOKEN unset: %d, want 403", code)
	}
	if list, _ := s.ListTargets(context.Background()); len(list) != 0 {
		t.Errorf("%d targets stored, want 0", len(list))
	}
}

func TestAddFormBodyLimit(t *testing.T) {
	srv, _ := newServer(t, token)
	big := url.Values{"name": {"big"}, "url": {"https://x.example/" + strings.Repeat("a", 70<<10)}, "token": {token}}
	if code, _, _ := postForm(t, srv.URL, big); code != 400 {
		t.Errorf("70 KB form: %d, want 400", code)
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
