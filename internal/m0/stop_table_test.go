package m0

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

// This file holds one test per row of the stop state table (spec/m0-launch.md
// sections 3 to 5 and spec/journal.md materialization rules) that can be
// driven over the in-memory ledger. Rows whose event is a reopen, and every
// path that needs real storage, are in stop_table_linux_test.go.

// tableLauncher traces every receiver call in order with its arguments and
// lets a case override the stop receipt, fail the observation or run a hook
// at the Stop boundary.
type tableLauncher struct {
	*fakeLauncher
	mu         sync.Mutex
	calls      []string
	stops      []StopRequest
	queries    []EffectQuery
	receipt    *StopReceipt
	observeErr error
	atStop     func(StopRequest)
	atStart    func(StartRequest)
	atObserve  func(EffectQuery)
}

func traceLauncher(e *testEnv) *tableLauncher {
	l := &tableLauncher{fakeLauncher: e.launcher}
	e.rt.seams.Launcher = l
	return l
}

func (l *tableLauncher) Start(ctx context.Context, req StartRequest, gate CommitGate) (StartReceipt, error) {
	l.mu.Lock()
	l.calls = append(l.calls, "start")
	hook := l.atStart
	l.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	return l.fakeLauncher.Start(ctx, req, gate)
}

func (l *tableLauncher) Stop(ctx context.Context, req StopRequest) (StopReceipt, error) {
	l.mu.Lock()
	l.calls = append(l.calls, "stop")
	l.stops = append(l.stops, req)
	hook, receipt := l.atStop, l.receipt
	l.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	if receipt != nil {
		l.fakeLauncher.mu.Lock()
		l.fakeLauncher.stops++
		l.fakeLauncher.lastStop = req
		l.fakeLauncher.mu.Unlock()
		return *receipt, nil
	}
	return l.fakeLauncher.Stop(ctx, req)
}

func (l *tableLauncher) Observe(ctx context.Context, q EffectQuery) (Observation, error) {
	l.mu.Lock()
	l.calls = append(l.calls, "observe")
	l.queries = append(l.queries, q)
	oerr, hook := l.observeErr, l.atObserve
	l.mu.Unlock()
	if hook != nil {
		hook(q)
	}
	if oerr != nil {
		return ObservationUnknown, oerr
	}
	return l.fakeLauncher.Observe(ctx, q)
}

func (l *tableLauncher) sequence() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func (l *tableLauncher) lastStopRequest(t *testing.T) StopRequest {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.stops) == 0 {
		t.Fatal("no stop request was issued")
	}
	return l.stops[len(l.stops)-1]
}

// wantSequence asserts the exact receiver call order; a table row names the
// calls it allows and nothing else may reach the receiver.
func wantSequence(t *testing.T, l *tableLauncher, want ...string) {
	t.Helper()
	got := l.sequence()
	same := len(got) == len(want)
	for i := 0; same && i < len(got); i++ {
		same = got[i] == want[i]
	}
	if !same {
		t.Fatalf("receiver call sequence: got %v want %v", got, want)
	}
}

// wantStopRequest asserts the stop effect's identity, first event and
// detection, obligations and ceiling against the record they were built from.
func wantStopRequest(t *testing.T, got StopRequest, rec dispatchRecord, obligations []StopObligation) {
	t.Helper()
	wantEffect := effectID(rec.Principal, rec.RequestID, lineageRecordID, componentLaunch, operationStop, 1)
	if got.EffectID != wantEffect || got.EffectID != rec.StopEffectID || got.ExecutionID != rec.ExecutionID || got.Worker != rec.Worker {
		t.Fatalf("stop identity: got %+v want effect %s record %+v", got, wantEffect, rec)
	}
	if len(obligations) == 0 {
		t.Fatal("test bug: a stop request needs at least one expected obligation")
	}
	if got.Event != obligations[0].Event || got.Detection != obligations[0].Detection {
		t.Fatalf("stop event/detection: got %d/%d want %d/%d", got.Event, got.Detection, obligations[0].Event, obligations[0].Detection)
	}
	if !reflect.DeepEqual(got.Obligations, obligations) {
		t.Fatalf("stop obligations: got %+v want %+v", got.Obligations, obligations)
	}
	ceiling := int64(0)
	for _, o := range obligations {
		c := StopCeiling(o.Event, o.Detection, rec.Deadlines.DetectSeconds, rec.Deadlines.StopSeconds)
		if ceiling == 0 || c < ceiling {
			ceiling = c
		}
	}
	if got.Ceiling != ceiling || got.Ceiling == 0 {
		t.Fatalf("stop ceiling: got %d want earliest %d over %+v", got.Ceiling, ceiling, obligations)
	}
}

func obligation(source string, event, detection int64) StopObligation {
	return StopObligation{Source: source, Event: event, Detection: detection}
}

// record returns the bytes of one cached record of the in-memory ledger.
func (m *memLedger) record(name string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.records[name]
	return append([]byte(nil), b...), ok
}

// cachedProjection decodes the materialized projection record.
func cachedProjection(t *testing.T, b []byte, ok bool) *projection {
	t.Helper()
	if !ok {
		t.Fatal("projection record not materialized")
	}
	var p projection
	must(t, json.Unmarshal(b, &p))
	return &p
}

// memProjection decodes the projection record cached in the in-memory ledger.
func memProjection(t *testing.T, m *memLedger) *projection {
	t.Helper()
	b, ok := m.record(projectionRecord)
	return cachedProjection(t, b, ok)
}

