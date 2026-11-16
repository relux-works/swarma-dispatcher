package m0

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	entryFixtureDir      = "m0-executor-entry"
	entryGenericNeighbor = "matching-control"
	entryNeighborSection = "valid_neighbors"
	entryMaskedPhrase    = "Hypothetical after"
	entryReachableAfter  = "run-claude pin recorded in spec §4"
	entry78Qualifier     = "78 only with trustworthy original fate evidence of no runner start"
)

type entryControl struct {
	ID             string   `json:"id"`
	Change         string   `json:"change"`
	Obligation     string   `json:"obligation"`
	Expected       string   `json:"expected"`
	Neighbors      []string `json:"neighbors"`
	ReachableAfter *string  `json:"reachable_after"`
}

// entryCase is any boundary case: only its id and, when present, its expected
// text are read. Other members differ per section.
type entryCase struct {
	ID       string `json:"id"`
	Expected string `json:"expected"`
}

type entryNeighbor struct {
	ID       string `json:"id"`
	Control  string `json:"control"`
	Expected string `json:"expected"`
}

var (
	entryControlTotal = regexp.MustCompile(`\((\d+) total\)`)
	entryBackQuoted   = regexp.MustCompile("`([^`\n]+)`")
	entryMentions78   = regexp.MustCompile(`(^|[^0-9])78([^0-9]|$)`)
)

// entryFixtureFiles are all JSON files of the family; each must be one
// document with no duplicate member name.
var entryFixtureFiles = []string{
	"forbidden-controls.json",
	"boundary.witnesses.json",
	"entry.specimen.json",
	"mapping.witness.json",
}

// Claim: only these 27 controls have matching-control as an exact single-mutation neighbor.
// Every other control must name a witness for its own property; a listed id
// that no longer names matching-control alone also fails the check.
var entryGenericNeighborAllowed = []string{
	"missing", "extra", "null", "duplicate", "escaped-duplicate",
	"invalid-utf8", "bom", "surrogate", "noninteger", "negative-zero",
	"range", "depth", "size", "escape-size", "identity", "generation",
	"association", "pins", "opaque", "selectors", "injected-member",
	"catalog", "pin-drift", "missing-timing", "tampered-timing",
	"home-prompt", "runner-variant-unpinned",
}

// entryReadmeLiterals are the back-quoted names in the fixture README that
// are neither control ids nor case ids. Any other back-quoted name must exist
// as an id, so a misspelled or removed control id in the README fails.
var entryReadmeLiterals = map[string]bool{
	"forbidden-controls.json": true, "boundary.witnesses.json": true,
	"entry.specimen.json": true, "mapping.witness.json": true,
	"change": true, "expected": true, "neighbors": true,
	"reachable_after": true, entryNeighborSection: true,
	"deadline_ms": true, "slack_ms": true, "now_ms": true,
	"run-codex": true, "run-claude": true, "/usr/bin:/bin": true,
	"pipe2": true, "pipe2(O_CLOEXEC)": true, "F_GETPIPE_SZ": true,
	"F_SETFL": true, "EINTR": true, "close": true, "dup2": true,
}

// entryWalk checks all four JSON documents, not just fields decoded below.
// Member names are ASCII and lowercase except the three existing mixed-case names.
// This rejects encoding/json's case-insensitive aliases without imposing
// executor-entry grammar limits on these documentary files.
func entryWalk(t *testing.T, name string, b []byte) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	var walk func(path string) error
	walk = func(path string) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if s, ok := tok.(string); ok && strings.HasSuffix(path, "/expected") &&
			entryMentions78.MatchString(s) && !strings.Contains(s, entry78Qualifier) && !strings.Contains(s, "UNKNOWN") {
			t.Errorf("%s: expected at %q mentions 78 without qualifier or UNKNOWN", name, path)
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := kt.(string)
				if !ok {
					return fmt.Errorf("%s: member name is not a string", path)
				}
				if seen[key] {
					t.Errorf("%s: duplicate member name %q in object %q", name, key, path)
				}
				seen[key] = true
				want := strings.ToLower(key)
				switch want {
				case "e":
					want = "E"
				case "h":
					want = "H"
				case "later_runner_versionprobe":
					want = "later_runner_versionProbe"
				}
				if key != want || strings.IndexFunc(key, func(r rune) bool { return r > 127 }) >= 0 {
					t.Errorf("%s: wrong-case member name %q in object %q, want exactly %q", name, key, path, want)
				}
				if err := walk(path + "/" + key); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case json.Delim('['):
			for i := 0; dec.More(); i++ {
				if err := walk(path + "/" + strconv.Itoa(i)); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	if err := walk(""); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		t.Errorf("%s: trailing data after the JSON document", name)
	}
}

// entryDecode decodes exactly one JSON value and requires EOF after it.
func entryDecode(t *testing.T, name string, b []byte, v any, strict bool) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("%s: trailing data after the JSON document", name)
	}
}

