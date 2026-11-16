//go:build linux

package m0

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

// Stop table rows whose event is a reopen, and every path that depends on
// real storage, run here over the real journal (ext4/XFS fixture) with
// established recovery. The in-memory rows are in stop_table_test.go.

// realEnv opens a fresh real-journal runtime for the synthetic installation
// and traces its receiver calls.
func realEnv(t *testing.T) (*testEnv, journal.Config, *tableLauncher) {
	t.Helper()
	e := newEnv(t)
	cfg := journalFixture(t)
	rt, err := Initialize(cfg, e.seams())
	must(t, err)
	e.rt = rt
	t.Cleanup(func() { _ = e.rt.Close() })
	return e, cfg, traceLauncher(e)
}

// reopenReal closes the session and recovers it through OpenEstablished; the
// tracing launcher keeps its history across the reopen.
func reopenReal(t *testing.T, e *testEnv, cfg journal.Config, l *tableLauncher) {
	t.Helper()
	_ = e.rt.Close()
	rt, err := OpenEstablished(cfg, e.seams())
	must(t, err)
	e.rt = rt
	e.rt.seams.Launcher = l
}

func realRecord(t *testing.T, cfg journal.Config) *projection {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg.Directory, "records", projectionRecord))
	return cachedProjection(t, b, err == nil)
}

// dieAtStop makes the next Stop end the session before its outcome can be
// journaled: the receiver was called and the reply is lost to the ledger.
func dieAtStop(t *testing.T, e *testEnv, l *tableLauncher) {
	t.Helper()
	rt := e.rt
	l.atStop = func(StopRequest) {
		l.mu.Lock()
		l.atStop = nil
		l.mu.Unlock()
		must(t, rt.Close())
	}
}

// Row 2, reopen event (m0-launch.md:324): a reservation that never reached
// intent is recovered as such and expiry, removal, Sweep and Reconcile after
// the reopen still create no stop state and call nothing. The recovered
// reservation that is dispatched again is the control: it continues to one
// start (the original transaction, not a second one).
func TestStopTableRow02ReservationSurvivesReopenInert(t *testing.T) {
	for _, mode := range []string{"inert", "continue-control"} {
		t.Run(mode, func(t *testing.T) {
			e, cfg, l := realEnv(t)
			e.rt.store = &typedFailLedger{ledger: e.rt.store, fail: frameIntent}
			_, err := e.dispatch(t)
			wantCode(t, err, CodeStorageFailed)
			reopenReal(t, e, cfg, l)
			rec := e.record(t, dispatchID)
			if rec.Phase != phaseReserved || rec.EffectID != "" {
				t.Fatalf("ROW2 reopen: %+v", rec)
			}
			if mode == "continue-control" {
				res, err := e.dispatch(t)
				must(t, err)
				if res.State != string(phaseStarted) || res.Replayed {
					t.Fatalf("ROW2 control: %+v", res)
				}
				wantSequence(t, l, "start")
				return
			}
			e.clock.set(rec.Deadlines.Expiry + 1)
			must(t, e.rt.Sweep(context.Background()))
			e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
			must(t, e.rt.Sweep(context.Background()))
			must(t, e.rt.Reconcile(context.Background()))
			after := e.record(t, dispatchID)
			if after.Phase != phaseReserved || after.StopEffectID != "" || len(after.StopObligations) != 0 {
				t.Fatalf("ROW2 reopen: stop state on a reservation: %+v", after)
			}
			wantSequence(t, l)
		})
	}
}

