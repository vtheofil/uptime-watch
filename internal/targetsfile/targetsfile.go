// Package targetsfile reads the list of targets kept in git ("targets as
// code"). The Helm chart renders the list from its values into a ConfigMap:
//
//	{"targets": [{"name": "github", "url": "https://github.com", "interval_seconds": 30}]}
//
// It is JSON, not YAML, because Helm writes it (toJson) and the standard
// library reads it: no extra dependency. People edit the YAML values, not this.
package targetsfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vtheofil/uptime-watch/internal/store"
)

type file struct {
	// A pointer, so a missing "targets" key (nil) differs from an empty list.
	// An empty list deletes every git target; a missing key is a mistake.
	Targets *[]entry `json:"targets"`
}

type entry struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	IntervalSeconds int    `json:"interval_seconds"` // optional; 0 means the default
	TimeoutMS       int    `json:"timeout_ms"`       // optional; 0 means the default
}

// Load reads and checks the file at path.
func Load(path string) ([]store.NewTarget, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	targets, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return targets, nil
}

// Parse reads the JSON document and checks every entry, so that a mistake
// fails before the database is touched. Unknown fields are errors: a typo
// like "intervall" must not be ignored silently.
func Parse(r io.Reader) ([]store.NewTarget, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var doc file
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid JSON: more data after the document")
	}
	if doc.Targets == nil {
		return nil, errors.New(`"targets" is missing; write "targets": [] to remove every git target`)
	}

	out := make([]store.NewTarget, 0, len(*doc.Targets))
	seen := map[string]bool{}
	for i, e := range *doc.Targets {
		nt := store.NewTarget{
			Name: strings.TrimSpace(e.Name), URL: e.URL,
			IntervalSeconds: e.IntervalSeconds, TimeoutMS: e.TimeoutMS,
		}
		if err := nt.Validate(); err != nil {
			return nil, fmt.Errorf("targets[%d] %q: %w", i, nt.Name, err)
		}
		if seen[nt.Name] {
			return nil, fmt.Errorf("targets[%d]: the name %q is listed twice", i, nt.Name)
		}
		seen[nt.Name] = true
		out = append(out, nt)
	}
	return out, nil
}