// reservationOnly produces table state N0 over the in-memory ledger: the
// process is lost after the reserve frame and before the intent frame, and
// the ledger is replayed into a fresh runtime (an in-memory stand-in for
// established recovery; the real-journal reopen rows live in the Linux file).
func reservationOnly(t *testing.T, e *testEnv) *tableLauncher {
	t.Helper()
	e.store.failAppendAt = e.store.appendCalls + 3 // policy, reserve, then intent fails
	e.store.failErr = &journal.Error{Class: journal.StorageWriteFailure, Op: "pwrite", Poisoned: true}
	_, err := e.dispatch(t)
	wantCode(t, err, CodeStorageFailed)
	if e.store.count(frameReserve) != 1 || e.store.count(frameIntent) != 0 {
		t.Fatal("fixture: reservation-only state not reproduced")
	}
	e.reopen(t)
	if rec := e.record(t, dispatchID); rec.Phase != phaseReserved || rec.EffectID != "" {
		t.Fatalf("fixture: %+v", rec)
	}
	return traceLauncher(e)
}

// detectRemovalByHandle marks the installation removed and lets a fresh signed
// dispatch detect and durably record it (refused m0_policy_removed).
func detectRemovalByHandle(t *testing.T, e *testEnv) {
	t.Helper()
	e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
	probe := parseDoc(t, e.dispatchDoc.encode(nil))
	setPath(t, probe, "request_id", text(strings.Repeat("5", 32)))
	_, err := e.rt.Handle(context.Background(), e.peer, signRequest(t, probe, e.key))
	wantCode(t, err, CodePolicyRemoved)
}

// unknownStop drives a started run into table state U: the owner cancel is
// issued once and the receiver fails, so the outcome frame records unknown.
func unknownStop(t *testing.T, e *testEnv, l *tableLauncher) dispatchRecord {
	t.Helper()
	e.launcher.mu.Lock()
	e.launcher.stopErr = errFixture
	e.launcher.mu.Unlock()
	_, err := e.cancel(t)
	wantCode(t, err, CodeObservationUnknown)
	e.launcher.mu.Lock()
	e.launcher.stopErr = nil
	e.launcher.mu.Unlock()
	rec := e.record(t, dispatchID)
	if rec.StopOutcome != stopUnknown || rec.Phase != phaseStarted {
		t.Fatalf("fixture: state U not reached: %+v", rec)
	}
	wantSequence(t, l, "start", "stop")
	return rec
}

// rejectingLedger fails every projection materialization with the journal's
// poisoned sync-failure shape; appends pass through.
type rejectingLedger struct{ ledger }

func (r *rejectingLedger) ReplaceRecord(string, []byte) error { return syncFailure() }

// typedFailLedger fails the append of the first frame of one type with a
// poisoned write failure, without writing it; everything else passes through.
type typedFailLedger struct {
	ledger
	mu     sync.Mutex
	fail   frameType
	failed bool
}

func (f *typedFailLedger) Append(b []byte) (uint64, error) {
	f.mu.Lock()
	armed := !f.failed
	f.mu.Unlock()
	if armed {
		if fr, err := decodeFrame(b); err == nil && fr.Type == f.fail {
			f.mu.Lock()
			f.failed = true
			f.mu.Unlock()
			return 0, &journal.Error{Class: journal.StorageWriteFailure, Op: "pwrite", Poisoned: true}
		}
	}
	return f.ledger.Append(b)
}

// Row 1 (m0-launch.md:324, journal.md:194): the owned cancel of a
// reservation that never reached intent journals the cancel record and the
// no_effect outcome, issues no call and ends the transaction (state X). The
// cancel of a run with launch intent is the positive control: it issues a Stop.
func TestStopTableRow01CancelOfReservation(t *testing.T) {
	t.Run("reservation-no-effect", func(t *testing.T) {
		e := newEnv(t)
		l := reservationOnly(t, e)
		res, err := e.cancel(t)
		must(t, err)
		rec := e.record(t, dispatchID)
		if res.State != string(phaseCancelled) || rec.Phase != phaseCancelled || rec.StopOutcome != stopNoEffect || rec.StopEffectID != "" || len(rec.StopObligations) != 0 {
			t.Fatalf("ROW1: %+v %+v", res, rec)
		}
		if e.store.count(frameCancel) != 1 || e.store.count(frameStop) != 1 {
			t.Fatalf("ROW1: cancel frames=%d stop frames=%d", e.store.count(frameCancel), e.store.count(frameStop))
		}
		wantSequence(t, l)
		if p := memProjection(t, e.store); p.Dispatches[dispatchID].StopOutcome != stopNoEffect {
			t.Fatalf("ROW1: terminal state not materialized: %+v", p.Dispatches[dispatchID])
		}
	})
	t.Run("intent-control", func(t *testing.T) {
		e := newEnv(t)
		l := traceLauncher(e)
		_, err := e.dispatch(t)
		must(t, err)
		res, err := e.cancel(t)
		must(t, err)
		if res.State != stopAbsent {
			t.Fatalf("ROW1 control: %+v", res)
		}
		wantSequence(t, l, "start", "stop")
	})
}

