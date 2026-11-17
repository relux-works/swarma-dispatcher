//go:build linux

package m0

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

func journalFixture(t *testing.T) journal.Config {
	t.Helper()
	d := t.TempDir()
	must(t, os.Chmod(d, 0700))
	for _, n := range []string{"records", "inputs"} {
		must(t, os.Mkdir(filepath.Join(d, n), 0700))
		must(t, os.Chmod(filepath.Join(d, n), 0700))
	}
	var fs syscall.Statfs_t
	must(t, syscall.Statfs(d, &fs))
	if fs.Type != 0xEF53 && fs.Type != 0x58465342 {
		t.Skipf("real storage UNRUN: fixture filesystem type %#x is neither ext4 nor XFS", fs.Type)
	}
	current := syscall.Umask(0)
	syscall.Umask(current)
	m := uint32(current)
	if m&0600 != 0 {
		t.Skipf("process umask %#o masks owner read/write", m)
	}
	return journal.Config{Directory: d, Records: "records", Inputs: "inputs", DeclaredUmask: &m, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 4 << 20}
}

func journalBytes(t *testing.T, cfg journal.Config) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg.Directory, "journal"))
	must(t, err)
	return b
}

// Claim (real ext4/XFS journal): Initialize commits the open frame as the
// genuine first transition; a started dispatch survives Close and established
// recovery, whose replay issues no second start; recovery without the
// attestation refuses m0_recovery_held; committed history is never rewritten
// (the recovered journal extends the old bytes); and the cached projection
// record is materialized.
func TestRealJournalLifecycle(t *testing.T) {
	cfg := journalFixture(t)
	e := newEnv(t)
	rt, err := Initialize(cfg, e.seams())
	must(t, err)
	e.rt = rt
	res, err := e.dispatch(t)
	must(t, err)
	if res.State != string(phaseStarted) {
		t.Fatalf("REAL: %+v", res)
	}
	must(t, rt.Close())
	before := journalBytes(t, cfg)
	held := cfg
	held.RecoveryPrerequisiteEstablished = false
	if _, err := OpenEstablished(held, e.seams()); CodeOf(err) != CodeRecoveryHeld {
		t.Fatalf("REAL: recovery without attestation: %v", err)
	}
	if !bytes.Equal(before, journalBytes(t, cfg)) {
		t.Fatal("REAL: refused recovery touched the journal")
	}
	rt2, err := OpenEstablished(cfg, e.seams())
	must(t, err)
	e.rt = rt2
	if rec := e.record(t, dispatchID); rec.Phase != phaseStarted {
		t.Fatalf("REAL: recovered %+v", rec)
	}
	again, err := e.dispatch(t)
	must(t, err)
	if !again.Replayed || again.State != string(phaseStarted) {
		t.Fatalf("REAL: replay %+v", again)
	}
	if starts, _ := e.launcher.count(); starts != 1 {
		t.Fatalf("REAL: starts=%d", starts)
	}
	must(t, rt2.Close())
	after := journalBytes(t, cfg)
	if len(after) <= len(before) || !bytes.Equal(before, after[:len(before)]) {
		t.Fatal("REAL: recovery rewrote committed history")
	}
	if _, err := os.Stat(filepath.Join(cfg.Directory, "records", projectionRecord)); err != nil {
		t.Fatalf("REAL: projection record missing: %v", err)
	}
}

// Claim (real journal, process lost after durable intent): the next session
// recovers the record as in-flight UNKNOWN, the same-ID replay returns
// observation_unknown with no second start, and the launch intent frame is
// the last committed transition.
func TestRealJournalCrashAfterIntent(t *testing.T) {
	cfg := journalFixture(t)
	e := newEnv(t)
	rt, err := Initialize(cfg, e.seams())
	must(t, err)
	e.rt = rt
	e.launcher.onStart = func() { must(t, rt.Close()) }
	_, err = e.dispatch(t)
	wantCode(t, err, CodeStorageFailed)
	e.launcher.onStart = nil
	rt2, err := OpenEstablished(cfg, e.seams())
	must(t, err)
	defer rt2.Close()
	e.rt = rt2
	if rec := e.record(t, dispatchID); rec.Phase != phaseInFlight || rec.EffectID == "" {
		t.Fatalf("CRASH-REAL: %+v", rec)
	}
	res, err := e.dispatch(t)
	wantCode(t, err, CodeObservationUnknown)
	if !res.Replayed {
		t.Fatal("CRASH-REAL: replay restarted")
	}
	if starts, _ := e.launcher.count(); starts != 1 {
		t.Fatalf("CRASH-REAL: starts=%d", starts)
	}
}

