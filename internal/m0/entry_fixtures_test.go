package m0

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"testing"
)

type entryControl struct {
	ID         string   `json:"id"`
	Change     string   `json:"change"`
	Obligation string   `json:"obligation"`
	Expected   string   `json:"expected"`
	Neighbors  []string `json:"neighbors"`
}

type entryNeighbor struct {
	ID       string `json:"id"`
	Control  string `json:"control"`
	Expected string `json:"expected"`
}

var entryControlTotal = regexp.MustCompile(`\((\d+) total\)`)

// Claim: executor-entry control ids are unique, every forbidden control names
// at least one valid neighbor witness that exists, and the README count is real.
func TestExecutorEntryControlFixtures(t *testing.T) {
	const dir = "m0-executor-entry"

	var controls []entryControl
	dec := json.NewDecoder(bytes.NewReader(fixture(t, dir, "forbidden-controls.json")))
	dec.DisallowUnknownFields()
	must(t, dec.Decode(&controls))

	var boundary struct {
		ValidNeighbors struct {
			Cases []entryNeighbor `json:"cases"`
		} `json:"valid_neighbors"`
	}
	must(t, json.Unmarshal(fixture(t, dir, "boundary.witnesses.json"), &boundary))

	neighbors := map[string]bool{}
	for _, n := range boundary.ValidNeighbors.Cases {
		if n.ID == "" || n.Control == "" || n.Expected == "" {
			t.Errorf("valid neighbor %q: empty id, control or expected", n.ID)
		}
		if _, dup := neighbors[n.ID]; dup {
			t.Errorf("valid neighbor id %q is not unique", n.ID)
		}
		neighbors[n.ID] = false
	}
	if len(neighbors) == 0 {
		t.Fatal("boundary.witnesses.json has no valid neighbor witnesses")
	}

	seen := map[string]bool{}
	for i, c := range controls {
		if c.ID == "" || c.Change == "" || c.Obligation == "" || c.Expected == "" {
			t.Errorf("control %d (%q): empty id, change, obligation or expected", i, c.ID)
		}
		if seen[c.ID] {
			t.Errorf("control id %q is not unique", c.ID)
		}
		seen[c.ID] = true
		if len(c.Neighbors) == 0 {
			t.Errorf("control %q has no valid neighbor witness", c.ID)
		}
		for _, n := range c.Neighbors {
			if _, ok := neighbors[n]; !ok {
				t.Errorf("control %q names unknown neighbor witness %q", c.ID, n)
				continue
			}
			neighbors[n] = true
		}
	}
	for id, used := range neighbors {
		if !used {
			t.Errorf("valid neighbor witness %q is named by no control", id)
		}
	}

	m := entryControlTotal.FindAllSubmatch(fixture(t, dir, "README.md"), -1)
	if len(m) != 1 {
		t.Fatalf("README.md: want exactly one \"(N total)\" control count, got %d", len(m))
	}
	stated, err := strconv.Atoi(string(m[0][1]))
	must(t, err)
	if stated != len(controls) {
		t.Errorf("README.md states %d controls, forbidden-controls.json has %d", stated, len(controls))
	}
}
