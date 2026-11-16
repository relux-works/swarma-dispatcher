package m0

import (
	"strings"
	"testing"
)

func specimenEntry(t *testing.T) *Entry {
	t.Helper()
	doc := parseDoc(t, fixture(t, "m0-executor-entry", "entry.specimen.json"))
	req, err := decodeUnsigned(doc.member("request"))
	must(t, err)
	text := func(v *value, k string) string {
		s, ok := v.member(k).text()
		if !ok {
			t.Fatalf("specimen member %s", k)
		}
		return s
	}
	num := func(v *value, k string) int64 {
		n, ok := v.member(k).number()
		if !ok {
			t.Fatalf("specimen member %s", k)
		}
		return n
	}
	ex := doc.member("execution")
	tm := doc.member("timing")
	return &Entry{
		Request:       req,
		Execution:     Execution{EffectID: text(ex, "effect_id"), ExecutionID: text(ex, "execution_id"), ControllerEpoch: uint64(num(ex, "controller_epoch"))},
		Catalog:       text(doc, "catalog"),
		Qualification: text(doc, "qualification"),
		Timing:        Timing{DeadlineMs: num(tm, "deadline_ms"), SlackMs: num(tm, "slack_ms")},
		Input:         specimenInput(t),
	}
}

// Claim: the composed entry reproduces the published mapping witness: CCJ-1
// of the input is 186 bytes with the recorded digest (the specimen's
// worker_plan), CCJ-1 of the entire entry is 1494 bytes with the recorded
// digest, 9 objects and 43 members. Adding one LF to the prompt changes both
// digests and refuses the worker_plan equality.
func TestEntrySpecimenWitness(t *testing.T) {
	witness := parseDoc(t, fixture(t, "m0-executor-entry", "mapping.witness.json"))
	str := func(k string) string {
		s, ok := witness.member(k).text()
		if !ok {
			t.Fatalf("witness member %s", k)
		}
		return s
	}
	num := func(k string) int64 {
		n, ok := witness.member(k).number()
		if !ok {
			t.Fatalf("witness member %s", k)
		}
		return n
	}
	e := specimenEntry(t)
	inputBytes, inputDigest, err := e.Input.Canonical()
	must(t, err)
	if int64(len(inputBytes)) != num("input_ccj1_bytes") || inputDigest != str("input_ccj1_sha256") || inputDigest != e.Request.Plan.WorkerPlan {
		t.Fatalf("WITNESS: input %d bytes %s", len(inputBytes), inputDigest)
	}
	entryBytes, entryDigest, err := e.Canonical()
	must(t, err)
	if int64(len(entryBytes)) != num("entry_ccj1_bytes") || entryDigest != str("entry_ccj1_sha256") {
		t.Fatalf("WITNESS: entry %d bytes %s", len(entryBytes), entryDigest)
	}
	parsed, _, err := canonicalize(entryBytes, entryLimits)
	must(t, err)
	objects, members, _ := parsed.stats()
	if int64(objects) != num("entry_objects") || int64(members) != num("entry_members") {
		t.Fatalf("WITNESS: %d objects %d members", objects, members)
	}
	mutant := *e
	mutant.Input.Prompt += "\n"
	_, mutantDigest, err := mutant.Input.Canonical()
	must(t, err)
	if mutantDigest == inputDigest {
		t.Fatal("WITNESS: added LF did not change the input digest")
	}
	if _, _, err := mutant.Canonical(); CodeOf(err) != CodePlanDigestMismatch {
		t.Fatalf("WITNESS: worker_plan inequality not refused: %v", err)
	}
}

