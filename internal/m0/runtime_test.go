package m0

import (
	"context"
	"strings"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

// Claim (positive control for every negative below): with every seam pinned
// to a synthetic installation, one signed dispatch reserves, journals intent,
// commits C after verified drop, records the start and returns "started" with
// exactly one launcher start; the ledger holds reserve, intent, commit and
// result frames and the lineage debit.
func TestDispatchPositiveControl(t *testing.T) {
	e := newEnv(t)
	res, err := e.dispatch(t)
	must(t, err)
	if res.State != string(phaseStarted) || res.Replayed || res.RequestID != dispatchID || !isID(res.EffectID) {
		t.Fatalf("POSITIVE: %+v", res)
	}
	if starts, stops := e.launcher.count(); starts != 1 || stops != 0 {
		t.Fatalf("POSITIVE: starts=%d stops=%d", starts, stops)
	}
	rec := e.record(t, dispatchID)
	if rec.Phase != phaseStarted || rec.CommitUnix == 0 || rec.EffectID != res.EffectID || !isExecutionID(rec.ExecutionID) || !isDigest(rec.EntryDigest) {
		t.Fatalf("POSITIVE: record %+v", rec)
	}
	for _, ft := range []frameType{frameOpen, framePolicy, frameReserve, frameIntent, frameCommit, frameResult} {
		if e.store.count(ft) != 1 {
			t.Errorf("POSITIVE: %s frames = %d", ft, e.store.count(ft))
		}
	}
	e.rt.mu.Lock()
	holder := e.rt.proj.Debits[rec.Lineage]
	e.rt.mu.Unlock()
	if holder != dispatchID || rec.Lineage != lineageKey(e.policy.ID, e.policy.Worker) {
		t.Fatal("POSITIVE: lineage debit not held by the dispatch")
	}
}

// Claim: a same-ID identical replay returns the original started outcome
// without a second launcher start; a same-ID changed body refuses
// request_id_conflict before any effect; a different request ID is not the
// protected signing plan and refuses before reservation; none adds a record.
func TestReplayConflictAndSecondID(t *testing.T) {
	e := newEnv(t)
	_, err := e.dispatch(t)
	must(t, err)
	again, err := e.dispatch(t)
	must(t, err)
	if !again.Replayed || again.State != string(phaseStarted) {
		t.Fatalf("REPLAY: %+v", again)
	}
	changed := parseDoc(t, e.dispatchDoc.encode(nil))
	setPath(t, changed, "plan.harness", text("sha256:"+strings.Repeat("9", 64)))
	_, err = e.rt.Handle(context.Background(), e.peer, signRequest(t, changed, e.key))
	wantCode(t, err, CodeRequestIDConflict)
	second := parseDoc(t, e.dispatchDoc.encode(nil))
	setPath(t, second, "request_id", text(strings.Repeat("3", 32)))
	_, err = e.rt.Handle(context.Background(), e.peer, signRequest(t, second, e.key))
	wantCode(t, err, CodeRequestInvalid)
	if starts, _ := e.launcher.count(); starts != 1 {
		t.Fatalf("REPLAY: starts=%d", starts)
	}
	e.rt.mu.Lock()
	n := len(e.rt.proj.Dispatches)
	e.rt.mu.Unlock()
	if n != 1 {
		t.Fatalf("REPLAY: %d records", n)
	}
}

// Claim: a replaced policy (new revision, new catalog pinning a new dispatch
// ID, higher install revision) cannot reset the worker's one-start ledger: the
// new ID refuses m0_launch_budget_consumed before reservation, the replacement
// records the original stop obligation, and Sweep issues exactly one stop
// under it. A reinstalled older body refuses as rollback.
func TestReplacedPolicyCannotResetLedger(t *testing.T) {
	e := newEnv(t)
	_, err := e.dispatch(t)
	must(t, err)
	newID := strings.Repeat("3", 32)
	e.source.edit(func(i *Installed) {
		pv := parseDoc(t, i.Policy.Canonical)
		setPath(t, pv, "revision", integer(2))
		p, err := DecodePolicy(pv.encode(nil))
		must(t, err)
		i.Policy = p
		dp := parseDoc(t, i.Catalog.Dispatch.Canonical)
		setPath(t, dp, "request.policy.revision", integer(2))
		setPath(t, dp, "request.request_id", text(newID))
		d, err := DecodeSigningPlan(dp.encode(nil))
		must(t, err)
		cp := parseDoc(t, i.Catalog.Cancel.Canonical)
		setPath(t, cp, "request.policy.revision", integer(2))
		setPath(t, cp, "request.dispatch_request_id", text(newID))
		c, err := DecodeSigningPlan(cp.encode(nil))
		must(t, err)
		i.Catalog = Catalog{Dispatch: d, Cancel: c}
		i.InstallRevision = 2
	})
	doc := parseDoc(t, e.dispatchDoc.encode(nil))
	setPath(t, doc, "policy.revision", integer(2))
	setPath(t, doc, "request_id", text(newID))
	_, err = e.rt.Handle(context.Background(), e.peer, signRequest(t, doc, e.key))
	wantCode(t, err, CodeBudgetConsumed)
	if e.store.count(frameReserve) != 1 || e.store.count(frameRemoval) != 1 {
		t.Fatalf("LEDGER: reserve=%d removal=%d", e.store.count(frameReserve), e.store.count(frameRemoval))
	}
	must(t, e.rt.Sweep(context.Background()))
	must(t, e.rt.Sweep(context.Background()))
	if starts, stops := e.launcher.count(); starts != 1 || stops != 1 {
		t.Fatalf("LEDGER: starts=%d stops=%d", starts, stops)
	}
	if rec := e.record(t, dispatchID); rec.Phase != phaseAbsent || rec.StopSource != "removal" {
		t.Fatalf("LEDGER: %+v", rec)
	}
	// Rollback: the previous revision reinstalled refuses a fresh request ID.
	original := buildInstalled(t, e.key, e.policy.Plan.WorkerPlan)
	original.InstallRevision = 3
	e.source.edit(func(i *Installed) { *i = *original })
	probe := parseDoc(t, e.dispatchDoc.encode(nil))
	setPath(t, probe, "request_id", text(strings.Repeat("4", 32)))
	_, err = e.rt.Handle(context.Background(), e.peer, signRequest(t, probe, e.key))
	wantCode(t, err, CodePolicyRollback)
}

// Claim (receiver interlock A): C is refused m0_start_invalidated when the
// receiver has not verified the privilege drop, when expiry is reached at or
// before C (equality refuses), when the policy is removed before C (Rv), when
// the receiver facts name another generation, and when the policy source or
// clock cannot be read at C. Each refusal consumes the debit: the same-ID
// retry returns the refused outcome with no second start, and a new ID refuses.
// The first case is the accepted control.
func TestCommitGateRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(t *testing.T, e *testEnv)
		ok   bool
	}{
		{"control", func(*testing.T, *testEnv) {}, true},
		{"drop-not-verified", func(_ *testing.T, e *testEnv) { e.launcher.drop = false }, false},
		{"expiry-equality", func(_ *testing.T, e *testEnv) {
			// Expiry is not_after here: reservation plus max_runtime lies beyond it.
			e.launcher.onStart = func() { e.clock.set(e.policy.NotAfter) }
		}, false},
		{"expiry-passed", func(_ *testing.T, e *testEnv) {
			e.launcher.onStart = func() { e.clock.set(e.policy.NotAfter + 1) }
		}, false},
		{"removed-before-c", func(_ *testing.T, e *testEnv) {
			e.launcher.onStart = func() { e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision = 2 }) }
		}, false},
		{"replaced-before-c", func(t *testing.T, e *testEnv) {
			e.launcher.onStart = func() {
				e.source.edit(func(i *Installed) {
					pv := parseDoc(t, i.Policy.Canonical)
					setPath(t, pv, "revision", integer(2))
					p, err := DecodePolicy(pv.encode(nil))
					must(t, err)
					i.Policy = p
					i.InstallRevision = 2
				})
			}
		}, false},
		{"facts-generation", func(_ *testing.T, e *testEnv) {
			e.launcher.factsEdit = func(f *ReceiverFacts) { f.Generation = strings.Repeat("9", 32) }
		}, false},
		{"facts-receiver", func(_ *testing.T, e *testEnv) {
			e.launcher.factsEdit = func(f *ReceiverFacts) { f.Receiver = strings.Repeat("9", 32) }
		}, false},
		{"clock-unknown-at-c", func(_ *testing.T, e *testEnv) {
			e.launcher.onStart = func() { e.clock.mu.Lock(); e.clock.confident = false; e.clock.mu.Unlock() }
		}, false},
		{"generation-unreadable-at-c", func(_ *testing.T, e *testEnv) {
			e.launcher.onStart = func() { e.gens.mu.Lock(); e.gens.err = errFixture; e.gens.mu.Unlock() }
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.arm(t, e)
			res, err := e.dispatch(t)
			if tc.ok {
				must(t, err)
				if res.State != string(phaseStarted) {
					t.Fatalf("GATE: control %+v", res)
				}
				return
			}
			wantCode(t, err, CodeStartInvalidated)
			if res == nil || res.State != string(phaseRefused) {
				t.Fatalf("GATE: %+v", res)
			}
			if e.store.count(frameCommit) != 0 {
				t.Fatal("GATE: commit frame written despite refusal")
			}
			if CodeOf(e.launcher.commitErr) != CodeStartInvalidated {
				t.Fatalf("GATE: receiver saw %v", e.launcher.commitErr)
			}
			// The debit is spent: the same-ID retry replays the refusal before
			// any current gate and issues no second start.
			e.launcher.onStart, e.launcher.factsEdit = nil, nil
			again, err := e.dispatch(t)
			wantCode(t, err, CodeStartInvalidated)
			if again == nil || !again.Replayed {
				t.Fatalf("GATE: retry %+v", again)
			}
			if starts, _ := e.launcher.count(); starts != 1 {
				t.Fatalf("GATE: starts=%d", starts)
			}
			e.rt.mu.Lock()
			_, debit := e.rt.proj.Debits[lineageKey(e.policy.ID, e.policy.Worker)]
			e.rt.mu.Unlock()
			if !debit {
				t.Fatal("GATE: debit rearmed after refusal")
			}
		})
	}
}