// Row 1a (m0-launch.md:324 a reservation without intent has no effect;
// journal.md:169-174 a committed frame stays committed): the cancel-only
// journal, the exact bytes an interrupted older process leaves after its
// cancel frame, replays through established recovery to state X: durable
// phase cancelled, outcome no_effect; the same-ID cancel replay and the
// dispatch replay answer cancelled, a new cancel ID is refused run_terminal,
// and Sweep and Reconcile call the receiver zero times. The uninterrupted
// cancel without a reopen is the control and yields the identical record.
func TestStopTableRow01aCancelOnlyJournalRecoversTerminal(t *testing.T) {
	for _, mode := range []string{"uninterrupted-control", "cancel-only-reopen"} {
		t.Run(mode, func(t *testing.T) {
			e, cfg, l := realEnv(t)
			e.rt.store = &typedFailLedger{ledger: e.rt.store, fail: frameIntent}
			_, err := e.dispatch(t)
			wantCode(t, err, CodeStorageFailed)
			reopenReal(t, e, cfg, l)
			if rec := e.record(t, dispatchID); rec.Phase != phaseReserved || rec.EffectID != "" {
				t.Fatalf("ROW1a fixture: %+v", rec)
			}
			at := e.record(t, dispatchID).Deadlines.ReservedAt + 1
			e.clock.set(at)
			res, err := e.cancel(t)
			must(t, err)
			if res.State != string(phaseCancelled) || res.Replayed {
				t.Fatalf("ROW1a: first cancel %+v", res)
			}
			control := e.record(t, dispatchID)
			if mode == "cancel-only-reopen" {
				// The journal now ends with the cancel frame and nothing after
				// it: the durable prefix an interrupted cancel leaves, replayed.
				reopenReal(t, e, cfg, l)
			}
			recovered := e.record(t, dispatchID)
			if recovered.Phase != phaseCancelled || recovered.StopOutcome != stopNoEffect || !recovered.CancelRequested || recovered.StopEffectID != "" {
				t.Fatalf("ROW1a %s: durable state is not X: %+v", mode, recovered)
			}
			if !reflect.DeepEqual(recovered, control) {
				t.Fatalf("ROW1a %s: recovered record differs from the uninterrupted one:\ncontrol   %+v\nrecovered %+v", mode, control, recovered)
			}
			replayed, err := e.cancel(t)
			must(t, err)
			if !replayed.Replayed || replayed.State != string(phaseCancelled) {
				t.Fatalf("ROW1a %s: cancel replay %+v", mode, replayed)
			}
			dispatched, err := e.dispatch(t)
			must(t, err)
			if !dispatched.Replayed || dispatched.State != string(phaseCancelled) {
				t.Fatalf("ROW1a %s: dispatch replay %+v", mode, dispatched)
			}
			fresh := parseDoc(t, e.cancelDoc.encode(nil))
			setPath(t, fresh, "request_id", text(strings.Repeat("8", 32)))
			e.source.edit(func(i *Installed) {
				cp := parseDoc(t, i.Catalog.Cancel.Canonical)
				setPath(t, cp, "request.request_id", text(strings.Repeat("8", 32)))
				c, err := DecodeSigningPlan(cp.encode(nil))
				must(t, err)
				i.Catalog.Cancel = c
			})
			_, err = e.rt.Handle(context.Background(), e.peer, signRequest(t, fresh, e.key))
			wantCode(t, err, CodeRunTerminal)
			e.clock.set(recovered.Deadlines.Expiry + 1)
			must(t, e.rt.Sweep(context.Background()))
			must(t, e.rt.Reconcile(context.Background()))
			if after := e.record(t, dispatchID); !reflect.DeepEqual(after, recovered) {
				t.Fatalf("ROW1a %s: terminal state changed: %+v", mode, after)
			}
			wantSequence(t, l)
			e.rt.mu.Lock()
			holder := e.rt.proj.Debits[recovered.Lineage]
			e.rt.mu.Unlock()
			if holder != dispatchID {
				t.Fatalf("ROW1a %s: debit rearmed or released", mode)
			}
		})
	}
}

// Row 4, real storage (journal.md:172-174 temporary file, fsync, rename): the
// removal promoted into the run's stop intent is in the materialized record
// file when the receiver is called, after a removal detected by Sweep and
// after a removal recorded, reopened and swept. A failing materialization
// beside them stops before the call with the sync-failure barrier.
func TestStopTableRow04RemovalIntentInRecordFile(t *testing.T) {
	for _, path := range []string{"sweep", "handle-reopen-sweep", "materialization-fails"} {
		t.Run(path, func(t *testing.T) {
			e, cfg, l := realEnv(t)
			_, err := e.dispatch(t)
			must(t, err)
			original := e.record(t, dispatchID)
			removalAt := original.Deadlines.ReservedAt + 1
			e.clock.set(removalAt)
			var seen []*projection
			l.atStop = func(StopRequest) { seen = append(seen, realRecord(t, cfg)) }
			switch path {
			case "sweep":
				e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
				must(t, e.rt.Sweep(context.Background()))
				wantSequence(t, l, "start", "stop")
			case "handle-reopen-sweep":
				detectRemovalByHandle(t, e)
				reopenReal(t, e, cfg, l)
				must(t, e.rt.Sweep(context.Background()))
				wantSequence(t, l, "start", "observe", "stop")
			default:
				e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
				e.rt.store = &rejectingLedger{ledger: e.rt.store}
				err = e.rt.Sweep(context.Background())
				if !MustExit(err) || !e.rt.ExitRequired() {
					t.Fatalf("ROW4 real: expected the sync-failure barrier, got %v", err)
				}
				wantSequence(t, l, "start")
				return
			}
			rec := e.record(t, dispatchID)
			want := []StopObligation{obligation(sourceRemoval, removalAt, removalAt)}
			wantStopRequest(t, l.lastStopRequest(t), rec, want)
			if len(seen) != 1 || seen[0].Dispatches[dispatchID] == nil || seen[0].Dispatches[dispatchID].StopEffectID != rec.StopEffectID || !reflect.DeepEqual(seen[0].Dispatches[dispatchID].StopObligations, want) {
				t.Fatalf("ROW4 real %s: record file behind the journal at the call: %+v", path, seen)
			}
		})
	}
}