// Row 2 (m0-launch.md:324 «before durable reservation ... no effect»; a
// reservation without intent has no effect to stop): expiry, removal, Sweep and
// Reconcile leave state N0 untouched with no call and no obligation. The same
// events on a run with launch intent create stop debt (positive control). The
// reopen event of this row is in the Linux file.
func TestStopTableRow02ReservationIsInert(t *testing.T) {
	for _, event := range []string{"expiry-sweep", "removal-sweep", "reconcile"} {
		t.Run(event, func(t *testing.T) {
			e := newEnv(t)
			l := reservationOnly(t, e)
			rec := e.record(t, dispatchID)
			switch event {
			case "expiry-sweep":
				e.clock.set(rec.Deadlines.Expiry + 1)
				must(t, e.rt.Sweep(context.Background()))
			case "removal-sweep":
				e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
				must(t, e.rt.Sweep(context.Background()))
			default:
				must(t, e.rt.Reconcile(context.Background()))
			}
			after := e.record(t, dispatchID)
			if after.Phase != phaseReserved || after.StopEffectID != "" || len(after.StopObligations) != 0 || after.StopOutcome != "" {
				t.Fatalf("ROW2 %s: stop state created on a reservation: %+v", event, after)
			}
			if e.store.count(frameStopIntent) != 0 || e.store.count(frameStop) != 0 || e.store.count(frameObserve) != 0 {
				t.Fatalf("ROW2 %s: stop frames written", event)
			}
			wantSequence(t, l)
		})
	}
	t.Run("intent-control", func(t *testing.T) {
		e := newEnv(t)
		l := traceLauncher(e)
		_, err := e.dispatch(t)
		must(t, err)
		rec := e.record(t, dispatchID)
		e.clock.set(rec.Deadlines.Expiry)
		must(t, e.rt.Sweep(context.Background()))
		wantSequence(t, l, "start", "stop")
		wantStopRequest(t, l.lastStopRequest(t), e.record(t, dispatchID), []StopObligation{obligation(sourceExpiry, rec.Deadlines.Expiry, rec.Deadlines.Expiry)})
	})
}

// Row 3 (m0-launch.md:299-301 intent before call; :291-292 owned cancel): the
// owned cancel of a run with launch intent journals the cancel record as the
// stop intent with the cancel obligation, then issues exactly one Stop carrying
// the derived stop effect, the cancel time as event and detection, that single
// obligation and its ceiling. The reservation cancel beside it (row 1) issues
// nothing.
func TestStopTableRow03CancelOfLaunchIntent(t *testing.T) {
	e := newEnv(t)
	l := traceLauncher(e)
	_, err := e.dispatch(t)
	must(t, err)
	original := e.record(t, dispatchID)
	at := original.Deadlines.ReservedAt + 7
	e.clock.set(at)
	res, err := e.cancel(t)
	must(t, err)
	wantSequence(t, l, "start", "stop")
	rec := e.record(t, dispatchID)
	wantStopRequest(t, l.lastStopRequest(t), rec, []StopObligation{obligation(sourceCancel, at, at)})
	if e.store.count(frameCancel) != 1 || e.store.count(frameStopIntent) != 0 || res.EffectID != rec.StopEffectID || res.RequestID != cancelID {
		t.Fatalf("ROW3: cancel frames=%d intent frames=%d res=%+v", e.store.count(frameCancel), e.store.count(frameStopIntent), res)
	}
	if rec.StopSource != sourceCancel || rec.StopEvent != at || rec.StopDetection != at {
		t.Fatalf("ROW3: provenance %+v", rec)
	}
	// Beside it: the reservation cancel of row 1 writes no intent and calls nothing.
	e2 := newEnv(t)
	l2 := reservationOnly(t, e2)
	_, err = e2.cancel(t)
	must(t, err)
	wantSequence(t, l2)
}

// Row 4 (m0-launch.md:227-235 removal intent, original clocks; journal.md:172
// barrier): a durably recorded removal, detected by a refused dispatch, by
// Sweep or by a cancel, becomes the stop obligation of the run with its
// original event and detection, and the next stop pass issues one Stop whose
// intent is already materialized in the cached record. The barrier witness
// beside them: when materialization fails, zero Stops reach the receiver and
// the sync-failure barrier is reported.
func TestStopTableRow04RemovalPromotesStopIntent(t *testing.T) {
	for _, path := range []string{"handle-then-sweep", "sweep", "cancel", "sweep-materialization-fails"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t)
			l := traceLauncher(e)
			_, err := e.dispatch(t)
			must(t, err)
			original := e.record(t, dispatchID)
			removalAt := original.Deadlines.ReservedAt + 1
			e.clock.set(removalAt)
			var seen []*projection
			l.atStop = func(StopRequest) { seen = append(seen, memProjection(t, e.store)) }
			switch path {
			case "handle-then-sweep":
				detectRemovalByHandle(t, e)
				e.clock.set(removalAt + 40)
				must(t, e.rt.Sweep(context.Background()))
			case "sweep":
				e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
				must(t, e.rt.Sweep(context.Background()))
			case "cancel":
				e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
				_, err = e.cancel(t)
				must(t, err)
			default:
				e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
				e.rt.store = &rejectingLedger{ledger: e.rt.store}
				err = e.rt.Sweep(context.Background())
				if !MustExit(err) || !e.rt.ExitRequired() {
					t.Fatalf("ROW4 barrier: expected the sync-failure barrier, got %v", err)
				}
				wantSequence(t, l, "start")
				if rec := e.record(t, dispatchID); !rec.hasObligation(sourceRemoval) || rec.StopOutcome != "" {
					t.Fatalf("ROW4 barrier: intent not retained as journaled: %+v", rec)
				}
				return
			}
			wantSequence(t, l, "start", "stop")
			rec := e.record(t, dispatchID)
			want := []StopObligation{obligation(sourceRemoval, removalAt, removalAt)}
			if path == "cancel" {
				want = append(want, obligation(sourceCancel, removalAt, removalAt))
			}
			wantStopRequest(t, l.lastStopRequest(t), rec, want)
			if len(seen) != 1 || seen[0].Dispatches[dispatchID] == nil || seen[0].Dispatches[dispatchID].StopEffectID != rec.StopEffectID || !reflect.DeepEqual(seen[0].Dispatches[dispatchID].StopObligations, want) {
				t.Fatalf("ROW4 %s: intent not materialized before the receiver call: %+v", path, seen)
			}
			if rec.Phase != phaseAbsent || rec.StopSource != sourceRemoval {
				t.Fatalf("ROW4 %s: %+v", path, rec)
			}
		})
	}
}