// Claim: only the original owner may cancel. A cancel signed by another key
// (self-consistent association) refuses run_not_owned; the owner's body from
// another kernel peer refuses run_not_owned; a cancel naming an unknown
// dispatch refuses run_not_found; none issues a stop. The owner's cancel from
// the original peer issues exactly one stop under the E-stop effect and, with
// receiver-confirmed absence, ends in "absent"; the same-ID retry replays that
// outcome without a second stop, while a new cancel ID (pinned by a replaced
// catalog) against the absent run refuses run_terminal.
func TestCancelOwnership(t *testing.T) {
	e := newEnv(t)
	_, err := e.dispatch(t)
	must(t, err)
	other := newKey(t)
	foreign := parseDoc(t, e.cancelDoc.encode(nil))
	setPath(t, foreign, "association.key", text(other.spelling))
	_, err = e.rt.Handle(context.Background(), e.peer, signRequest(t, foreign, other))
	wantCode(t, err, CodeRunNotOwned)
	_, err = e.rt.Handle(context.Background(), e.peer+1, e.cancelWire)
	wantCode(t, err, CodeRunNotOwned)
	unknown := parseDoc(t, e.cancelDoc.encode(nil))
	setPath(t, unknown, "dispatch_request_id", text(strings.Repeat("7", 32)))
	_, err = e.rt.Handle(context.Background(), e.peer, signRequest(t, unknown, e.key))
	wantCode(t, err, CodeRunNotFound)
	if _, stops := e.launcher.count(); stops != 0 {
		t.Fatalf("OWNER: stops=%d after refusals", stops)
	}
	res, err := e.cancel(t)
	must(t, err)
	if res.State != stopAbsent || res.RequestID != cancelID || res.EffectID != effectID(e.policy.Association.ID+":"+e.policy.Association.Generation, dispatchID, 1, componentLaunch, operationStop, 1) {
		t.Fatalf("OWNER: %+v", res)
	}
	if _, stops := e.launcher.count(); stops != 1 || e.launcher.lastStop.Ceiling == 0 {
		t.Fatalf("OWNER: stops=%d ceiling=%d", stops, e.launcher.lastStop.Ceiling)
	}
	again, err := e.cancel(t)
	must(t, err)
	if !again.Replayed || again.State != stopAbsent {
		t.Fatalf("OWNER: replay %+v", again)
	}
	if _, stops := e.launcher.count(); stops != 1 {
		t.Fatal("OWNER: replay issued a second stop")
	}
	if rec := e.record(t, dispatchID); rec.Phase != phaseAbsent {
		t.Fatalf("OWNER: %+v", rec)
	}
	// A new cancel ID, admitted by a catalog that now pins it, is not a retry.
	newCancel := strings.Repeat("8", 32)
	e.source.edit(func(i *Installed) {
		cp := parseDoc(t, i.Catalog.Cancel.Canonical)
		setPath(t, cp, "request.request_id", text(newCancel))
		c, err := DecodeSigningPlan(cp.encode(nil))
		must(t, err)
		i.Catalog.Cancel = c
	})
	fresh := parseDoc(t, e.cancelDoc.encode(nil))
	setPath(t, fresh, "request_id", text(newCancel))
	_, err = e.rt.Handle(context.Background(), e.peer, signRequest(t, fresh, e.key))
	wantCode(t, err, CodeRunTerminal)
	if _, stops := e.launcher.count(); stops != 1 {
		t.Fatal("OWNER: terminal refusal issued a stop")
	}
	_, err = e.dispatch(t)
	must(t, err) // the original outcome is replayed; absent is terminal
}