// Row 10 then row 8 (m0-launch.md:327 query before retry; the permit of rule
// b): after a reopen leaves an outcome-less transaction, Reconcile observes it
// once and the following Sweep issues the Stop without observing again; one
// recorded observation permits one retry. For a recorded unknown outcome the
// Sweep after Reconcile observes again (row 9, the strict reading the table
// leaves to the owner), and a second Sweep after the retry has nothing left.
func TestStopTableRow08ReconciledIntentStopsDirectly(t *testing.T) {
	for _, state := range []string{"intent-never-called", "unknown-control"} {
		t.Run(state, func(t *testing.T) {
			e, cfg, l := realEnv(t)
			_, err := e.dispatch(t)
			must(t, err)
			if state == "intent-never-called" {
				detectRemovalByHandle(t, e)
				reopenReal(t, e, cfg, l)
				must(t, e.rt.Reconcile(context.Background()))
				wantSequence(t, l, "start", "observe")
				must(t, e.rt.Sweep(context.Background()))
				wantSequence(t, l, "start", "observe", "stop")
			} else {
				unknownStop(t, e, l)
				reopenReal(t, e, cfg, l)
				must(t, e.rt.Reconcile(context.Background()))
				wantSequence(t, l, "start", "stop", "observe")
				must(t, e.rt.Sweep(context.Background()))
				wantSequence(t, l, "start", "stop", "observe", "observe", "stop")
			}
			if rec := e.record(t, dispatchID); rec.Phase != phaseAbsent {
				t.Fatalf("ROW8 %s: %+v", state, rec)
			}
			must(t, e.rt.Sweep(context.Background()))
			must(t, e.rt.Reconcile(context.Background()))
			if calls := l.sequence(); calls[len(calls)-1] != "stop" {
				t.Fatalf("ROW8 %s: terminal transaction reached the receiver: %v", state, calls)
			}
		})
	}
}

// Row 9 after reopen (m0-launch.md:325-333; a reopened process cannot tell
// «never called» from «called, reply lost»): a recovered unknown transaction
// observes the original stop effect before the same Stop from the same-ID
// cancel tail and from Sweep; the retried request equals the first. The
// never-reopened unknown transaction beside them follows the same sequence
// (control for the paths, not for the reopen).
func TestStopTableRow09RecoveredUnknownObservesBeforeRetry(t *testing.T) {
	for _, path := range []string{"cancel-tail", "sweep", "no-reopen-control"} {
		t.Run(path, func(t *testing.T) {
			e, cfg, l := realEnv(t)
			_, err := e.dispatch(t)
			must(t, err)
			first := unknownStop(t, e, l)
			if path != "no-reopen-control" {
				reopenReal(t, e, cfg, l)
			}
			if path == "sweep" {
				must(t, e.rt.Sweep(context.Background()))
			} else {
				res, err := e.cancel(t)
				must(t, err)
				if !res.Replayed || res.State != stopAbsent {
					t.Fatalf("ROW9 %s: %+v", path, res)
				}
			}
			wantSequence(t, l, "start", "stop", "observe", "stop")
			l.mu.Lock()
			stops, query := append([]StopRequest(nil), l.stops...), l.queries[0]
			l.mu.Unlock()
			if !reflect.DeepEqual(stops[0], stops[1]) {
				t.Fatalf("ROW9 %s: retried stop differs after reopen: %+v", path, stops)
			}
			if query.EffectID != first.StopEffectID || query.Operation != operationStop || query.ArgumentsDigest != first.ExecutionID {
				t.Fatalf("ROW9 %s: observation names another effect: %+v", path, query)
			}
		})
	}
}

