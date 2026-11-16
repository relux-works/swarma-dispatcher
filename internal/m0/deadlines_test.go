package m0

import "testing"

// Claim: the stop ceiling is the earliest of the M0 total, the detection
// component and every outstanding deadline, exactly as section 5 computes:
// removal 0 and detection 20 give 290 (not 300); completion at 290 meets it
// and 291 misses it; an outstanding P7 at 320 does not extend, at 280 wins;
// a shorter qualified bound 279 wins; a late detection (31 s) is itself unmet.
func TestStopCeilingArithmetic(t *testing.T) {
	if c := StopCeiling(0, 20, 30, 270); c != 290 {
		t.Fatalf("CEILING: %d want 290", c)
	}
	if !MeetsCeiling(290, 290) || MeetsCeiling(291, 290) {
		t.Fatal("CEILING: equality or 291 misjudged")
	}
	if c := StopCeiling(0, 20, 30, 270, 320); c != 290 {
		t.Fatalf("CEILING: later P7 extended to %d", c)
	}
	if c := StopCeiling(0, 20, 30, 270, 280); c != 280 {
		t.Fatalf("CEILING: earlier P7 ignored: %d", c)
	}
	if c := StopCeiling(0, 20, 30, 270, 320, 279); c != 279 {
		t.Fatalf("CEILING: shorter qualified bound ignored: %d", c)
	}
	if c := StopCeiling(0, 20, 30, 270, 0); c != 290 {
		t.Fatalf("CEILING: zero outstanding entry changed the result: %d", c)
	}
	if !DetectionTimely(0, 30, 30) || DetectionTimely(0, 31, 30) || DetectionTimely(10, 5, 30) {
		t.Fatal("CEILING: detection bound misjudged")
	}
}

// Claim: C is permitted only strictly before expiry at max(now, watermark):
// equality refuses, a watermark ahead of a stepped-back clock refuses, and a
// later not_before is checked at min(now, watermark). Original deadlines take
// the earlier of not_after and reservation plus max_runtime, and refuse when
// nothing remains.
func TestStartPermittedAndWindow(t *testing.T) {
	if !startPermitted(99, 0, 100) || startPermitted(100, 0, 100) || startPermitted(50, 100, 100) || startPermitted(101, 0, 100) {
		t.Fatal("START: expiry rule misjudged")
	}
	p := &Policy{NotBefore: 1000, NotAfter: 2000, Bounds: Bounds{MaxRuntimeSeconds: 600}}
	if !windowOpen(p, 1000, 0) || windowOpen(p, 999, 0) || windowOpen(p, 2000, 0) || windowOpen(p, 1500, 2000) || !windowOpen(p, 1500, 1500) {
		t.Fatal("WINDOW: not_before/not_after rule misjudged")
	}
	q := Qualified{SlackMs: 10000}
	d, err := originalDeadlines(p, 1000, q)
	must(t, err)
	if d.Expiry != 1600 || d.DeadlineMs != 1600000 || d.SlackMs != 10000 {
		t.Fatalf("DEADLINES: %+v", d)
	}
	d, err = originalDeadlines(p, 1900, q)
	must(t, err)
	if d.Expiry != 2000 {
		t.Fatalf("DEADLINES: expiry not cut by not_after: %+v", d)
	}
	if _, err := originalDeadlines(p, 2000, q); CodeOf(err) != CodePolicyWindow {
		t.Fatalf("DEADLINES: no remaining runtime accepted: %v", err)
	}
}