// Claim: a cancel that races the start, arriving after intent and before C,
// records the stop intent so the gate refuses C: no commit frame, the start is
// refused m0_start_invalidated, the stop is issued once under the original
// effect, and an unknown stop outcome keeps the claim and stop debt. A cancel
// of a reservation that never reached intent needs no stop.
func TestCancelRacingStart(t *testing.T) {
	e := newEnv(t)
	e.launcher.stopAbsent = false
	var cancelErr error
	e.launcher.onStart = func() {
		_, cancelErr = e.rt.Handle(context.Background(), e.peer, e.cancelWire)
	}
	res, err := e.dispatch(t)
	wantCode(t, err, CodeStartInvalidated)
	wantCode(t, cancelErr, CodeObservationUnknown)
	if res.State != string(phaseRefused) || e.store.count(frameCommit) != 0 {
		t.Fatalf("RACE: %+v commits=%d", res, e.store.count(frameCommit))
	}
	rec := e.record(t, dispatchID)
	if !rec.CancelRequested || rec.StopOutcome != stopUnknown || rec.StopEffectID == "" {
		t.Fatalf("RACE: %+v", rec)
	}
	if starts, stops := e.launcher.count(); starts != 1 || stops != 1 {
		t.Fatalf("RACE: starts=%d stops=%d", starts, stops)
	}
	e.rt.mu.Lock()
	_, debit := e.rt.proj.Debits[rec.Lineage]
	e.rt.mu.Unlock()
	if !debit {
		t.Fatal("RACE: debit released")
	}
}

