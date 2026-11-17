//go:build linux && (amd64 || arm64)

package compose

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

func put(t *testing.T, dir, name, value string) {
	t.Helper()
	must(t, os.WriteFile(filepath.Join(dir, name), []byte(value), 0600))
}
func get(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	must(t, err)
	return string(b)
}

// Claim: a stop at every coordinator persistence step before Begin, including both sides of the ledger flip, resumes without operator action; interrupted quarantine temporaries never authorize state.
func TestFirstStartInterruptions(t *testing.T) {
	control := newFixture(t)
	var observed []string
	control.deps.step = func(step string) error { observed = append(observed, step); return nil }
	session := control.open()
	must(t, session.Close())
	steps := []string{".ledger-install:temp", ".ledger-install:file-sync", ".ledger-install:rename", ".ledger-install:directory-sync", ".record-install:temp", ".record-install:file-sync", ".record-install:rename", ".record-install:directory-sync", "quarantine-started", ".ledger-install:temp", ".ledger-install:file-sync", ".ledger-install:rename", ".ledger-install:directory-sync"}
	if !reflect.DeepEqual(observed, steps) {
		t.Fatalf("uncovered coordinator step: %v", observed)
	}
	for n := 1; n <= len(steps); n++ {
		t.Run(fmt.Sprintf("step-%02d", n), func(t *testing.T) {
			f := newFixture(t)
			calls := 0
			f.deps.step = func(string) error {
				calls++
				if calls == n {
					return errFixture
				}
				return nil
			}
			if s, err := f.attach(); s != nil || err == nil {
				t.Fatal("injected stop did not stop startup")
			}
			if f.events.calls != 0 || f.inner.starts != 0 {
				t.Fatal("Begin was reachable before establishment")
			}
			// An actual process loss can leave either client's non-authoritative temp.
			put(t, f.state, ".install-interrupted", "partial")
			put(t, f.state, ".quarantine-interrupted", "partial")
			f.deps.step = nil
			s := f.open()
			if get(t, f.state, ledgerMarker) != "established\n" || get(t, f.state, recordMarker) != "initialize\n" || !s.store.Pending() {
				t.Fatal("restart inferred or prematurely flipped a fact")
			}
		})
	}
	for _, name := range []string{"before-start", "quarantine-temp"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if name == "quarantine-temp" {
				put(t, f.state, ledgerMarker, "starting\n")
				put(t, f.state, recordMarker, "initialize\n")
				put(t, f.state, "launcher.lock", "")
				put(t, f.state, ".quarantine-interrupted", "partial")
			}
			f.open()
		})
	}
}

// Claim: missing/incorrect independent markers never infer fresh over an existing ledger, journal or record, while the empty protected installation beside each refusal boots successfully.
func TestInstallationNoInference(t *testing.T) {
	for _, name := range []string{"ledger-without-marker", "nonzero-ledger-under-intent", "journal-without-marker", "record-without-marker", "journal-under-initialize", "established-without-record-fact"} {
		t.Run(name, func(t *testing.T) {
			control := newFixture(t)
			control.open()
			f := newFixture(t)
			switch name {
			case "ledger-without-marker":
				put(t, f.state, "quarantine.json", `{"version":1,"entries":{}}`)
			case "nonzero-ledger-under-intent":
				put(t, f.state, ledgerMarker, "starting\n")
				put(t, f.state, recordMarker, "initialize\n")
				put(t, f.state, "quarantine.json", `{"version":1,"entries":{"30002":{"uid":30002,"holder":"fixture-hold","reason":"leased","helper":false}}}`)
				q := &sweep.Quarantine{}
				must(t, q.Start(f.state, sweep.Established))
				must(t, q.Close())
			case "journal-without-marker":
				put(t, f.state, "journal", "prior journal")
			case "record-without-marker":
				put(t, filepath.Join(f.state, "records"), "run-orphan", "prior record")
			case "journal-under-initialize":
				put(t, f.state, ledgerMarker, "starting\n")
				put(t, f.state, recordMarker, "initialize\n")
				put(t, f.state, "journal", "prior journal")
			case "established-without-record-fact":
				s := f.open()
				must(t, s.Close())
				must(t, os.Remove(filepath.Join(f.state, recordMarker)))
			}
			if s, err := f.attach(); s != nil || !errors.Is(err, ErrInstallation) {
				t.Fatalf("prior state admitted or wrong boundary: %v", err)
			}
		})
	}
}