// Row 5 (m0-launch.md:342-343 expiry closes C and triggers stop; :354
// «Neither receipt arrival nor delayed polling may reset them»): the expiry
// obligation is recorded the first time a durable frame carries qualified time
// at or past the original expiry, with the expiry as event and that frame's
// time as detection, whether the frame is Sweep's own expiry intent, a cancel
// frame or a policy frame; the next pass issues one Stop carrying it. Before
// expiry Sweep records and calls nothing (control of the other direction).
func TestStopTableRow05ExpiryDetection(t *testing.T) {
	for _, path := range []string{"before-expiry-control", "sweep", "cancel-frame", "policy-frame"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t)
			l := traceLauncher(e)
			_, err := e.dispatch(t)
			must(t, err)
			original := e.record(t, dispatchID)
			expiry := original.Deadlines.Expiry
			var seen []*projection
			l.atStop = func(StopRequest) { seen = append(seen, memProjection(t, e.store)) }
			want := []StopObligation{}
			switch path {
			case "before-expiry-control":
				e.clock.set(expiry - 1)
				must(t, e.rt.Sweep(context.Background()))
				wantSequence(t, l, "start")
				if rec := e.record(t, dispatchID); rec.StopEffectID != "" || e.store.count(frameStopIntent) != 0 {
					t.Fatalf("ROW5 control: expiry recorded early: %+v", rec)
				}
				return
			case "sweep":
				e.clock.set(expiry)
				must(t, e.rt.Sweep(context.Background()))
				want = append(want, obligation(sourceExpiry, expiry, expiry))
				if e.store.count(frameStopIntent) != 1 {
					t.Fatalf("ROW5 sweep: expiry intent frames=%d", e.store.count(frameStopIntent))
				}
			case "cancel-frame":
				e.clock.set(expiry + 5)
				_, err = e.cancel(t)
				must(t, err)
				want = append(want, obligation(sourceExpiry, expiry, expiry+5), obligation(sourceCancel, expiry+5, expiry+5))
				if e.store.count(frameStopIntent) != 0 {
					t.Fatal("ROW5 cancel-frame: a separate expiry intent was written although the cancel frame proved expiry")
				}
			default:
				e.clock.set(expiry + 5)
				// An installation re-read with an advanced installation
				// revision writes a policy frame carrying the time.
				e.source.edit(func(i *Installed) { i.InstallRevision++ })
				must(t, e.rt.Sweep(context.Background()))
				want = append(want, obligation(sourceExpiry, expiry, expiry+5))
				if e.store.count(frameStopIntent) != 0 || e.store.count(framePolicy) != 2 {
					t.Fatalf("ROW5 policy-frame: intent frames=%d policy frames=%d", e.store.count(frameStopIntent), e.store.count(framePolicy))
				}
			}
			wantSequence(t, l, "start", "stop")
			rec := e.record(t, dispatchID)
			wantStopRequest(t, l.lastStopRequest(t), rec, want)
			if rec.StopSource != sourceExpiry || rec.StopEvent != expiry {
				t.Fatalf("ROW5 %s: provenance %+v", path, rec)
			}
			if len(seen) != 1 || !reflect.DeepEqual(seen[0].Dispatches[dispatchID].StopObligations, want) {
				t.Fatalf("ROW5 %s: intent not materialized before the receiver call: %+v", path, seen)
			}
		})
	}
}

// Row 6 (m0-launch.md:351-354 retain separate original events, earliest
// ceiling; :372-375 stop continues under its original effect): a further
// obligation on an unresolved transaction (owner cancel, removal, expiry) is
// added with its own clocks; the retried Stop keeps the original effect
// identity, first event and detection, carries every obligation and a ceiling
// that is never later than before. The first stop request is the control each
// retry is compared with.
func TestStopTableRow06LaterObligationKeepsIdentity(t *testing.T) {
	for _, path := range []string{"cancel", "removal", "expiry"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t)
			l := traceLauncher(e)
			_, err := e.dispatch(t)
			must(t, err)
			original := e.record(t, dispatchID)
			expiry := original.Deadlines.Expiry
			var first dispatchRecord
			var want []StopObligation
			switch path {
			case "cancel":
				// First intent from expiry, receiver failing: state U.
				e.clock.set(expiry)
				e.launcher.stopErr = errFixture
				must(t, e.rt.Sweep(context.Background()))
				e.launcher.mu.Lock()
				e.launcher.stopErr = nil
				e.launcher.mu.Unlock()
				first = e.record(t, dispatchID)
				e.clock.set(expiry + 20)
				_, err = e.cancel(t)
				must(t, err)
				want = []StopObligation{obligation(sourceExpiry, expiry, expiry), obligation(sourceCancel, expiry+20, expiry+20)}
			case "removal":
				cancelAt := original.Deadlines.ReservedAt + 1
				e.clock.set(cancelAt)
				first = unknownStop(t, e, l)
				e.clock.set(cancelAt + 1)
				e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
				must(t, e.rt.Sweep(context.Background()))
				want = []StopObligation{obligation(sourceCancel, cancelAt, cancelAt), obligation(sourceRemoval, cancelAt+1, cancelAt+1)}
			default:
				cancelAt := original.Deadlines.ReservedAt + 1
				e.clock.set(cancelAt)
				first = unknownStop(t, e, l)
				e.clock.set(expiry)
				must(t, e.rt.Sweep(context.Background()))
				want = []StopObligation{obligation(sourceCancel, cancelAt, cancelAt), obligation(sourceExpiry, expiry, expiry)}
			}
			if first.StopOutcome != stopUnknown {
				t.Fatalf("ROW6 %s: fixture did not reach U: %+v", path, first)
			}
			wantSequence(t, l, "start", "stop", "observe", "stop")
			rec := e.record(t, dispatchID)
			retried := l.lastStopRequest(t)
			wantStopRequest(t, retried, rec, want)
			l.mu.Lock()
			initial := l.stops[0]
			l.mu.Unlock()
			if retried.EffectID != initial.EffectID || retried.Event != initial.Event || retried.Detection != initial.Detection || retried.ExecutionID != initial.ExecutionID {
				t.Fatalf("ROW6 %s: identity changed: first=%+v retried=%+v", path, initial, retried)
			}
			if retried.Ceiling > initial.Ceiling {
				t.Fatalf("ROW6 %s: ceiling moved later: %d > %d", path, retried.Ceiling, initial.Ceiling)
			}
			if rec.StopEvent != first.StopEvent || rec.StopDetection != first.StopDetection || rec.StopSource != first.StopSource || rec.Phase != phaseAbsent {
				t.Fatalf("ROW6 %s: record provenance changed: first=%+v now=%+v", path, first, rec)
			}
		})
	}
}