// Claim: a journal sync failure latches the runtime: the failing dispatch
// refuses m0_storage_failed carrying the exit sentinel, every later request
// refuses without touching any seam, and a same-boot established reopen
// without the attestation refuses m0_recovery_held. The control runtime with
// a healthy store has no exit duty.
func TestJournalSyncFailureLatchesAndHoldsRecovery(t *testing.T) {
	control := newEnv(t)
	_, err := control.dispatch(t)
	must(t, err)
	if control.rt.ExitRequired() {
		t.Fatal("SYNC: control requires exit")
	}
	e := newEnv(t)
	e.store.failAppendAt = e.store.appendCalls + 2 // the reserve frame, after the policy frame
	e.store.failErr = syncFailure()
	_, err = e.dispatch(t)
	wantCode(t, err, CodeStorageFailed)
	if !MustExit(err) || !e.rt.ExitRequired() {
		t.Fatalf("SYNC: exit duty missing: %v", err)
	}
	if starts, _ := e.launcher.count(); starts != 0 {
		t.Fatal("SYNC: launch after storage failure")
	}
	// A failing clock would otherwise be the first refusal: the latch wins
	// without consulting any seam.
	e.clock.mu.Lock()
	e.clock.err = errFixture
	e.clock.mu.Unlock()
	_, err = e.dispatch(t)
	wantCode(t, err, CodeStorageFailed)
	_, err = e.cancel(t)
	wantCode(t, err, CodeStorageFailed)
	umask := uint32(0077)
	_, err = OpenEstablished(journal.Config{Directory: "/nonexistent", Records: "records", Inputs: "inputs", DeclaredUmask: &umask, SupportedLocalStorage: true, ConsumerBudget: 1 << 20}, DefaultSeams())
	wantCode(t, err, CodeRecoveryHeld)
}

