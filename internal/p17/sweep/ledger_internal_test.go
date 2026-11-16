package sweep

import (
	"os"
	"testing"
)

// SetSyncForTest installs a per-ledger crash point, never a production option.
func SetSyncForTest(q *Quarantine, f func(*os.File) error) { q.syncFile = f }

// Claim: ReturnUnused removes only its own clean lease, never another holder's lease with an equally valid proof.
func TestUnusedReturnLeaseIdentity(t *testing.T) {
	q := &Quarantine{}
	if err := q.Lease(7, "old"); err != nil {
		t.Fatal(err)
	}
	proof := Enumeration{UID: 7, qualified: true}
	if err := q.ReturnUnused(7, proof, "new"); err == nil || !q.Held(7) {
		t.Fatal("stale clean return removed another holder")
	}
	if err := q.ReturnUnused(7, proof, "old"); err != nil || q.Held(7) {
		t.Fatalf("own clean control: %v", err)
	}
}

// Claim: a conflicting non-terminal record prevents both sweeping and releasing the uid; without that conflict both operations proceed.
func TestRecordGuardProtectsEnd(t *testing.T) {
	conflict := true
	q := &Quarantine{Gate: ReleaseGate{Operator: StaticOperatorRelease{"run": 7}}, Guard: func(uint32, string) bool { return conflict }}
	// Hold restores a run at startup even when another record conflicts.
	if err := q.Hold(7, "run"); err != nil {
		t.Fatal(err)
	}
	proof := Enumeration{UID: 7, qualified: true}
	called := false
	q.WithLease(7, "run", func() Outcome { called = true; return Outcome{Final: proof} })
	if called {
		t.Fatal("swept despite conflicting live record")
	}
	if _, err := q.Release("run", 7, false, proof); err == nil || !q.Held(7) {
		t.Fatal("released despite conflicting live record")
	}
	conflict = false
	q.WithLease(7, "run", func() Outcome { called = true; return Outcome{Final: proof} })
	if !called {
		t.Fatal("owned control did not sweep")
	}
	if _, err := q.Release("run", 7, false, proof); err != nil {
		t.Fatalf("uncontested control: %v", err)
	}
}

// Claim: durable helper/holder entries survive a fresh quarantine and stale holders cannot remove them; a matching operator proof can.
func TestLedgerLeaseIdentity(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	q := &Quarantine{}
	if err := q.Start(d); err != nil {
		t.Fatal(err)
	}
	if err := q.Lease(7, "old"); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkHelper(7, "old"); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q = &Quarantine{Gate: ReleaseGate{Operator: StaticOperatorRelease{"old": 7, "new": 7}}}
	if err := q.Start(d); err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	proof := Enumeration{UID: 7, qualified: true}
	if !q.HelperInvoked(7) || !q.Held(7) {
		t.Fatal("durable helper lost")
	}
	if err := q.ReturnUnused(7, proof, "new"); err == nil {
		t.Fatal("stale unused return removed old hold")
	}
	if _, err := q.Release("new", 7, false, proof); err == nil || !q.Held(7) {
		t.Fatal("stale release removed old hold")
	}
	if _, err := q.Release("old", 7, false, proof); err != nil || q.Held(7) {
		t.Fatalf("own operator release: %v", err)
	}
	if err := q.Lease(7, "next"); err != nil || q.HelperInvoked(7) {
		t.Fatalf("clean next lease: %v", err)
	}
}

// Claim: an in-progress sweep cannot lose its lease/helper mark to release or a new lease, beside release after completion.
func TestSweepPinsLease(t *testing.T) {
	q := &Quarantine{Gate: ReleaseGate{Operator: StaticOperatorRelease{"run": 7}}}
	if err := q.Lease(7, "run"); err != nil {
		t.Fatal(err)
	}
	proof := Enumeration{UID: 7, qualified: true}
	out := q.WithLease(7, "run", func() Outcome {
		if err := q.MarkHelper(7, "run"); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Release("run", 7, false, proof); err == nil {
			t.Error("released while sweep in progress")
		}
		if err := q.Lease(7, "next"); err == nil || !q.HelperInvoked(7) {
			t.Error("lease erased live sweep mark")
		}
		return Outcome{Final: proof}
	})
	if !out.Final.Proved() {
		t.Fatal("owned sweep did not run")
	}
	if _, err := q.Release("run", 7, false, proof); err != nil {
		t.Fatalf("completed sweep control: %v", err)
	}
}
