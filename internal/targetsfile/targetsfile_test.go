package targetsfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vtheofil/uptime-watch/internal/store"
	"github.com/vtheofil/uptime-watch/internal/targetsfile"
)

func TestParseReadsEveryField(t *testing.T) {
	got, err := targetsfile.Parse(strings.NewReader(`{"targets": [
		{"name": " github ", "url": "https://github.com", "interval_seconds": 30, "timeout_ms": 2000},
		{"name": "go-dev", "url": "https://go.dev"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []store.NewTarget{
		{Name: "github", URL: "https://github.com", IntervalSeconds: 30, TimeoutMS: 2000},
		{Name: "go-dev", URL: "https://go.dev"}, // zeros: the store fills in the defaults
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestEmptyListIsAllowed(t *testing.T) {
	got, err := targetsfile.Parse(strings.NewReader(`{"targets": []}`))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want an empty list and no error", got, err)
	}
}

func TestParseRejectsMistakes(t *testing.T) {
	cases := []struct{ name, doc, wantInErr string }{
		{"empty file", ``, "invalid JSON"},
		{"targets missing", `{}`, `"targets" is missing`},
		{"targets null", `{"targets": null}`, `"targets" is missing`},
		{"typo in a field", `{"targets": [{"name": "a", "url": "https://a.example", "intervall": 30}]}`, "unknown field"},
		{"interval as text", `{"targets": [{"name": "a", "url": "https://a.example", "interval_seconds": "30s"}]}`, "invalid JSON"},
		{"no scheme", `{"targets": [{"name": "a", "url": "a.example"}]}`, "http://"},
		{"empty name", `{"targets": [{"name": " ", "url": "https://a.example"}]}`, "name is required"},
		{"same name twice", `{"targets": [{"name": "a", "url": "https://a.example"}, {"name": "a", "url": "https://b.example"}]}`, "listed twice"},
		{"two documents", `{"targets": []} {"targets": []}`, "more data"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := targetsfile.Parse(strings.NewReader(c.doc))
			if err == nil || !strings.Contains(err.Error(), c.wantInErr) {
				t.Errorf("err = %v, want one containing %q", err, c.wantInErr)
			}
		})
	}
}

func TestBadEntryIsAnInvalidTarget(t *testing.T) {
	_, err := targetsfile.Parse(strings.NewReader(`{"targets": [{"name": "a", "url": "ftp://a.example"}]}`))
	if !errors.Is(err, store.ErrInvalidTarget) || !strings.Contains(err.Error(), `targets[0] "a"`) {
		t.Errorf("err = %v, want ErrInvalidTarget naming targets[0] \"a\"", err)
	}
}

func TestLoadNamesTheFileInErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := targetsfile.Load(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v, want one naming %s", err, path)
	}
	if _, err := targetsfile.Load(path + ".missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v, want os.ErrNotExist", err)
	}
}
