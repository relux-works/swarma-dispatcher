package sweep

import (
	"errors"
	"os"
	"testing"
)

// SetSyncForTest installs a per-ledger crash point, never a production option.
func SetSyncForTest(q *Quarantine, f func(*os.File) error) { q.syncFile = f }

func privateQuarantine(t *testing.T, gate ReleaseGate, guard func(uint32, string) bool) *Quarantine {
	t.Helper()
	q := &Quarantine{}
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	if err := q.Configure(q, gate, guard); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(d, Fresh); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

// Claim: ReturnUnused removes only its own clean lease, never another holder's equally proved lease.
func TestUnusedReturnLeaseIdentity(t *testing.T) {
	q := privateQuarantine(t, ReleaseGate{}, nil)
	if err := q.Lease(7, "old"); err != nil {
		t.Fatal(err)
	}
	proof := Enumeration{UID: 7, qualified: true}
	if err := q.ReturnUnused(7, proof, "new"); err == nil || !q.Held(7) {
		t.Fatal("stale return removed another holder")
	}
	if err := q.ReturnUnused(7, proof, "old"); err != nil || q.Held(7) {
		t.Fatalf("own clean control: %v", err)
	}
}

// Claim: a conflicting non-terminal record prevents both sweep and release; without the conflict both proceed.
func TestRecordGuardProtectsEnd(t *testing.T) {
	conflict := true
	q := privateQuarantine(t, ReleaseGate{Operator: StaticOperatorRelease{"run": 7}}, func(uint32, string) bool { return conflict })
	if err := q.Hold(7, "run"); err != nil {
		t.Fatal(err)
	}
	proof := Enumeration{UID: 7, qualified: true}
	called := false
	q.WithLease(7, "run", func() Outcome { called = true; return Outcome{Final: proof} })
	if called {
		t.Fatal("swept despite conflicting record")
	}
	if _, err := q.Release("run", 7, false, proof); err == nil || !q.Held(7) {
		t.Fatal("released despite conflicting record")
	}
	conflict = false
	q.WithLease(7, "run", func() Outcome { called = true; return Outcome{Final: proof} })
	if !called {
		t.Fatal("owned control did not sweep")
	}
	if _, err := q.Release("run", 7, false, proof); err != nil {
		t.Fatal(err)
	}
}

// Claim: a durable helper and holder survive a fresh quarantine; stale returns cannot clear them but the current operator proof can.
func TestLedgerLeaseIdentity(t *testing.T) {
	q := privateQuarantine(t, ReleaseGate{}, nil)
	d := q.directory
	if err := q.Lease(7, "old"); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkHelper(7, "old"); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q = &Quarantine{}
	if err := q.Configure(q, ReleaseGate{Operator: StaticOperatorRelease{"old": 7, "new": 7}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(d, Established); err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	proof := Enumeration{UID: 7, qualified: true}
	if !q.HelperInvoked(7) || !q.Held(7) {
		t.Fatal("durable helper lost")
	}
	if err := q.ReturnUnused(7, proof, "new"); err == nil {
		t.Fatal("stale unused return removed hold")
	}
	if _, err := q.Release("new", 7, false, proof); err == nil || !q.Held(7) {
		t.Fatal("stale release removed hold")
	}
	if _, err := q.Release("old", 7, false, proof); err != nil || q.Held(7) {
		t.Fatalf("own operator: %v", err)
	}
	if err := q.Lease(7, "next"); err != nil || q.HelperInvoked(7) {
		t.Fatalf("next lease: %v", err)
	}
}

// Claim: an active sweep pins its lease and helper mark against release/re-lease, beside release after completion.
func TestSweepPinsLease(t *testing.T) {
	q := privateQuarantine(t, ReleaseGate{Operator: StaticOperatorRelease{"run": 7}}, nil)
	if err := q.Lease(7, "run"); err != nil {
		t.Fatal(err)
	}
	proof := Enumeration{UID: 7, qualified: true}
	out := q.WithLease(7, "run", func() Outcome {
		if err := q.MarkHelper(7, "run"); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Release("run", 7, false, proof); err == nil {
			t.Error("released active sweep")
		}
		if err := q.Lease(7, "next"); err == nil || !q.HelperInvoked(7) {
			t.Error("new lease erased sweep mark")
		}
		return Outcome{Final: proof}
	})
	if !out.Final.Proved() {
		t.Fatal("owned sweep did not run")
	}
	if _, err := q.Release("run", 7, false, proof); err != nil {
		t.Fatal(err)
	}
}

// Claim: unstarted operations and every empty, extra or foreign holder key refuse; the exact active key mutates durably.
func TestKeyedOperations(t *testing.T) {
	raw := &Quarantine{}
	if err := raw.Lease(7, "run"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("unstarted lease: %v", err)
	}
	q := privateQuarantine(t, ReleaseGate{Operator: StaticOperatorRelease{"run": 7}}, nil)
	proof := Enumeration{UID: 7, qualified: true}
	for _, keys := range [][]string{nil, {""}, {"run", "extra"}} {
		if err := q.Lease(7, keys...); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("bad lease key: %v", err)
		}
		if err := q.Hold(7, keys...); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("bad hold key: %v", err)
		}
	}
	if err := q.Lease(7, "run"); err != nil {
		t.Fatal(err)
	}
	for _, keys := range [][]string{nil, {""}, {"run", "extra"}, {"foreign"}} {
		if err := q.MarkHelper(7, keys...); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("bad helper key: %v", err)
		}
		if err := q.ReturnUnused(7, proof, keys...); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("bad return key: %v", err)
		}
	}
	if err := q.Hold(7, "foreign"); !errors.Is(err, ErrQuarantined) {
		t.Fatal("foreign Hold accepted")
	}
	if err := q.Note(7, "", "reason"); !errors.Is(err, ErrQuarantined) {
		t.Fatal("empty Note accepted")
	}
	if err := q.Transfer(7, "run", ""); !errors.Is(err, ErrQuarantined) {
		t.Fatal("empty Transfer accepted")
	}
	if err := q.ReturnUnused(7, proof, "run"); err != nil {
		t.Fatal(err)
	}
}