// Claim: every unpinned seam refuses by its own name before any reservation
// or effect, except the launcher, whose refusal during the only call is a lost
// reply: the original record latches UNKNOWN, the debit stays spent and no
// retry launches. The pinned environment is the accepted control.
func TestUnpinnedSeamsRefuseByName(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(s *Seams)
		seam Seam
	}{
		{"policy", func(s *Seams) { s.Policy = nil }, SeamPolicySource},
		{"clock", func(s *Seams) { s.Clock = nil }, SeamClock},
		{"generation", func(s *Seams) { s.Generation = nil }, SeamGeneration},
		{"entry", func(s *Seams) { s.Entry = nil }, SeamEntry},
		{"runner", func(s *Seams) { s.Runner = nil }, SeamRunner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			seams := e.seams()
			tc.arm(&seams)
			rt, err := newRuntime(newMemLedger(), seams)
			must(t, err)
			e.rt = rt
			_, err = e.dispatch(t)
			wantCode(t, err, CodeSeamUnpinned)
			if !strings.Contains(err.Error(), string(tc.seam)) {
				t.Fatalf("SEAM: refusal does not name the seam: %v", err)
			}
			rt.mu.Lock()
			n := len(rt.proj.Dispatches)
			rt.mu.Unlock()
			if n != 0 {
				t.Fatal("SEAM: reservation despite unpinned seam")
			}
		})
	}
	e := newEnv(t)
	seams := e.seams()
	seams.Launcher = nil
	rt, err := newRuntime(newMemLedger(), seams)
	must(t, err)
	e.rt = rt
	res, err := e.dispatch(t)
	wantCode(t, err, CodeObservationUnknown)
	if res.State != string(phaseUnknown) {
		t.Fatalf("SEAM: launcher %+v", res)
	}
	again, err := e.dispatch(t)
	wantCode(t, err, CodeObservationUnknown)
	if !again.Replayed {
		t.Fatal("SEAM: retry did not replay the unknown original")
	}
	// With every seam at its default the first refusal names the policy
	// source: the activation gate is reported before the clock.
	rt2, err := newRuntime(newMemLedger(), DefaultSeams())
	must(t, err)
	_, err = rt2.Handle(context.Background(), 30001, e.dispatchWire)
	wantCode(t, err, CodeSeamUnpinned)
	if !strings.Contains(err.Error(), string(SeamPolicySource)) {
		t.Fatalf("SEAM: default seams refusal: %v", err)
	}
	_, err = rt2.Handle(context.Background(), 30001, e.cancelWire)
	wantCode(t, err, CodeSeamUnpinned)
}