// Row 7 (m0-launch.md:313-315 same ID recovers only the original record;
// :171-173 same-ID retries reuse records): a same-ID cancel replay on an
// unresolved transaction writes no new cancel record, continues the stop as
// rows 8 to 10 require and replies with the original cancel's outcome. A
// changed body under the same ID is refused before any call (negative beside
// the replay).
func TestStopTableRow07SameIDCancelReplay(t *testing.T) {
	e := newEnv(t)
	l := traceLauncher(e)
	_, err := e.dispatch(t)
	must(t, err)
	unknownStop(t, e, l)
	changed := parseDoc(t, e.cancelDoc.encode(nil))
	setPath(t, changed, "plan.harness", text("sha256:"+strings.Repeat("9", 64)))
	_, err = e.rt.Handle(context.Background(), e.peer, signRequest(t, changed, e.key))
	wantCode(t, err, CodeRequestIDConflict)
	wantSequence(t, l, "start", "stop")
	res, err := e.cancel(t)
	must(t, err)
	wantSequence(t, l, "start", "stop", "observe", "stop")
	rec := e.record(t, dispatchID)
	if !res.Replayed || res.RequestID != cancelID || res.State != stopAbsent || res.EffectID != rec.StopEffectID || res.Op != OpCancel {
		t.Fatalf("ROW7: reply is not the original cancel's outcome: %+v", res)
	}
	if e.store.count(frameCancel) != 1 {
		t.Fatalf("ROW7: cancel frames=%d", e.store.count(frameCancel))
	}
	l.mu.Lock()
	same := reflect.DeepEqual(l.stops[0], l.stops[1])
	l.mu.Unlock()
	if !same {
		t.Fatalf("ROW7: retried stop differs: %+v", l.stops)
	}
}

// Row 8 (m0-launch.md:299-301 intent journaled before its call; the permit is
// the process's own first intent): a stop transaction whose first intent this
// process appended itself, by cancel, by Sweep at expiry or by a detected
// removal, is fresh and the stop pass issues the Stop directly, without an
// observation. A transaction with a recorded unknown outcome observes first
// (row 9 beside it). The after-Reconcile path of this row is in the Linux file.
func TestStopTableRow08FreshIntentStopsDirectly(t *testing.T) {
	for _, path := range []string{"cancel", "sweep-expiry", "sweep-removal", "unknown-observes-control"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t)
			l := traceLauncher(e)
			_, err := e.dispatch(t)
			must(t, err)
			original := e.record(t, dispatchID)
			switch path {
			case "cancel":
				_, err = e.cancel(t)
				must(t, err)
			case "sweep-expiry":
				e.clock.set(original.Deadlines.Expiry)
				must(t, e.rt.Sweep(context.Background()))
			case "sweep-removal":
				e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
				must(t, e.rt.Sweep(context.Background()))
			default:
				unknownStop(t, e, l)
				must(t, e.rt.Sweep(context.Background()))
				wantSequence(t, l, "start", "stop", "observe", "stop")
				return
			}
			wantSequence(t, l, "start", "stop")
			if rec := e.record(t, dispatchID); rec.Phase != phaseAbsent || rec.StopObserved != "" || e.store.count(frameObserve) != 0 {
				t.Fatalf("ROW8 %s: %+v", path, rec)
			}
		})
	}
}