// Claim: Attach opens the ledger only when the configuration enables M0 and
// the policy source reports an installed policy; the returned extension serves
// one dispatch over the real journal, and a second Attach with the
// "established" entry recovers it. With default seams the same configuration
// is m0_closed and no state file is created.
func TestAttachOpensLedger(t *testing.T) {
	cfg := journalFixture(t)
	// Attach sets the daemon umask; restore the test process umask afterwards.
	previous := syscall.Umask(0)
	syscall.Umask(previous)
	t.Cleanup(func() { syscall.Umask(previous) })
	e := newEnv(t)
	doc := `{"callers":[{"dev":1,"dir":"/example/caller","ino":2,"uid":30001}],"enabled":true,"entry":"initialize","kind":"swarma-dispatcher m0-config/3","p17":` + fixtureP17 + `,"recovery_attested":true,"state_dir":"` + cfg.Directory + `","supported_local_storage":true,"v":3}`
	read := func(string) ([]byte, error) { return []byte(doc), nil }
	if _, err := Attach("/m0.json", DefaultSeams(), read); CodeOf(err) != CodeClosed {
		t.Fatalf("ATTACH: default seams %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Directory, "journal")); !os.IsNotExist(err) {
		t.Fatal("ATTACH: closed gate created state")
	}
	ext, err := Attach("/m0.json", e.seams(), read)
	must(t, err)
	callers := ext.ManagedCallers()
	if len(callers) != 1 || callers[0].UID != 30001 {
		t.Fatal("ATTACH: protected callers not exposed")
	}
	callers[0].UID++
	if ext.ManagedCallers()[0].UID != 30001 {
		t.Fatal("ATTACH: caller snapshot is mutable")
	}
	reply, err := ext.Serve(e.peer, dispatchID, e.dispatchWire)
	must(t, err)
	if !strings.Contains(reply, `"state":"started"`) {
		t.Fatalf("ATTACH: %s", reply)
	}
	must(t, ext.Close())
	established := strings.Replace(doc, `"entry":"initialize"`, `"entry":"established"`, 1)
	ext2, err := Attach("/m0.json", e.seams(), func(string) ([]byte, error) { return []byte(established), nil })
	must(t, err)
	defer ext2.Close()
	reply, err = ext2.Serve(e.peer, dispatchID, e.dispatchWire)
	must(t, err)
	if !strings.Contains(reply, `"state":"started"`) {
		t.Fatalf("ATTACH: recovered %s", reply)
	}
	if starts, _ := e.launcher.count(); starts != 1 {
		t.Fatalf("ATTACH: starts=%d", starts)
	}
}

// Claim (real journal, stop continuation): a healthy stop completes (control).
// After a transient receiver failure the stop outcome is unknown and the
// original obligation is retained; once the receiver is healthy the SAME stop
// (effect, event, detection) is continued to absence from a same-ID cancel
// retry, from Sweep plus Reconcile, and after a journal close and
// established reopen plus Sweep and Reconcile, each with exactly one start and
// two stops. A receiver that keeps failing leaves the run started with the
// stop unknown and no state claims absence. After absence nothing is
// re-issued.
func TestStopRecoveryAfterTransientFailure(t *testing.T) {
	for _, mode := range []string{"healthy-control", "retry", "sweep", "reopen-sweep", "still-failing"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			cfg := journalFixture(t)
			rt, err := Initialize(cfg, e.seams())
			must(t, err)
			e.rt = rt
			t.Cleanup(func() { _ = e.rt.Close() })
			_, err = e.dispatch(t)
			must(t, err)
			if mode == "healthy-control" {
				res, err := e.cancel(t)
				must(t, err)
				if res.State != stopAbsent {
					t.Fatalf("RECOVERY: healthy stop %+v", res)
				}
				return
			}
			e.launcher.stopErr = errFixture
			_, err = e.cancel(t)
			wantCode(t, err, CodeObservationUnknown)
			original := e.record(t, dispatchID)
			if original.StopEffectID == "" || original.StopOutcome != stopUnknown || original.Phase != phaseStarted {
				t.Fatalf("RECOVERY: retained stop missing: %+v", original)
			}
			if mode != "still-failing" {
				e.launcher.mu.Lock()
				e.launcher.stopErr = nil
				e.launcher.mu.Unlock()
			}
			if mode == "reopen-sweep" {
				must(t, e.rt.Close())
				e.rt, err = OpenEstablished(cfg, e.seams())
				must(t, err)
			}
			if mode == "retry" {
				_, _ = e.cancel(t)
			} else {
				must(t, e.rt.Sweep(context.Background()))
				must(t, e.rt.Reconcile(context.Background()))
			}
			recovered := e.record(t, dispatchID)
			starts, stops := e.launcher.count()
			if starts != 1 || recovered.StopEffectID != original.StopEffectID || recovered.StopEvent != original.StopEvent || recovered.StopDetection != original.StopDetection {
				t.Fatalf("RECOVERY: original obligation changed: starts=%d old=%+v new=%+v", starts, original, recovered)
			}
			if stops != 2 || recovered.StopAttempts != 2 || recovered.StopObserved == "" {
				t.Fatalf("RECOVERY: stop not continued once: stops=%d %+v", stops, recovered)
			}
			if mode == "still-failing" {
				if recovered.StopOutcome != stopUnknown || recovered.Phase != phaseStarted || len(recovered.StopEvidence) != 0 {
					t.Fatalf("RECOVERY: absence inferred from a failed stop: %+v", recovered)
				}
				return
			}
			if recovered.StopOutcome != stopAbsent || recovered.Phase != phaseAbsent {
				t.Fatalf("RECOVERY: recovered receiver never revisited: %+v", recovered)
			}
			must(t, e.rt.Sweep(context.Background()))
			must(t, e.rt.Reconcile(context.Background()))
			if _, stops := e.launcher.count(); stops != 2 {
				t.Fatalf("RECOVERY: terminal stop re-issued: stops=%d", stops)
			}
		})
	}
}