// Claim: the Codex mapping yields exactly the witness argv (19 elements, 392
// NUL-counted bytes) for the specimen with now=20000 ms, and T follows
// min(240, floor((D-now-S)/1000)): 260 s remaining caps at 240, a 1000 ms
// remainder gives 1, 900 ms refuses, and overflow-range inputs refuse. The
// unpinned Claude variant refuses by name.
func TestCodexArgvWitness(t *testing.T) {
	witness := parseDoc(t, fixture(t, "m0-executor-entry", "mapping.witness.json"))
	want := witness.member("argv")
	e := specimenEntry(t)
	runner := CodexRunner{Executable: "/example/protected/swarma-credential-broker", Home: "/example/station"}
	got, err := runner.Argv(e, 20000)
	must(t, err)
	if len(got) != len(want.items) || len(got) != 19 {
		t.Fatalf("ARGV: %d elements", len(got))
	}
	total := 0
	for i, item := range want.items {
		s, _ := item.text()
		if got[i] != s {
			t.Errorf("ARGV: element %d %q want %q", i, got[i], s)
		}
		total += len(got[i]) + 1
	}
	if n, _ := witness.member("argv_nul_terminated_bytes").number(); int64(total) != n {
		t.Errorf("ARGV: %d NUL-counted bytes", total)
	}
	for _, tc := range []struct {
		d, now, s, want int64
		code            Code
	}{
		{290000, 20000, 10000, 240, ""},
		{21000, 10000, 10000, 1, ""},
		{20900, 10000, 10000, 0, CodeDeadlineExhausted},
		{10000, 20000, 10000, 0, CodeDeadlineExhausted},
		{maxSafeInteger + 1, 0, 1, 0, CodeTimeUnknown},
		{1000, 0, 0, 0, CodeTimeUnknown},
	} {
		got, err := RunnerTimeout(tc.d, tc.now, tc.s)
		if CodeOf(err) != tc.code || got != tc.want {
			t.Errorf("TIMEOUT: D=%d now=%d S=%d -> %d %v", tc.d, tc.now, tc.s, got, err)
		}
	}
	if _, err := (UnpinnedRunner{}).Argv(e, 20000); CodeOf(err) != CodeSeamUnpinned || !strings.Contains(err.Error(), string(SeamRunner)) {
		t.Errorf("ARGV: unpinned runner did not refuse by name: %v", err)
	}
	if _, err := (CodexRunner{Executable: "relative", Home: "/h"}).Argv(e, 20000); CodeOf(err) != CodePolicyMismatch {
		t.Errorf("ARGV: relative installed literal accepted: %v", err)
	}
	long := CodexRunner{Executable: "/e", Home: "/" + strings.Repeat("h", 1010)}
	if _, err := long.Argv(e, 20000); CodeOf(err) != CodeRequestTooLarge {
		t.Errorf("ARGV: derived path over 1024 bytes accepted: %v", err)
	}
}

// Claim: the input body refuses each shape violation one step past its bound
// beside the accepted boundary: selector 64/65 bytes and leading '-',
// catalog string 128/129 bytes and leading '-', prompt 24576/24577 bytes and
// U+0000, with the entry ceilings still met for the maximal prompt.
func TestInputBounds(t *testing.T) {
	base := specimenInput(t)
	check := func(name string, edit func(*Input), ok bool) {
		t.Helper()
		in := base
		edit(&in)
		_, _, err := in.Canonical()
		if ok && err != nil {
			t.Errorf("INPUT: %s refused: %v", name, err)
		}
		if !ok && CodeOf(err) != CodeRequestInvalid {
			t.Errorf("INPUT: %s accepted: %v", name, err)
		}
	}
	check("selector-64", func(i *Input) { i.Account = strings.Repeat("a", 64) }, true)
	check("selector-65", func(i *Input) { i.Account = strings.Repeat("a", 65) }, false)
	check("selector-dash-first", func(i *Input) { i.Account = "-a" }, false)
	check("selector-upper", func(i *Input) { i.Profile = "A" }, false)
	check("catalog-128", func(i *Input) { i.Model = strings.Repeat("m", 128) }, true)
	check("catalog-129", func(i *Input) { i.Model = strings.Repeat("m", 129) }, false)
	check("catalog-dash-first", func(i *Input) { i.ReasoningEffort = "-x" }, false)
	check("catalog-space", func(i *Input) { i.Model = "a b" }, false)
	check("prompt-max", func(i *Input) { i.Prompt = strings.Repeat("p", 24576) }, true)
	check("prompt-over", func(i *Input) { i.Prompt = strings.Repeat("p", 24577) }, false)
	check("prompt-nul", func(i *Input) { i.Prompt = "a\x00b" }, false)
	check("prompt-empty", func(i *Input) { i.Prompt = "" }, false)
	e := specimenEntry(t)
	e.Input.Prompt = strings.Repeat("p", 24576)
	_, digest, err := e.Input.Canonical()
	must(t, err)
	// Rebuild the request with the worker_plan of the maximal prompt.
	doc := parseDoc(t, fixture(t, "m0-executor-entry", "entry.specimen.json")).member("request")
	setPath(t, doc, "plan.worker_plan", text(digest))
	req, err := decodeUnsigned(doc)
	must(t, err)
	e.Request = req
	if _, _, err := e.Canonical(); err != nil {
		t.Errorf("INPUT: maximal prompt entry refused: %v", err)
	}
}