// Row 9 (m0-launch.md:325-333 query the original effect before a same-effect
// retry): a transaction with a recorded unknown outcome observes the original
// stop effect, journals the class and only then retries the same Stop, from
// the same-ID cancel tail and from Sweep alike. A fresh transaction beside
// them (row 8) does not observe. The after-reopen paths of this row are in the
// Linux file.
func TestStopTableRow09UnknownObservesBeforeRetry(t *testing.T) {
	for _, path := range []string{"cancel-tail", "sweep", "fresh-control"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t)
			l := traceLauncher(e)
			_, err := e.dispatch(t)
			must(t, err)
			if path == "fresh-control" {
				_, err = e.cancel(t)
				must(t, err)
				wantSequence(t, l, "start", "stop")
				return
			}
			first := unknownStop(t, e, l)
			if path == "cancel-tail" {
				_, err = e.cancel(t)
				must(t, err)
			} else {
				must(t, e.rt.Sweep(context.Background()))
			}
			wantSequence(t, l, "start", "stop", "observe", "stop")
			rec := e.record(t, dispatchID)
			l.mu.Lock()
			query, stops := l.queries[0], append([]StopRequest(nil), l.stops...)
			l.mu.Unlock()
			if query != (EffectQuery{EffectID: first.StopEffectID, Operation: operationStop, ArgumentsDigest: first.ExecutionID}) {
				t.Fatalf("ROW9 %s: observation names another effect: %+v", path, query)
			}
			if !reflect.DeepEqual(stops[0], stops[1]) {
				t.Fatalf("ROW9 %s: retried stop differs: %+v", path, stops)
			}
			if rec.StopObserved == "" || rec.Phase != phaseAbsent || rec.StopAttempts != 2 {
				t.Fatalf("ROW9 %s: %+v", path, rec)
			}
		})
	}
}

// Row 10 (m0-launch.md:327 «Query/reconcile the original receiver effect»;
// :253-254 no retry launch): Reconcile observes an unresolved stop transaction,
// journals the class as a stop observation and never calls Stop. A terminal
// transaction beside it is not observed. After Reconcile the recorded unknown
// transaction still observes before its retry (row 9, as the table leaves U
// with a rule-(b) permit undecided and R3 is read strictly); the fresh
// continuation of an outcome-less transaction is in the Linux file.
func TestStopTableRow10ReconcileObservesNeverStops(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		e := newEnv(t)
		l := traceLauncher(e)
		_, err := e.dispatch(t)
		must(t, err)
		first := unknownStop(t, e, l)
		e.launcher.observe = ObservationPresent
		must(t, e.rt.Reconcile(context.Background()))
		wantSequence(t, l, "start", "stop", "observe")
		rec := e.record(t, dispatchID)
		if rec.StopObserved != ObservationPresent || rec.StopOutcome != stopUnknown || rec.StopAttempts != 1 || rec.Phase != phaseStarted {
			t.Fatalf("ROW10: %+v", rec)
		}
		if e.store.count(frameObserve) != 1 || e.store.count(frameStop) != 1 {
			t.Fatalf("ROW10: observe frames=%d stop frames=%d", e.store.count(frameObserve), e.store.count(frameStop))
		}
		l.mu.Lock()
		query := l.queries[0]
		l.mu.Unlock()
		if query != (EffectQuery{EffectID: first.StopEffectID, Operation: operationStop, ArgumentsDigest: first.ExecutionID}) {
			t.Fatalf("ROW10: observation names another effect: %+v", query)
		}
		must(t, e.rt.Sweep(context.Background()))
		wantSequence(t, l, "start", "stop", "observe", "observe", "stop")
	})
	t.Run("terminal-control", func(t *testing.T) {
		e := newEnv(t)
		l := traceLauncher(e)
		_, err := e.dispatch(t)
		must(t, err)
		_, err = e.cancel(t)
		must(t, err)
		must(t, e.rt.Reconcile(context.Background()))
		wantSequence(t, l, "start", "stop")
	})
}

// Row 12 (m0-launch.md:397-399 only fenced confirmed absence releases; a
// released or effect-less transaction is final): on a terminal transaction
// (absent, or the no-effect cancel) a later expiry, a later removal, a same-ID
// cancel replay and a new cancel change no stop state and reach the receiver
// with nothing; the new cancel is refused m0_run_terminal. An unresolved
// transaction beside them is continued by the same events (control).
func TestStopTableRow12TerminalIsInert(t *testing.T) {
	for _, state := range []string{"absent", "no-effect", "unresolved-control"} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			var l *tableLauncher
			switch state {
			case "absent":
				l = traceLauncher(e)
				_, err := e.dispatch(t)
				must(t, err)
				_, err = e.cancel(t)
				must(t, err)
			case "no-effect":
				l = reservationOnly(t, e)
				_, err := e.cancel(t)
				must(t, err)
			default:
				l = traceLauncher(e)
				_, err := e.dispatch(t)
				must(t, err)
				unknownStop(t, e, l)
				// The receiver keeps answering without absence so the
				// transaction stays unresolved through every event.
				l.receipt = &StopReceipt{}
			}
			before := e.record(t, dispatchID)
			calls := len(l.sequence())
			e.clock.set(before.Deadlines.Expiry + 1)
			must(t, e.rt.Sweep(context.Background()))
			e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision++ })
			must(t, e.rt.Sweep(context.Background()))
			must(t, e.rt.Reconcile(context.Background()))
			if state == "unresolved-control" {
				if got := l.sequence(); len(got) <= calls {
					t.Fatalf("ROW12 control: unresolved transaction not continued: %v", got)
				}
				if rec := e.record(t, dispatchID); !rec.hasObligation(sourceExpiry) || !rec.hasObligation(sourceRemoval) {
					t.Fatalf("ROW12 control: later obligations not added: %+v", rec)
				}
				return
			}
			res, err := e.cancel(t)
			must(t, err)
			if !res.Replayed || res.State != before.StopOutcome && !(before.StopOutcome == stopNoEffect && res.State == string(phaseCancelled)) {
				t.Fatalf("ROW12 %s: replay %+v", state, res)
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
			after := e.record(t, dispatchID)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("ROW12 %s: terminal state changed:\nbefore %+v\nafter  %+v", state, before, after)
			}
			if got := l.sequence(); len(got) != calls {
				t.Fatalf("ROW12 %s: receiver reached: %v", state, got)
			}
		})
	}
}