// Claim: the executor-entry fixture files agree with each other.
//   - Each JSON file is one document: no trailing data, no duplicate member
//     name, and no wrong-case spelling of any member name.
//   - Control ids and boundary case ids are unique; a neighbor may be any
//     case id of boundary.witnesses.json and must resolve; every
//     valid_neighbors case is named by a control.
//   - Only the allowed rows name matching-control alone.
//   - Every expected that mentions 78 carries the qualifier or says UNKNOWN.
//     One-mechanism-per-row semantics require review, not a prose parser.
//   - A control carries reachable_after, with the fixed value, exactly when
//     one of its neighbors is masked until the run-claude pin.
//   - Every back-quoted name in the README is a control id, a case id or a
//     known literal, and the README count is real.
//
// This is documentary consistency only; it proves nothing about an adapter.
func TestExecutorEntryControlFixtures(t *testing.T) {
	const dir = entryFixtureDir

	for _, name := range entryFixtureFiles {
		entryWalk(t, name, fixture(t, dir, name))
	}

	var controls []entryControl
	entryDecode(t, "forbidden-controls.json", fixture(t, dir, "forbidden-controls.json"), &controls, true)

	var sections map[string]json.RawMessage
	entryDecode(t, "boundary.witnesses.json", fixture(t, dir, "boundary.witnesses.json"), &sections, false)

	// Case ids from every section; ids are globally unique.
	cases := map[string]entryCase{}
	used := map[string]bool{} // valid_neighbors case id -> named by a control
	for section, raw := range sections {
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var body struct {
			Cases []json.RawMessage `json:"cases"`
		}
		entryDecode(t, "boundary.witnesses.json/"+section, raw, &body, false)
		for i, rc := range body.Cases {
			where := fmt.Sprintf("boundary.witnesses.json/%s/cases/%d", section, i)
			var c entryCase
			entryDecode(t, where, rc, &c, false)
			if c.ID == "" {
				t.Errorf("%s: empty id", where)
				continue
			}
			if _, dup := cases[c.ID]; dup {
				t.Errorf("boundary case id %q is not unique", c.ID)
			}
			cases[c.ID] = c
			if section != entryNeighborSection {
				continue
			}
			var n entryNeighbor
			entryDecode(t, where, rc, &n, true)
			if n.Control == "" || n.Expected == "" {
				t.Errorf("valid neighbor %q: empty control or expected", n.ID)
			}
			used[n.ID] = false
		}
	}
	if len(used) == 0 {
		t.Fatal("boundary.witnesses.json has no valid neighbor witnesses")
	}
	if _, ok := used[entryGenericNeighbor]; !ok {
		t.Errorf("boundary.witnesses.json has no %q valid neighbor", entryGenericNeighbor)
	}

	allowed := map[string]bool{} // id -> seen naming matching-control alone
	for _, id := range entryGenericNeighborAllowed {
		allowed[id] = false
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
		if _, clash := cases[c.ID]; clash {
			t.Errorf("control id %q is also a boundary case id", c.ID)
		}
		if len(c.Neighbors) == 0 {
			t.Errorf("control %q has no valid neighbor witness", c.ID)
		}
		onlyGeneric := len(c.Neighbors) > 0
		masked := false
		for _, n := range c.Neighbors {
			w, ok := cases[n]
			if !ok {
				t.Errorf("control %q names unknown neighbor witness %q", c.ID, n)
				onlyGeneric = false
				continue
			}
			if _, ok := used[n]; ok {
				used[n] = true
			}
			if n != entryGenericNeighbor {
				onlyGeneric = false
			}
			if strings.Contains(w.Expected, entryMaskedPhrase) {
				masked = true
			}
		}
		if onlyGeneric {
			if _, ok := allowed[c.ID]; !ok {
				t.Errorf("control %q names only %s but is not on the allowed list", c.ID, entryGenericNeighbor)
			} else {
				allowed[c.ID] = true
			}
		}

		switch {
		case masked && c.ReachableAfter == nil:
			t.Errorf("control %q: a neighbor is masked (%q) but reachable_after is missing", c.ID, entryMaskedPhrase)
		case masked && *c.ReachableAfter != entryReachableAfter:
			t.Errorf("control %q: reachable_after is %q, want %q", c.ID, *c.ReachableAfter, entryReachableAfter)
		case !masked && c.ReachableAfter != nil:
			t.Errorf("control %q: reachable_after is set but no neighbor is masked (%q)", c.ID, entryMaskedPhrase)
		}
	}
	for id, ok := range used {
		if !ok {
			t.Errorf("valid neighbor witness %q is named by no control", id)
		}
	}
	for id, ok := range allowed {
		if !ok {
			t.Errorf("allowed list entry %q is stale: no control with that id names only %s", id, entryGenericNeighbor)
		}
	}

	readme := fixture(t, dir, "README.md")
	for _, m := range entryBackQuoted.FindAllSubmatch(readme, -1) {
		name := string(m[1])
		_, isCase := cases[name]
		if !seen[name] && !isCase && !entryReadmeLiterals[name] {
			t.Errorf("README.md names %q, which is no control id, case id or known literal", name)
		}
	}
	m := entryControlTotal.FindAllSubmatch(readme, -1)
	if len(m) != 1 {
		t.Fatalf("README.md: want exactly one \"(N total)\" control count, got %d", len(m))
	}
	stated, err := strconv.Atoi(string(m[0][1]))
	must(t, err)
	if stated != len(controls) {
		t.Errorf("README.md states %d controls, forbidden-controls.json has %d", stated, len(controls))
	}
}
