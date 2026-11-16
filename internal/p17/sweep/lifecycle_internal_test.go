package sweep

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Claim: Start requires one explicit installation fact; a valid fresh fact starts, while no fact or an extra fact creates no ledger.
func TestInstallationFactRequired(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	q := &Quarantine{}
	for _, facts := range [][]Installation{nil, {0}, {Fresh, Established}} {
		if err := q.Start(d, facts...); !errors.Is(err, ErrInstallation) {
			t.Fatalf("fact accepted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(d, QuarantineName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid fact wrote state: %v", err)
		}
	}
	if err := q.Start(d, Fresh); err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.Lease(7, "control"); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(d, Established); !errors.Is(err, ErrInstallation) {
		t.Fatalf("changed lifetime fact: %v", err)
	}
}

// Claim: crashed ledger temporaries are never authoritative: an established real ledger wins, a fresh install ignores them and a missing established ledger is not recovered from one.
func TestTemporaryLedgerIgnored(t *testing.T) {
	for _, mode := range []string{"established", "fresh", "missing"} {
		t.Run(mode, func(t *testing.T) {
			d := t.TempDir()
			if err := os.Chmod(d, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, ".quarantine-crashed"), []byte(`{"version":99,"entries":{}}`), 0600); err != nil {
				t.Fatal(err)
			}
			q := &Quarantine{}
			if mode == "established" || mode == "missing" {
				if err := q.Start(d, Fresh); err != nil {
					t.Fatal(err)
				}
				if err := q.Lease(7, "current"); err != nil {
					t.Fatal(err)
				}
				if err := q.Close(); err != nil {
					t.Fatal(err)
				}
				if mode == "missing" {
					if err := os.Remove(filepath.Join(d, QuarantineName)); err != nil {
						t.Fatal(err)
					}
				}
				q = &Quarantine{}
			}
			fact := Established
			if mode == "fresh" {
				fact = Fresh
			}
			err := q.Start(d, fact)
			defer q.Close()
			if mode == "missing" {
				if !errors.Is(err, ErrQuarantined) || q.Lease(9, "next") == nil {
					t.Fatalf("temporary substituted for ledger: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "established" {
				if e, ok := q.Entry(7); !ok || e.Holder != "current" {
					t.Fatal("real ledger lost")
				}
			}
			if err := q.Lease(9, "control"); err != nil {
				t.Fatalf("ignored temporary control: %v", err)
			}
		})
	}
}

// Claim: Close cannot erase a prior fatal persistence error; an ordinarily closed quarantine remains nonfatal and both refuse subsequent leasing.
func TestClosePreservesFatal(t *testing.T) {
	for _, fatal := range []bool{true, false} {
		q := privateQuarantine(t, ReleaseGate{}, nil)
		if fatal {
			q.syncFile = func(*os.File) error { return errors.New("injected sync failure") }
			if err := q.Lease(7, "fault"); !errors.Is(err, ErrNotDurable) {
				t.Fatalf("not fatal: %v", err)
			}
		}
		if err := q.Close(); err != nil {
			t.Fatal(err)
		}
		if errors.Is(q.Error(), ErrNotDurable) != fatal || q.Lease(9, "next") == nil {
			t.Fatalf("closed error changed: %v", q.Error())
		}
	}
}

// Claim: every unstarted mutation refuses without a holder or helper side effect; the started exact-key control leases and returns.
func TestUnstartedOperationsRefuse(t *testing.T) {
	q := &Quarantine{}
	proof := Enumeration{UID: 7, qualified: true}
	for _, operation := range []func() error{
		func() error { return q.Lease(7, "run") }, func() error { return q.Hold(7, "run") }, func() error { return q.MarkHelper(7, "run") },
		func() error { return q.ReturnUnused(7, proof, "run") }, func() error { return q.Note(7, "run", "reason") }, func() error { return q.Transfer(7, "run", "next") },
		func() error { _, err := q.Release("run", 7, true, proof); return err },
	} {
		if err := operation(); !errors.Is(err, ErrNotStarted) {
			t.Fatalf("unstarted mutation: %v", err)
		}
	}
	called := false
	out := q.WithLease(7, "run", func() Outcome { called = true; return Outcome{Final: proof} })
	if called || out.Err == nil {
		t.Fatal("unstarted sweep ran")
	}
	if _, ok := q.Entry(7); ok || q.HelperInvoked(7) {
		t.Fatal("unstarted mutation changed holder")
	}
	q = privateQuarantine(t, ReleaseGate{}, nil)
	if err := q.Lease(7, "run"); err != nil {
		t.Fatal(err)
	}
	if err := q.ReturnUnused(7, proof, "run"); err != nil {
		t.Fatal(err)
	}
}