// Row 13 (journal.md:169-174 only success commits; a failed materialization
// returns before the dependent call): when the materialization before the Stop
// fails, or the cancel intent cannot be appended, no receiver call is made,
// the failure is reported with its exit duty where the journal demands it, and
// the committed frames are exactly those that succeeded. The healthy cancel
// beside them issues the Stop.
func TestStopTableRow13FailureBeforeCallHolds(t *testing.T) {
	for _, path := range []string{"healthy-control", "materialization-fails", "intent-append-fails"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t)
			l := traceLauncher(e)
			_, err := e.dispatch(t)
			must(t, err)
			frames := e.store.count(frameCancel)
			switch path {
			case "healthy-control":
				_, err = e.cancel(t)
				must(t, err)
				wantSequence(t, l, "start", "stop")
				return
			case "materialization-fails":
				e.rt.store = &rejectingLedger{ledger: e.rt.store}
				_, err = e.cancel(t)
				if !MustExit(err) || !e.rt.ExitRequired() {
					t.Fatalf("ROW13 %s: expected the sync-failure barrier, got %v", path, err)
				}
				if e.store.count(frameCancel) != frames+1 {
					t.Fatalf("ROW13 %s: the committed intent was not retained", path)
				}
			default:
				e.rt.store = &typedFailLedger{ledger: e.rt.store, fail: frameCancel}
				_, err = e.cancel(t)
				wantCode(t, err, CodeStorageFailed)
				if e.store.count(frameCancel) != frames {
					t.Fatalf("ROW13 %s: a failed append left a frame", path)
				}
			}
			wantSequence(t, l, "start")
			_, err = e.cancel(t)
			wantCode(t, err, CodeStorageFailed)
			wantSequence(t, l, "start")
		})
	}
}

// Row 14 (m0-launch.md:397-399 receiver-owned confirmed absence with evidence
// releases the live claim; :253-254 nothing rearms the debit): a Stop receipt
// with bounded absence evidence ends the transaction as absent with that
// evidence, the run is released, the lineage debit stays spent, and no later
// pass or reconciliation calls the receiver again. Row 15 holds the negatives.
func TestStopTableRow14AbsenceReleases(t *testing.T) {
	e := newEnv(t)
	l := traceLauncher(e)
	_, err := e.dispatch(t)
	must(t, err)
	evidence := []byte("fenced account-wide absence")
	l.receipt = &StopReceipt{Absent: true, Evidence: evidence}
	res, err := e.cancel(t)
	must(t, err)
	rec := e.record(t, dispatchID)
	if res.State != stopAbsent || rec.Phase != phaseAbsent || rec.StopOutcome != stopAbsent || string(rec.StopEvidence) != string(evidence) || rec.StopAttempts != 1 {
		t.Fatalf("ROW14: %+v %+v", res, rec)
	}
	e.rt.mu.Lock()
	holder := e.rt.proj.Debits[rec.Lineage]
	e.rt.mu.Unlock()
	if holder != dispatchID {
		t.Fatal("ROW14: debit released by absence")
	}
	must(t, e.rt.Sweep(context.Background()))
	must(t, e.rt.Reconcile(context.Background()))
	wantSequence(t, l, "start", "stop")
}

// Row 15 (m0-launch.md:248-252 refusal and lost replies are not absence;
// :397-399 leader exit, missing receipt and failed inventory are not absence):
// a Stop that errs, that returns without absence, or that returns absence with
// empty or oversized evidence is journaled as unknown; the run stays started
// with its claim and no evidence. Bounded absence beside them is the control.
func TestStopTableRow15NonAbsenceIsUnknown(t *testing.T) {
	for _, path := range []string{"absent-control", "error", "not-absent", "empty-evidence", "oversized-evidence"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t)
			l := traceLauncher(e)
			_, err := e.dispatch(t)
			must(t, err)
			switch path {
			case "absent-control":
				l.receipt = &StopReceipt{Absent: true, Evidence: []byte("evidence")}
			case "error":
				e.launcher.stopErr = errFixture
			case "not-absent":
				l.receipt = &StopReceipt{}
			case "empty-evidence":
				l.receipt = &StopReceipt{Absent: true}
			default:
				l.receipt = &StopReceipt{Absent: true, Evidence: make([]byte, maxEvidenceBytes+1)}
			}
			res, err := e.cancel(t)
			rec := e.record(t, dispatchID)
			wantSequence(t, l, "start", "stop")
			if path == "absent-control" {
				must(t, err)
				if res.State != stopAbsent || rec.Phase != phaseAbsent {
					t.Fatalf("ROW15 control: %+v", rec)
				}
				return
			}
			wantCode(t, err, CodeObservationUnknown)
			if res == nil || res.State != stopUnknown {
				t.Fatalf("ROW15 %s: reply does not report the unknown outcome: %+v", path, res)
			}
			if rec.StopOutcome != stopUnknown || rec.Phase != phaseStarted || len(rec.StopEvidence) != 0 || rec.StopAttempts != 1 || e.store.count(frameStop) != 1 {
				t.Fatalf("ROW15 %s: %+v", path, rec)
			}
		})
	}
}