// Claim (real journal, ceiling survives reopen): a cancel arriving after the
// original expiry with a failing receiver records the expiry and cancel
// obligations; after a journal close and established reopen the continued
// stop carries the same effect and the same earliest ceiling (expiry-based),
// never a later one, and reaches absence with one start and two stops.
func TestOriginalExpiryCeilingSurvivesReopen(t *testing.T) {
	e := newEnv(t)
	cfg := journalFixture(t)
	rt, err := Initialize(cfg, e.seams())
	must(t, err)
	e.rt = rt
	t.Cleanup(func() { _ = e.rt.Close() })
	_, err = e.dispatch(t)
	must(t, err)
	original := e.record(t, dispatchID)
	detection := original.Deadlines.Expiry + original.Deadlines.DetectSeconds + 10
	e.clock.set(detection)
	e.launcher.stopErr = errFixture
	_, err = e.cancel(t)
	wantCode(t, err, CodeObservationUnknown)
	first := e.launcher.lastStop
	want := StopCeiling(original.Deadlines.Expiry, detection, original.Deadlines.DetectSeconds, original.Deadlines.StopSeconds)
	if first.Ceiling != want {
		t.Fatalf("CEILING-REOPEN: first stop ceiling %d want %d", first.Ceiling, want)
	}
	must(t, e.rt.Close())
	e.rt, err = OpenEstablished(cfg, e.seams())
	must(t, err)
	rec := e.record(t, dispatchID)
	if !rec.hasObligation(sourceExpiry) || !rec.hasObligation(sourceCancel) || rec.StopOutcome != stopUnknown {
		t.Fatalf("CEILING-REOPEN: obligations lost across reopen: %+v", rec)
	}
	e.launcher.mu.Lock()
	e.launcher.stopErr = nil
	e.launcher.mu.Unlock()
	must(t, e.rt.Sweep(context.Background()))
	retried := e.launcher.lastStop
	rec = e.record(t, dispatchID)
	starts, stops := e.launcher.count()
	if retried.EffectID != first.EffectID || retried.Ceiling != want || retried.Ceiling > first.Ceiling || rec.Phase != phaseAbsent || starts != 1 || stops != 2 {
		t.Fatalf("CEILING-REOPEN: first=%+v retried=%+v rec=%+v starts=%d stops=%d", first, retried, rec, starts, stops)
	}
}