// Row 11 (m0-launch.md:325-333 lost or restored evidence is never «not
// issued»; the permit never survives a reopen): after established recovery
// every unresolved transaction must observe before its Stop, whether the
// intent was journaled and never called, the Stop was called and its outcome
// lost, the unknown outcome was persisted, or an observation was journaled
// before the reopen. The same intent continued without a reopen (control)
// stops directly.
func TestStopTableRow11ReopenRequiresObservation(t *testing.T) {
	for _, state := range []string{"no-reopen-control", "intent-never-called", "lost-outcome", "persisted-unknown", "observed-before-reopen"} {
		t.Run(state, func(t *testing.T) {
			e, cfg, l := realEnv(t)
			_, err := e.dispatch(t)
			must(t, err)
			original := e.record(t, dispatchID)
			e.clock.set(original.Deadlines.ReservedAt + 1)
			prefix := []string{"start"}
			switch state {
			case "no-reopen-control":
				detectRemovalByHandle(t, e)
				must(t, e.rt.Sweep(context.Background()))
				wantSequence(t, l, "start", "stop")
				return
			case "intent-never-called":
				detectRemovalByHandle(t, e)
			case "lost-outcome":
				e.launcher.stopErr = errFixture
				dieAtStop(t, e, l)
				_, err = e.cancel(t)
				wantCode(t, err, CodeStorageFailed)
				e.launcher.mu.Lock()
				e.launcher.stopErr = nil
				e.launcher.mu.Unlock()
				prefix = append(prefix, "stop")
			case "persisted-unknown":
				unknownStop(t, e, l)
				prefix = append(prefix, "stop")
			default:
				unknownStop(t, e, l)
				must(t, e.rt.Reconcile(context.Background()))
				prefix = append(prefix, "stop", "observe")
			}
			reopenReal(t, e, cfg, l)
			recovered := e.record(t, dispatchID)
			if !recovered.stopUnresolved() {
				t.Fatalf("ROW11 %s: fixture did not leave an unresolved transaction: %+v", state, recovered)
			}
			if state == "lost-outcome" && recovered.StopOutcome != "" {
				t.Fatalf("ROW11 lost-outcome: an outcome frame was journaled: %+v", recovered)
			}
			must(t, e.rt.Sweep(context.Background()))
			wantSequence(t, l, append(prefix, "observe", "stop")...)
			rec := e.record(t, dispatchID)
			if rec.Phase != phaseAbsent || rec.StopEffectID != recovered.StopEffectID || rec.StopEvent != recovered.StopEvent || rec.StopDetection != recovered.StopDetection || len(rec.StopObligations) != len(recovered.StopObligations) {
				t.Fatalf("ROW11 %s: original obligation changed: before=%+v after=%+v", state, recovered, rec)
			}
			if starts, _ := e.launcher.count(); starts != 1 {
				t.Fatalf("ROW11 %s: starts=%d", state, starts)
			}
		})
	}
}

// Row 16 (journal.md:171 a committed intent stays committed if a later
// operation fails; m0-launch.md:332 lost evidence is never «never launched»):
// when the stop outcome frame cannot be appended, or the process dies at the
// Stop boundary, the transaction stays as it was and after the reopen it must
// observe before the same Stop; the retried request equals the first and no
// second launch happens. The persisted unknown outcome beside them is the
// control that an outcome frame, when it can be written, records the receipt.
func TestStopTableRow16LostOutcomeStaysUnresolved(t *testing.T) {
	for _, path := range []string{"persisted-unknown-control", "outcome-append-fails", "process-dies-at-stop"} {
		t.Run(path, func(t *testing.T) {
			e, cfg, l := realEnv(t)
			_, err := e.dispatch(t)
			must(t, err)
			e.launcher.stopErr = errFixture
			switch path {
			case "persisted-unknown-control":
				_, err = e.cancel(t)
				wantCode(t, err, CodeObservationUnknown)
				if rec := e.record(t, dispatchID); rec.StopOutcome != stopUnknown || rec.StopAttempts != 1 {
					t.Fatalf("ROW16 control: %+v", rec)
				}
			case "outcome-append-fails":
				e.rt.store = &typedFailLedger{ledger: e.rt.store, fail: frameStop}
				_, err = e.cancel(t)
				wantCode(t, err, CodeStorageFailed)
			default:
				dieAtStop(t, e, l)
				_, err = e.cancel(t)
				wantCode(t, err, CodeStorageFailed)
			}
			e.launcher.mu.Lock()
			e.launcher.stopErr = nil
			e.launcher.mu.Unlock()
			wantSequence(t, l, "start", "stop")
			reopenReal(t, e, cfg, l)
			recovered := e.record(t, dispatchID)
			if path != "persisted-unknown-control" && (recovered.StopOutcome != "" || recovered.StopAttempts != 0 || recovered.StopEffectID == "") {
				t.Fatalf("ROW16 %s: lost outcome not recovered as unresolved intent: %+v", path, recovered)
			}
			must(t, e.rt.Sweep(context.Background()))
			wantSequence(t, l, "start", "stop", "observe", "stop")
			l.mu.Lock()
			stops := append([]StopRequest(nil), l.stops...)
			l.mu.Unlock()
			if !reflect.DeepEqual(stops[0], stops[1]) {
				t.Fatalf("ROW16 %s: retried stop differs: %+v", path, stops)
			}
			rec := e.record(t, dispatchID)
			if starts, stopsIssued := e.launcher.count(); starts != 1 || stopsIssued != 2 || rec.Phase != phaseAbsent {
				t.Fatalf("ROW16 %s: starts=%d stops=%d %+v", path, starts, stopsIssued, rec)
			}
		})
	}
}