// Row 17 (m0-launch.md:397-399 observation is not fenced confirmed absence;
// :327 query before retry): every observation class, present, absent,
// contradictory, unknown and a failed query, is journaled as the class (a
// failure as unknown) through Reconcile and through the retry pass; none
// changes the stop outcome, the phase or the evidence, and the retry pass still
// issues the Stop. The receipt that finally confirms absence is the control
// beside them.
func TestStopTableRow17ObservationClassesAreEvidenceOnly(t *testing.T) {
	for _, class := range []string{"present", "absent", "contradictory", "unknown", "failed"} {
		t.Run(class, func(t *testing.T) {
			e := newEnv(t)
			l := traceLauncher(e)
			_, err := e.dispatch(t)
			must(t, err)
			unknownStop(t, e, l)
			want := Observation(class)
			if class == "failed" {
				l.observeErr = errors.New("observe failed")
				want = ObservationUnknown
			} else {
				e.launcher.observe = want
			}
			l.receipt = &StopReceipt{}
			must(t, e.rt.Reconcile(context.Background()))
			afterReconcile := e.record(t, dispatchID)
			if afterReconcile.StopObserved != want || afterReconcile.StopOutcome != stopUnknown || afterReconcile.Phase != phaseStarted || len(afterReconcile.StopEvidence) != 0 {
				t.Fatalf("ROW17 %s: observation changed stop state: %+v", class, afterReconcile)
			}
			must(t, e.rt.Sweep(context.Background()))
			afterSweep := e.record(t, dispatchID)
			wantSequence(t, l, "start", "stop", "observe", "observe", "stop")
			if afterSweep.StopObserved != want || afterSweep.StopOutcome != stopUnknown || afterSweep.Phase != phaseStarted || afterSweep.StopAttempts != 2 || len(afterSweep.StopEvidence) != 0 {
				t.Fatalf("ROW17 %s: retry pass changed state without a receipt: %+v", class, afterSweep)
			}
			l.mu.Lock()
			l.receipt = &StopReceipt{Absent: true, Evidence: []byte("evidence")}
			l.mu.Unlock()
			res, err := e.cancel(t)
			must(t, err)
			if res.State != stopAbsent || e.record(t, dispatchID).Phase != phaseAbsent {
				t.Fatalf("ROW17 %s: receipt not accepted after observations: %+v", class, res)
			}
			wantSequence(t, l, "start", "stop", "observe", "observe", "stop", "observe", "stop")
		})
	}
}

// Class sweep, Launcher.Start (journal.md:172 barrier; m0-launch.md:299-301
// intent before call): the reservation and intent records are materialized in
// the cached projection before the only Start call, and a failed
// materialization returns before it with zero starts and the sync-failure
// barrier. The healthy dispatch is the control.
func TestStartPathBarrierBeforeLaunch(t *testing.T) {
	t.Run("record-at-start-control", func(t *testing.T) {
		e := newEnv(t)
		l := traceLauncher(e)
		var seen *projection
		l.atStart = func(StartRequest) { seen = memProjection(t, e.store) }
		res, err := e.dispatch(t)
		must(t, err)
		rec := seen.Dispatches[dispatchID]
		if rec == nil || rec.Phase != phaseInFlight || rec.EffectID != res.EffectID || rec.ExecutionID == "" {
			t.Fatalf("START-BARRIER: intent not materialized before Start: %+v", rec)
		}
		wantSequence(t, l, "start")
	})
	t.Run("materialization-fails", func(t *testing.T) {
		e := newEnv(t)
		l := traceLauncher(e)
		e.rt.store = &rejectingLedger{ledger: e.rt.store}
		_, err := e.dispatch(t)
		if !MustExit(err) || !e.rt.ExitRequired() {
			t.Fatalf("START-BARRIER: expected the sync-failure barrier, got %v", err)
		}
		wantSequence(t, l)
		if e.store.count(frameIntent) != 1 || e.store.count(frameResult) != 0 {
			t.Fatal("START-BARRIER: intent not retained or a result written without a call")
		}
	})
}

// Class sweep, start-effect Launcher.Observe (journal.md:172 barrier): after
// the process is lost past durable intent and the ledger is replayed into a
// runtime whose record store is empty, Reconcile materializes the recovered
// intent before it queries the launch effect, and the query names that
// intent. A failing materialization beside it returns before the query.
func TestStartObserveBarrierAfterRecovery(t *testing.T) {
	for _, mode := range []string{"record-at-observe-control", "materialization-fails"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			e.launcher.onStart = func() { must(t, e.rt.Close()) }
			_, err := e.dispatch(t)
			wantCode(t, err, CodeStorageFailed)
			e.launcher.onStart = nil
			e.reopen(t)
			l := traceLauncher(e)
			if _, ok := e.store.record(projectionRecord); ok {
				t.Fatal("fixture: the replayed store already holds a record")
			}
			if mode == "materialization-fails" {
				e.rt.store = &rejectingLedger{ledger: e.rt.store}
				err = e.rt.Reconcile(context.Background())
				if !MustExit(err) || !e.rt.ExitRequired() {
					t.Fatalf("OBSERVE-BARRIER: expected the sync-failure barrier, got %v", err)
				}
				wantSequence(t, l)
				return
			}
			var seen *projection
			l.atObserve = func(EffectQuery) { seen = memProjection(t, e.store) }
			must(t, e.rt.Reconcile(context.Background()))
			wantSequence(t, l, "observe")
			rec := e.record(t, dispatchID)
			l.mu.Lock()
			query := l.queries[0]
			l.mu.Unlock()
			if cached := seen.Dispatches[dispatchID]; cached == nil || cached.Phase != phaseInFlight || cached.EffectID != rec.EffectID || query.EffectID != rec.EffectID || query.Operation != operationStart || query.ArgumentsDigest != rec.EntryDigest {
				t.Fatalf("OBSERVE-BARRIER: record %+v query %+v", cached, query)
			}
		})
	}
}