// recordRemoval marks the installed policy removed and lets a fresh signed
// dispatch detect and durably record it (refused m0_policy_removed).
func recordRemoval(t *testing.T, e *testEnv) {
	t.Helper()
	e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
	probe := parseDoc(t, e.dispatchDoc.encode(nil))
	setPath(t, probe, "request_id", text(strings.Repeat("5", 32)))
	_, err := e.rt.Handle(context.Background(), e.peer, signRequest(t, probe, e.key))
	wantCode(t, err, CodePolicyRemoved)
}

// Claim (real journal, recorded removal carried into every stop entry): once
// a removal is durably detected, the removal obligation with its original
// event and detection is on the started run; a delayed Sweep (control) and a
// delayed owner cancel forty seconds later, with and without a journal close
// and established reopen, hand the receiver a ceiling no later than
// removal + detect + stop, with exactly one start and one stop.
func TestRecordedRemovalCeiling(t *testing.T) {
	for _, mode := range []string{"sweep-control", "cancel", "cancel-reopen"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			cfg := journalFixture(t)
			rt, err := Initialize(cfg, e.seams())
			must(t, err)
			e.rt = rt
			t.Cleanup(func() { _ = e.rt.Close() })
			_, err = e.dispatch(t)
			must(t, err)
			original := e.record(t, dispatchID)
			removalEvent := original.Deadlines.ReservedAt + 1
			e.clock.set(removalEvent)
			recordRemoval(t, e)
			if mode == "cancel-reopen" {
				must(t, e.rt.Close())
				e.rt, err = OpenEstablished(cfg, e.seams())
				must(t, err)
			}
			rec := e.record(t, dispatchID)
			if !rec.hasObligation(sourceRemoval) || rec.StopObligations[0] != (StopObligation{Source: sourceRemoval, Event: removalEvent, Detection: removalEvent}) {
				t.Fatalf("REMOVAL-CEILING: removal obligation not carried: %+v", rec.StopObligations)
			}
			detection := removalEvent + original.Deadlines.DetectSeconds + 10
			e.clock.set(detection)
			if mode == "sweep-control" {
				must(t, e.rt.Sweep(context.Background()))
			} else {
				_, err = e.cancel(t)
				must(t, err)
			}
			stop := e.launcher.lastStop
			latest := removalEvent + original.Deadlines.DetectSeconds + original.Deadlines.StopSeconds
			if starts, stops := e.launcher.count(); starts != 1 || stops != 1 {
				t.Fatalf("REMOVAL-CEILING: starts=%d stops=%d", starts, stops)
			}
			if stop.Ceiling == 0 || stop.Ceiling > latest {
				t.Fatalf("REMOVAL-CEILING: ceiling=%d exceeds removal total %d; obligations=%+v", stop.Ceiling, latest, stop.Obligations)
			}
		})
	}
}