// Claim (replay after crash): when the process is lost after durable intent,
// recovery sees an in-flight record and latches UNKNOWN: the same-ID replay
// returns observation_unknown without a start, a new ID refuses, Reconcile
// records the receiver observation class without starting anything, and the
// unknown effect never becomes absence. Recovery before intent (reservation
// only) continues the original transaction instead, issuing exactly one start.
func TestReplayAfterCrash(t *testing.T) {
	e := newEnv(t)
	e.launcher.onStart = func() { must(t, e.rt.Close()) }
	_, err := e.dispatch(t)
	wantCode(t, err, CodeStorageFailed)
	if e.store.count(frameIntent) != 1 || e.store.count(frameResult) != 0 {
		t.Fatal("CRASH: intent not durable or result written after loss")
	}
	e.launcher.onStart = nil
	rt := e.reopen(t)
	rec := e.record(t, dispatchID)
	if rec.Phase != phaseInFlight {
		t.Fatalf("CRASH: %+v", rec)
	}
	res, err := e.dispatch(t)
	wantCode(t, err, CodeObservationUnknown)
	if !res.Replayed {
		t.Fatal("CRASH: replay started again")
	}
	second := parseDoc(t, e.dispatchDoc.encode(nil))
	setPath(t, second, "request_id", text(strings.Repeat("3", 32)))
	_, err = rt.Handle(context.Background(), e.peer, signRequest(t, second, e.key))
	wantCode(t, err, CodeRequestInvalid)
	e.launcher.observe = ObservationUnknown
	must(t, rt.Reconcile(context.Background()))
	if e.store.count(frameObserve) != 1 || e.record(t, dispatchID).Observed != ObservationUnknown {
		t.Fatal("CRASH: observation not recorded")
	}
	if starts, _ := e.launcher.count(); starts != 1 {
		t.Fatalf("CRASH: starts=%d", starts)
	}
	// Reservation-only loss: the same ID recovers the original transaction.
	e2 := newEnv(t)
	e2.store.failAppendAt = e2.store.appendCalls + 3 // policy, reserve, then intent fails
	e2.store.failErr = &journal.Error{Class: journal.StorageWriteFailure, Op: "pwrite", Poisoned: true}
	_, err = e2.dispatch(t)
	wantCode(t, err, CodeStorageFailed)
	if e2.store.count(frameReserve) != 1 || e2.store.count(frameIntent) != 0 {
		t.Fatal("CRASH: reservation-only state not reproduced")
	}
	e2.reopen(t)
	res, err = e2.dispatch(t)
	must(t, err)
	if res.State != string(phaseStarted) || res.Replayed {
		t.Fatalf("CRASH: recovery %+v", res)
	}
	if starts, _ := e2.launcher.count(); starts != 1 || e2.store.count(frameReserve) != 1 {
		t.Fatalf("CRASH: starts=%d reserves=%d", starts, e2.store.count(frameReserve))
	}
}