// Claim: deleting an established ledger fails startup and blocks Hold for every managed UID, rather than treating missing state as fresh; the intact established installation restarts beside it.
func TestEstablishedLedgerMissing(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	must(t, s.Close())
	again := f.open()
	must(t, again.Close())
	must(t, os.Remove(filepath.Join(f.state, "quarantine.json")))
	if s, err := f.attach(); s != nil || !errors.Is(err, sweep.ErrQuarantined) {
		t.Fatalf("missing established ledger: %v", err)
	}
	q := &sweep.Quarantine{}
	defer q.Close()
	if err := q.Start(f.state, sweep.Established); !errors.Is(err, sweep.ErrQuarantined) {
		t.Fatalf("missing ledger not pool-wide: %v", err)
	}
	for uid := uint32(30000); uid <= 59999; uid++ {
		if !q.Held(uid) || q.Hold(uid, "fixture-hold") == nil {
			t.Fatalf("missing ledger opened UID %d", uid)
		}
	}
}

// Claim: ledger flip file/directory fsync failures take the fatal exit path before store construction or any Begin; a subsequent fresh process can recover the durable intent without operator edits.
func TestLedgerFlipFailure(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprintf("directory-%t", directory), func(t *testing.T) {
			control := newFixture(t)
			control.open()
			f := newFixture(t)
			flip := false
			var trace []string
			f.deps.trace = func(step string) { trace = append(trace, step) }
			f.deps.step = func(step string) error {
				if step == "quarantine-started" {
					flip = true
				}
				return nil
			}
			f.deps.syncFile = func(file *os.File) error {
				st, err := file.Stat()
				if err != nil {
					return err
				}
				if flip && st.IsDir() == directory {
					return syscall.EIO
				}
				return file.Sync()
			}
			if s, err := f.attach(); s != nil || !errors.Is(err, transport.ErrFatal) || !errors.Is(err, syscall.EIO) {
				t.Fatalf("flip was not process-fatal: %v", err)
			}
			for _, step := range trace {
				if step == "records" || step == "launcher" || step == "hooks" || step == "attach" {
					t.Fatal("flip failure exposed Begin")
				}
			}
			if f.events.calls != 0 || f.inner.starts != 0 {
				t.Fatal("failed flip began a run")
			}
			f.deps.step = nil
			f.deps.syncFile = nil
			f.open()
		})
	}
}

// Claim: the independent journal fact flips only after the first durable Create, never on boot; flip fsync failure latches fatal before binding/dispatch and prevents subsequent record mutations.
func TestRecordFactFlip(t *testing.T) {
	control := newFixture(t)
	s := control.open()
	if get(t, control.state, recordMarker) != "initialize\n" || !s.store.Pending() {
		t.Fatal("boot flipped the journal fact")
	}
	must(t, s.store.Create(control.record()))
	if get(t, control.state, recordMarker) != "established\n" || s.store.Pending() || !bytes.Equal(s.ConfigBytes(), control.config) {
		t.Fatal("first Create did not flip only its independent fact")
	}
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprintf("directory-%t", directory), func(t *testing.T) {
			f := newFixture(t)
			session := f.open()
			session.store.install.syncFile = func(file *os.File) error {
				st, err := file.Stat()
				if err != nil {
					return err
				}
				if st.IsDir() == directory {
					return syscall.EIO
				}
				return file.Sync()
			}
			err := session.store.Create(f.record())
			if !errors.Is(err, sweep.ErrNotDurable) || !errors.Is(err, syscall.EIO) || session.ExitCause() == nil {
				t.Fatalf("record flip failure did not latch: %v", err)
			}
			before := get(t, f.state, "journal")
			called := false
			if _, err := session.store.Update("fixture-record", func(*runrecord.Record) error { called = true; return nil }); !errors.Is(err, sweep.ErrNotDurable) || called {
				t.Fatal("latched store allowed update")
			}
			if err := session.store.Create(f.record()); !errors.Is(err, sweep.ErrNotDurable) {
				t.Fatal("latched store allowed Create")
			}
			body, err := session.Serve(30001, "after-flip", f.frame)
			if body != "" || !errors.Is(err, transport.ErrFatal) || f.events.calls != 0 || f.inner.starts != 0 {
				t.Fatalf("failed record flip did not stop admission: %v", err)
			}
			if get(t, f.state, "journal") != before {
				t.Fatal("latched record store performed more state I/O")
			}
		})
	}
}