// Claim (real journal, removal detection survives delay): a removal detected
// through Handle keeps its original detection; an immediate Sweep (control), a
// Sweep delayed past the detect bound, and a delayed Sweep after a journal
// close and established reopen all hand the receiver the original
// detection + stop component, never a ceiling restarted at the sweep time.
func TestRemovalDetectionSurvivesDelay(t *testing.T) {
	for _, mode := range []string{"immediate-control", "delayed-sweep", "delayed-reopen-sweep"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			cfg := journalFixture(t)
			rt, err := Initialize(cfg, e.seams())
			must(t, err)
			e.rt = rt
			t.Cleanup(func() { _ = e.rt.Close() })
			_, err = e.dispatch(t)
			must(t, err)
			original := e.record(t, dispatchID)
			detectedAt := original.Deadlines.ReservedAt + 1
			e.clock.set(detectedAt)
			recordRemoval(t, e)
			if mode == "delayed-reopen-sweep" {
				must(t, e.rt.Close())
				e.rt, err = OpenEstablished(cfg, e.seams())
				must(t, err)
			}
			if mode != "immediate-control" {
				e.clock.set(detectedAt + original.Deadlines.DetectSeconds + 10)
			}
			must(t, e.rt.Sweep(context.Background()))
			stop := e.launcher.lastStop
			if want := detectedAt + original.Deadlines.StopSeconds; stop.Ceiling != want {
				t.Fatalf("REMOVAL-DETECTION: ceiling=%d want original detection component %d; obligations=%+v", stop.Ceiling, want, stop.Obligations)
			}
			if rec := e.record(t, dispatchID); rec.StopDetection != detectedAt || rec.StopSource != sourceRemoval {
				t.Fatalf("REMOVAL-DETECTION: provenance re-timed: %+v", rec)
			}
		})
	}
}

// Claim (real journal, unknown stop after refused start): a cancel that wins
// before C with a transiently failing receiver leaves a refused start with an
// unknown stop; once the receiver is healthy, Sweep (with Reconcile around it)
// continues the SAME stop effect with the same ceiling to absence, with and
// without a journal close and established reopen, one start and two stops. The
// same-ID cancel retry is the control. No launch is started and absence comes
// only from the stop receipt.
func TestUnknownStopAfterRefusedStart(t *testing.T) {
	for _, mode := range []string{"retry-control", "sweep", "reopen-sweep"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			cfg := journalFixture(t)
			rt, err := Initialize(cfg, e.seams())
			must(t, err)
			e.rt = rt
			t.Cleanup(func() { _ = e.rt.Close() })
			e.launcher.stopErr = errFixture
			e.launcher.onStart = func() {
				_, cancelErr := e.cancel(t)
				wantCode(t, cancelErr, CodeObservationUnknown)
			}
			_, err = e.dispatch(t)
			wantCode(t, err, CodeStartInvalidated)
			original := e.record(t, dispatchID)
			if original.Phase != phaseRefused || original.StopOutcome != stopUnknown {
				t.Fatalf("REFUSED-STOP: ordering not reached: %+v", original)
			}
			first := e.launcher.lastStop
			e.launcher.mu.Lock()
			e.launcher.stopErr = nil
			e.launcher.onStart = nil
			e.launcher.mu.Unlock()
			if mode == "reopen-sweep" {
				must(t, e.rt.Close())
				e.rt, err = OpenEstablished(cfg, e.seams())
				must(t, err)
			}
			if mode == "retry-control" {
				_, err = e.cancel(t)
				must(t, err)
			} else {
				must(t, e.rt.Reconcile(context.Background()))
				must(t, e.rt.Sweep(context.Background()))
				must(t, e.rt.Reconcile(context.Background()))
			}
			recovered := e.record(t, dispatchID)
			starts, stops := e.launcher.count()
			last := e.launcher.lastStop
			if starts != 1 || last.EffectID != first.EffectID || last.Ceiling != first.Ceiling || last.Event != first.Event || last.Detection != first.Detection {
				t.Fatalf("REFUSED-STOP: original effect changed: starts=%d first=%+v last=%+v", starts, first, last)
			}
			if recovered.StopOutcome != stopAbsent || recovered.Phase != phaseAbsent || stops != 2 || len(recovered.StopEvidence) == 0 {
				t.Fatalf("REFUSED-STOP: healthy receiver not revisited: %+v stops=%d", recovered, stops)
			}
		})
	}
}