// Claim (late refusal): a committed start whose handoff S lands outside the
// qualified C-to-exec interval is refused m0_late_start_refused rather than
// reported as success; the record keeps C, the spent debit and the receipt,
// and its replay returns the same refusal. A handoff inside the interval is
// the accepted control.
func TestLateStartRefused(t *testing.T) {
	control := newEnv(t)
	control.launcher.execDelay = 5
	res, err := control.dispatch(t)
	must(t, err)
	if res.State != string(phaseStarted) {
		t.Fatalf("LATE: control %+v", res)
	}
	e := newEnv(t)
	e.launcher.execDelay = 6
	res, err = e.dispatch(t)
	wantCode(t, err, CodeLateStart)
	rec := e.record(t, dispatchID)
	if res.State != string(phaseLate) || rec.CommitUnix == 0 || string(rec.Receipt) != "receipt" {
		t.Fatalf("LATE: %+v %+v", res, rec)
	}
	again, err := e.dispatch(t)
	wantCode(t, err, CodeLateStart)
	if !again.Replayed {
		t.Fatal("LATE: replay restarted")
	}
	if starts, _ := e.launcher.count(); starts != 1 {
		t.Fatal("LATE: second start")
	}
}

// Claim: planned expiry closes the started run through Sweep: once the
// qualified clock reaches the original expiry, a stop intent sourced "expiry"
// is journaled with the original expiry as its event and one stop is issued;
// before expiry Sweep issues nothing.
func TestSweepStopsAtExpiry(t *testing.T) {
	e := newEnv(t)
	_, err := e.dispatch(t)
	must(t, err)
	must(t, e.rt.Sweep(context.Background()))
	if _, stops := e.launcher.count(); stops != 0 {
		t.Fatal("EXPIRY: stop before expiry")
	}
	rec := e.record(t, dispatchID)
	e.clock.set(rec.Deadlines.Expiry)
	must(t, e.rt.Sweep(context.Background()))
	rec = e.record(t, dispatchID)
	if _, stops := e.launcher.count(); stops != 1 || rec.StopSource != "expiry" || rec.StopEvent != rec.Deadlines.Expiry || rec.Phase != phaseAbsent {
		t.Fatalf("EXPIRY: stops=%d %+v", stops, rec)
	}
}

// Claim: the policy ordering rules refuse, before any reservation, a reused
// revision with a different body (m0_policy_revision_reused), an installation
// revision that goes backwards (m0_policy_rollback), a tombstone without an
// installation revision advance, and a removed policy for forward work
// (m0_policy_removed) while owned cancel still proceeds. An unchanged
// installation is recorded exactly once and is the accepted control.
func TestPolicyOrdering(t *testing.T) {
	e := newEnv(t)
	_, err := e.dispatch(t)
	must(t, err)
	_, err = e.dispatch(t)
	must(t, err)
	if e.store.count(framePolicy) != 1 {
		t.Fatal("ORDER: policy frame not recorded exactly once")
	}
	probe := func(id string) error {
		doc := parseDoc(t, e.dispatchDoc.encode(nil))
		setPath(t, doc, "request_id", text(strings.Repeat(id, 32)))
		_, err := e.rt.Handle(context.Background(), e.peer, signRequest(t, doc, e.key))
		return err
	}
	before := e.source.inst
	e.source.edit(func(i *Installed) {
		pv := parseDoc(t, i.Policy.Canonical)
		setPath(t, pv, "qualification", text("sha256:"+strings.Repeat("7", 64)))
		p, err := DecodePolicy(pv.encode(nil))
		must(t, err)
		i.Policy = p
	})
	wantCode(t, probe("5"), CodePolicyRevisionReused)
	e.source.edit(func(i *Installed) { *i = *before; i.InstallRevision = 0 })
	wantCode(t, probe("5"), CodePolicyRollback)
	e.source.edit(func(i *Installed) { *i = *before; i.Removed = true })
	wantCode(t, probe("5"), CodePolicyRevisionReused)
	e.source.edit(func(i *Installed) { *i = *before; i.Removed = true; i.InstallRevision = 2 })
	wantCode(t, probe("5"), CodePolicyRemoved)
	if e.store.count(frameReserve) != 1 {
		t.Fatal("ORDER: a refused probe reserved")
	}
	// Owned cancel after removal uses the retained original consent.
	res, err := e.cancel(t)
	must(t, err)
	if res.State != stopAbsent {
		t.Fatalf("ORDER: cancel after removal %+v", res)
	}
}
