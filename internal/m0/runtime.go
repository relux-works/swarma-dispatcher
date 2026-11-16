package m0

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	goruntime "runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

const (
	effectDomain    = "swarma-dispatcher effect/1"
	executionDomain = "swarma-dispatcher execution/1"
	componentLaunch = "launcher"
	operationStart  = "launch.start"
	operationStop   = "exec.stop"
	lineageRecordID = 1
)

// ledger is the durable store contract satisfied by *journal.Session. Only the
// journal is used in production; tests may substitute a fault-injecting store.
type ledger interface {
	Append([]byte) (uint64, error)
	ReplaceRecord(string, []byte) error
	State() journal.State
	Close() error
}

// Result is the outcome returned for one request, original or replayed.
type Result struct {
	RequestID string
	Op        Op
	State     string
	EffectID  string
	Replayed  bool
}

// Runtime is the M0 ledger and policy evaluator. The mutex is the
// state-transition lock: it covers local journal and projection I/O only and
// is never held across a seam call.
//
// Three process-local facts refine the durable state and never survive a
// reopen: materialized reports that the cached projection record equals the
// applied frames (the barrier of journal.md «materialize before a dependent
// external call»); permits holds the call permit of each run's stop
// transaction (granted when this process appended the run's first stop
// intent, or journaled a stop observation while no stop pass owned the run;
// consumed by the Stop call); inflight marks the runs whose stop pass is
// between its decision and its outcome frame.
type Runtime struct {
	mu           sync.Mutex
	seams        Seams
	store        ledger
	proj         *projection
	closed       bool
	latched      error
	exit         atomic.Bool
	materialized bool
	permits      map[string]bool
	inflight     map[string]chan struct{}
}

func platformSupported() error {
	if goruntime.GOOS != "linux" {
		return refuse(CodeUnsupportedPlatform, "M0 is Linux only")
	}
	return nil
}

// Initialize creates a fresh ledger whose genuine first transition is this
// daemon instance's open frame (epoch 1). Installation-established fresh
// state only; never an automatic response to lost history.
func Initialize(cfg journal.Config, seams Seams) (*Runtime, error) {
	if err := platformSupported(); err != nil {
		return nil, err
	}
	open := &frame{Type: frameOpen, Epoch: 1}
	first, err := encodeFrame(open)
	if err != nil {
		return nil, err
	}
	s, err := journal.Initialize(cfg, first)
	if err != nil {
		return nil, storageRefusal(err)
	}
	r := &Runtime{seams: seams.complete(), store: s, proj: newProjection()}
	if err := r.proj.apply(open); err != nil {
		_ = s.Close()
		return nil, err
	}
	return r, nil
}

// OpenEstablished replays the ledger through established recovery, then
// appends a new daemon epoch. Without the recovery-prerequisite attestation it
// refuses m0_recovery_held: same-boot reopening after a sync failure is the
// caller's barrier, never inferred from bytes.
func OpenEstablished(cfg journal.Config, seams Seams) (*Runtime, error) {
	if !cfg.RecoveryPrerequisiteEstablished {
		return nil, refuse(CodeRecoveryHeld, "recovery prerequisite not established")
	}
	if err := platformSupported(); err != nil {
		return nil, err
	}
	c := &consumer{proj: newProjection()}
	s, err := journal.OpenEstablished(cfg, c)
	if err != nil {
		return nil, storageRefusal(err)
	}
	r := &Runtime{seams: seams.complete(), store: s, proj: c.proj}
	r.mu.Lock()
	err = r.appendLocked(&frame{Type: frameOpen, Epoch: c.proj.Epoch + 1})
	r.mu.Unlock()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return r, nil
}

// newRuntime wires a runtime over an already usable ledger. Tests use it with
// fault-injecting stores; production goes through Initialize/OpenEstablished.
func newRuntime(store ledger, seams Seams) (*Runtime, error) {
	r := &Runtime{seams: seams.complete(), store: store, proj: newProjection()}
	r.mu.Lock()
	err := r.appendLocked(&frame{Type: frameOpen, Epoch: 1})
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return r, nil
}

// MustExit reports whether err carries the journal sync-failure sentinel: the
// owning process must exit and must not reopen the ledger in this boot.
func MustExit(err error) bool { return journal.MustExit(err) }

// ExitRequired reports whether this runtime has observed a sync failure.
func (r *Runtime) ExitRequired() bool { return r.exit.Load() }

// Close ends the ledger session. Later calls refuse.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if err := r.store.Close(); err != nil {
		return r.storageFailureLocked(err)
	}
	return nil
}

func storageRefusal(err error) *Refusal {
	switch journal.ClassOf(err) {
	case journal.JournalFull:
		return &Refusal{Code: CodeReserveExhausted, Detail: "journal full", Cause: err}
	case journal.PayloadInvalid:
		return &Refusal{Code: CodeReserveExhausted, Detail: "payload bound", Cause: err}
	}
	return &Refusal{Code: CodeStorageFailed, Detail: "journal: " + string(journal.ClassOf(err)), Cause: err}
}

// storageFailureLocked latches every non-clean journal failure: the session
// has ended, nothing further is appended, and a sync failure additionally
// requires process exit.
func (r *Runtime) storageFailureLocked(err error) error {
	ref := storageRefusal(err)
	if ref.Code == CodeReserveExhausted {
		return ref
	}
	if r.latched == nil {
		r.latched = ref
	}
	if journal.MustExit(err) {
		r.exit.Store(true)
	}
	return ref
}

func (r *Runtime) usableLocked() error {
	if r.latched != nil {
		return r.latched
	}
	if r.closed {
		return refuse(CodeStorageFailed, "runtime closed")
	}
	return nil
}

// appendLocked journals one frame durably, then reduces it. An inconsistency
// after a durable append closes the runtime: the ledger is not repaired.
//
// Every appended frame leaves the cached projection record behind the applied
// frames, whatever the frame reduced to (a cancel, a removal promoted into
// stop intents, an expiry noticed while reducing another frame, an explicit
// expiry intent): the next external call must pass barrierLocked first.
//
// A frame appended by this process that gives a run its first stop intent
// grants that run's call permit (rule a). Replay reduces frames through the
// consumer and never through this function, so a replayed intent grants
// nothing.
func (r *Runtime) appendLocked(f *frame) error {
	b, err := encodeFrame(f)
	if err != nil {
		return err
	}
	if _, err := r.store.Append(b); err != nil {
		return r.storageFailureLocked(err)
	}
	var withoutIntent []string
	for id, rec := range r.proj.Dispatches {
		if rec.StopEffectID == "" {
			withoutIntent = append(withoutIntent, id)
		}
	}
	if err := r.proj.apply(f); err != nil {
		r.latched = err
		return err
	}
	r.materialized = false
	for _, id := range withoutIntent {
		if r.proj.Dispatches[id].StopEffectID != "" {
			r.grantPermitLocked(id)
		}
	}
	return nil
}

// materializeLocked writes the bounded projection record, as the journal
// contract requires before a dependent external call and after a transition
// is published.
func (r *Runtime) materializeLocked() error {
	b, err := r.proj.marshal()
	if err != nil {
		r.latched = err
		return err
	}
	if len(b) > maxProjectionBytes {
		r.latched = refuse(CodeReserveExhausted, "projection exceeds the durable reserve")
		return r.latched
	}
	if err := r.store.ReplaceRecord(projectionRecord, b); err != nil {
		return r.storageFailureLocked(err)
	}
	r.materialized = true
	return nil
}

// barrierLocked is the durable-intent barrier of every external call
// (journal.md: «Before a dependent external call, materialize journaled
// records»). It materializes the projection whenever any frame was applied
// since the last materialization, so no call site needs to know which
// reduction created the records the call depends on. A failed barrier
// returns before the call with the journal unchanged.
func (r *Runtime) barrierLocked() error {
	if r.materialized {
		return nil
	}
	return r.materializeLocked()
}

// grantPermitLocked records a process-local call permit for one run's stop
// transaction. Permits are never persisted and never derived from replay.
func (r *Runtime) grantPermitLocked(requestID string) {
	if r.permits == nil {
		r.permits = map[string]bool{}
	}
	r.permits[requestID] = true
}

// claimStopPassLocked marks one run's stop pass as in flight, from its decision
// to its outcome frame, and returns the channel closed when it ends. A second
// pass for the same run waits on it instead of issuing a parallel call.
func (r *Runtime) claimStopPassLocked(requestID string) chan struct{} {
	if r.inflight == nil {
		r.inflight = map[string]chan struct{}{}
	}
	done := make(chan struct{})
	r.inflight[requestID] = done
	return done
}

func (r *Runtime) releaseStopPass(requestID string, done chan struct{}) {
	r.mu.Lock()
	delete(r.inflight, requestID)
	r.mu.Unlock()
	close(done)
}

// reserveFitsLocked refuses forward work when the durable reserve cannot
// cover the whole transaction (reservation, intent, commit, result, cancel and
// stop frames plus the cached projection).
func (r *Runtime) reserveFitsLocked() error {
	b, err := r.proj.marshal()
	if err != nil {
		return err
	}
	// Half the reserve stays free for the remaining frames of the transaction
	// and the recovery projection; the exact accounting is a gate (SC-Q6).
	if len(b) > maxProjectionBytes/2 {
		return refuse(CodeReserveExhausted, "durable reserve cannot cover the transaction")
	}
	return nil
}

func lineageKey(policyID string, w Worker) string {
	return policyID + "/" + strconv.FormatUint(uint64(w.UID), 10) + "/" + w.Generation
}

// effectID derives the dispatcher-owned idempotency key of one downstream
// call: the first 32 hex characters of SHA-256 over the effect domain and
// CCJ-1 of the six identifying members, with no separator (dispatcher 4.5).
// The member names are this runtime's draft spelling.
func effectID(principal, requestID string, recordID uint64, component, operation string, attempt uint64) string {
	body := object(
		member{"attempt", integer(int64(attempt))},
		member{"component", text(component)},
		member{"operation", text(operation)},
		member{"principal", text(principal)},
		member{"record_id", integer(int64(recordID))},
		member{"request_id", text(requestID)},
	).encode(nil)
	sum := sha256.Sum256(append([]byte(effectDomain), body...))
	return hex.EncodeToString(sum[:16])
}

// executionID derives the draft execution claim identity from the launch
// effect. Its exact construction is not frozen by the specification.
func executionID(effect string) string {
	sum := sha256.Sum256([]byte(executionDomain + effect))
	return executionPrefix + hex.EncodeToString(sum[:16])
}

// Handle decodes one signed wire request from the kernel peer and routes it.
func (r *Runtime) Handle(ctx context.Context, peer uint32, wire []byte) (*Result, error) {
	req, err := DecodeRequest(wire)
	if err != nil {
		return nil, err
	}
	switch req.Op {
	case OpDispatch:
		return r.dispatch(ctx, peer, req)
	case OpCancel:
		return r.cancel(ctx, peer, req)
	}
	return nil, invalid("unsupported operation")
}

// currentInstallation reads the protected policy source outside the lock,
// then records and checks the installation ordering under it: a lower install
// revision or policy revision is rollback, the same revision with a different
// body is a reused revision, and a removed or replaced policy records the
// retained stop obligation once. With forward set, a removed policy refuses.
func (r *Runtime) currentInstallation(now Reading, forward bool) (*Installed, error) {
	inst, err := r.seams.Policy.Current()
	if err != nil {
		return nil, asRefusal(err, CodePolicyNotInstalled, "policy source failed")
	}
	if err := inst.validate(); err != nil {
		return nil, err
	}
	seen := policySeen{ID: inst.Policy.ID, Revision: inst.Policy.Revision, Digest: inst.Policy.Digest, InstallRevision: inst.InstallRevision, Removed: inst.Removed}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usableLocked(); err != nil {
		return nil, err
	}
	if prev := r.proj.Policy; prev != nil {
		if seen.InstallRevision < prev.InstallRevision {
			return nil, refuse(CodePolicyRollback, "installation revision went backwards")
		}
		if seen.ID == prev.ID {
			if seen.Revision < prev.Revision {
				return nil, refuse(CodePolicyRollback, "policy revision went backwards")
			}
			if seen.Revision == prev.Revision && seen.Digest != prev.Digest {
				return nil, refuse(CodePolicyRevisionReused, "same policy revision with a different body")
			}
			if seen.Revision == prev.Revision && seen.InstallRevision == prev.InstallRevision && seen.Removed != prev.Removed {
				return nil, refuse(CodePolicyRevisionReused, "tombstone without an installation revision advance")
			}
		}
		if seen != *prev {
			if err := r.appendLocked(&frame{Type: framePolicy, Now: timeOrZero(now), Policy: &seen}); err != nil {
				return nil, err
			}
		}
	} else if err := r.appendLocked(&frame{Type: framePolicy, Now: timeOrZero(now), Policy: &seen}); err != nil {
		return nil, err
	}
	// Removal or replacement invalidates every reserved lineage: record the
	// original removal intent once, with the retained stop obligation.
	if !r.proj.RemovalRecorded {
		current := PolicyPin{ID: inst.Policy.ID, Revision: inst.Policy.Revision}
		for _, rec := range r.proj.Dispatches {
			if inst.Removed || rec.Policy != current {
				reason := "replaced"
				if inst.Removed {
					reason = "removed"
				}
				if err := r.appendLocked(&frame{Type: frameRemoval, Now: timeOrZero(now), Removal: &removalFrame{Event: timeOrZero(now), Detection: timeOrZero(now), Reason: reason}}); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	if inst.Removed && forward {
		return nil, refuse(CodePolicyRemoved, "policy removed; admission closed")
	}
	return inst, nil
}

func timeOrZero(now Reading) int64 {
	if now.Confident {
		return now.Unix
	}
	return 0
}

func (r *Runtime) epoch() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proj.Epoch
}

// outcomeOf maps a record to the caller-visible result and refusal.
func outcomeOf(rec *dispatchRecord, replayed bool) (*Result, error) {
	res := &Result{RequestID: rec.RequestID, Op: OpDispatch, State: string(rec.Phase), EffectID: rec.EffectID, Replayed: replayed}
	switch rec.Phase {
	case phaseStarted, phaseAbsent, phaseCancelled:
		return res, nil
	case phaseRefused:
		return res, refuse(CodeStartInvalidated, rec.Detail)
	case phaseLate:
		return res, refuse(CodeLateStart, rec.Detail)
	}
	return res, refuse(CodeObservationUnknown, "original effect unresolved; no second launch")
}

func (r *Runtime) dispatch(ctx context.Context, peer uint32, req *Request) (*Result, error) {
	principal := req.Principal()
	// Replay: an identical body recovers only the original record. A changed
	// body is a conflict before any new effect.
	r.mu.Lock()
	existing := r.snapshotLocked(req.RequestID)
	err := r.usableLocked()
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Principal != principal || !bytes.Equal(existing.Canonical, req.Canonical) {
			return nil, refuse(CodeRequestIDConflict, "request body differs from the remembered original")
		}
		if err := req.Verify(); err != nil {
			return nil, err
		}
		if peer != existing.PeerUID {
			return nil, refuse(CodeCallerNotPermitted, "kernel peer differs from the original caller")
		}
		if existing.Phase != phaseReserved {
			return outcomeOf(existing, true)
		}
	}
	// The activation gate (installed policy) is checked before the clock so a
	// closed installation is reported as such; unknown time then closes
	// forward work.
	now, clockErr := r.seams.Clock.Now()
	if clockErr != nil || !now.Confident {
		now = Reading{}
	}
	inst, err := r.currentInstallation(now, true)
	if err != nil {
		return nil, err
	}
	if clockErr != nil {
		return nil, asRefusal(clockErr, CodeTimeUnknown, "clock failed")
	}
	if !now.Confident {
		return nil, refuse(CodeTimeUnknown, "time confidence not established")
	}
	p := inst.Policy
	// Prepare: every protected equality before any reservation or effect.
	if req.Association != p.Association {
		return nil, refuse(CodeCallerNotPermitted, "association differs from the protected association")
	}
	if peer != p.Association.UID {
		return nil, refuse(CodeCallerNotPermitted, "kernel peer is not the association UID")
	}
	if req.Receiver != p.Receiver {
		return nil, refuse(CodePolicyMismatch, "receiver differs from the protected policy")
	}
	if req.Policy != (PolicyPin{ID: p.ID, Revision: p.Revision}) {
		return nil, refuse(CodePolicyMismatch, "policy pin differs from the installed revision")
	}
	if req.Worker != p.Worker {
		return nil, refuse(CodePolicyMismatch, "worker tuple differs from the protected policy")
	}
	if req.Plan != p.Plan {
		return nil, refuse(CodePolicyMismatch, "plan pins differ from the protected policy")
	}
	if err := req.Verify(); err != nil {
		return nil, err
	}
	if !bytes.Equal(req.Canonical, inst.Catalog.Dispatch.Request.Canonical) {
		return nil, invalid("request is not the protected dispatch signing plan")
	}
	if err := r.generationCurrent(req.Association.UID, req.Association.Generation, "caller"); err != nil {
		return nil, err
	}
	if err := r.generationCurrent(req.Worker.UID, req.Worker.Generation, "worker"); err != nil {
		return nil, err
	}
	watermark := r.watermark()
	if !windowOpen(p, now.Unix, watermark) {
		return nil, refuse(CodePolicyWindow, "outside the policy validity window")
	}
	deadlines, err := originalDeadlines(p, effectiveNow(now.Unix, watermark), inst.Qualified)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		deadlines = existing.Deadlines
	}
	lineage := lineageKey(p.ID, req.Worker)
	effect := effectID(principal, req.RequestID, lineageRecordID, componentLaunch, operationStart, 1)
	exec := executionID(effect)
	epoch := r.epoch()
	unsigned := *req
	unsigned.Auth = Auth{}
	entry, err := r.seams.Entry.Compose(&unsigned, Execution{EffectID: effect, ExecutionID: exec, ControllerEpoch: epoch}, Timing{DeadlineMs: deadlines.DeadlineMs, SlackMs: deadlines.SlackMs})
	if err != nil {
		return nil, asRefusal(err, CodeSeamUnpinned, "entry composer failed")
	}
	if entry == nil {
		return nil, Unpinned(SeamEntry)
	}
	// The composer selects only the input body and pins; identity and timing
	// are the dispatcher's.
	entry.Request = &unsigned
	entry.Execution = Execution{EffectID: effect, ExecutionID: exec, ControllerEpoch: epoch}
	entry.Timing = Timing{DeadlineMs: deadlines.DeadlineMs, SlackMs: deadlines.SlackMs}
	if entry.Catalog != p.Catalog || entry.Qualification != p.Qualification {
		return nil, refuse(CodePolicyMismatch, "entry catalog or qualification pin differs from the policy")
	}
	entryBytes, entryDigest, err := entry.Canonical()
	if err != nil {
		return nil, err
	}
	if _, err := r.seams.Runner.Argv(entry, now.Unix*1000); err != nil {
		return nil, asRefusal(err, CodeSeamUnpinned, "runner mapping failed")
	}
	// Reservation and intent under the state lock; no seam call is made here.
	r.mu.Lock()
	if err := r.usableLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	rec := r.proj.Dispatches[req.RequestID]
	if rec == nil {
		if len(r.proj.Dispatches) > 0 {
			r.mu.Unlock()
			return nil, refuse(CodeBudgetConsumed, "the single launch budget is already reserved")
		}
		if _, held := r.proj.Debits[lineage]; held {
			r.mu.Unlock()
			return nil, refuse(CodeBudgetConsumed, "the lineage debit is already held")
		}
		if err := r.reserveFitsLocked(); err != nil {
			r.mu.Unlock()
			return nil, err
		}
		if err := r.appendLocked(&frame{Type: frameReserve, Now: now.Unix, Reserve: &reserveFrame{
			RequestID: req.RequestID, Principal: principal, PeerUID: peer, Canonical: string(req.Canonical),
			Association: req.Association, Worker: req.Worker, Policy: req.Policy, Receiver: req.Receiver, Plan: req.Plan,
			Lineage: lineage, Deadlines: deadlines,
		}}); err != nil {
			r.mu.Unlock()
			return nil, err
		}
		rec = r.proj.Dispatches[req.RequestID]
	}
	if rec.Phase != phaseReserved {
		res, err := outcomeOf(rec, true)
		r.mu.Unlock()
		return res, err
	}
	if rec.CancelRequested {
		res, err := outcomeOf(rec, true)
		r.mu.Unlock()
		return res, err
	}
	if err := r.appendLocked(&frame{Type: frameIntent, Now: now.Unix, Intent: &intentFrame{RequestID: req.RequestID, EffectID: effect, ExecutionID: exec, EntryDigest: entryDigest, Attempt: 1}}); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	// The reservation and intent records the launch depends on are
	// materialized here, under the same lock hold, before the only Start call.
	if err := r.barrierLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	start := StartRequest{EffectID: effect, ExecutionID: exec, ControllerEpoch: epoch, Worker: req.Worker, ExecProfile: req.Plan.ExecProfile, Entry: entryBytes, EntryDigest: entryDigest, Deadlines: deadlines}
	policy := PolicyPin{ID: p.ID, Revision: p.Revision}
	r.mu.Unlock()
	// The only physical launch call, outside the state lock. The clock is a
	// seam too and is read before the lock is retaken.
	receipt, callErr := r.seams.Launcher.Start(ctx, start, &gate{r: r, requestID: req.RequestID, policy: policy})
	after, afterErr := r.seams.Clock.Now()
	if afterErr != nil || !after.Confident {
		after = Reading{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usableLocked(); err != nil {
		return nil, err
	}
	rec = r.proj.Dispatches[req.RequestID]
	if rec == nil {
		return nil, inconsistent("reservation vanished during the launch call")
	}
	if rec.Phase != phaseInFlight && rec.Phase != phaseCommitted {
		return outcomeOf(rec, true)
	}
	result := &resultFrame{RequestID: req.RequestID}
	switch {
	case callErr != nil:
		result.Outcome = phaseUnknown
		result.Detail = "launcher reply lost or refused"
	case rec.Phase == phaseCommitted:
		switch {
		case receipt.ExecUnix == 0:
			result.Outcome = phaseUnknown
			result.Detail = "committed; handoff unobserved"
		case receipt.ExecUnix < rec.CommitUnix || (receipt.ExecUnix-rec.CommitUnix)*1000 > inst.Qualified.HandoffMs:
			result.Outcome = phaseLate
			result.ExecUnix = receipt.ExecUnix
			result.Detail = "handoff outside the qualified C-to-exec interval"
		default:
			result.Outcome = phaseStarted
			result.ExecUnix = receipt.ExecUnix
		}
		if len(receipt.Receipt) <= maxEvidenceBytes {
			result.Receipt = receipt.Receipt
		}
	case !receipt.Committed:
		result.Outcome = phaseRefused
		result.Detail = "receiver did not commit C"
	default:
		result.Outcome = phaseUnknown
		result.Detail = "receipt claims C without a durable commit"
	}
	if err := r.appendLocked(&frame{Type: frameResult, Now: timeOrZero(after), Result: result}); err != nil {
		return nil, err
	}
	if err := r.materializeLocked(); err != nil {
		return nil, err
	}
	return outcomeOf(r.proj.Dispatches[req.RequestID], false)
}

func (r *Runtime) watermark() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proj.Watermark
}

// snapshotLocked copies one dispatch record so callers can read it after the
// lock is released without racing later transitions.
func (r *Runtime) snapshotLocked(requestID string) *dispatchRecord {
	rec := r.proj.Dispatches[requestID]
	if rec == nil {
		return nil
	}
	cp := *rec
	return &cp
}

func (r *Runtime) generationCurrent(uid uint32, want, role string) error {
	g, err := r.seams.Generation.Current(uid)
	if err != nil {
		return asRefusal(err, CodeGenerationMismatch, role+" generation unreadable")
	}
	if !g.Active || g.Generation != want {
		return refuse(CodeGenerationMismatch, role+" generation is not the current active generation")
	}
	return nil
}

// gate is the dispatcher's commit gate for receiver interlock A.
type gate struct {
	r         *Runtime
	requestID string
	policy    PolicyPin
}

func invalidated(detail string) *Refusal { return refuse(CodeStartInvalidated, detail) }

// Commit re-reads policy currentness (Rv), qualified time and the worker
// generation outside the lock, then checks the receiver facts, cancel and
// removal state and expiry under it and durably commits C. Expiry or Rv
// before C refuses; C at expiry refuses; a refusal never rearms the debit.
func (g *gate) Commit(facts ReceiverFacts) error {
	r := g.r
	inst, err := r.seams.Policy.Current()
	if err != nil || inst == nil || inst.Policy == nil {
		return invalidated("policy unreadable at C")
	}
	if inst.Removed || (PolicyPin{ID: inst.Policy.ID, Revision: inst.Policy.Revision}) != g.policy {
		return invalidated("policy removed or replaced before C")
	}
	now, err := r.seams.Clock.Now()
	if err != nil || !now.Confident {
		return invalidated("time unknown at C")
	}
	gen, err := r.seams.Generation.Current(facts.WorkerUID)
	if err != nil || !gen.Active {
		return invalidated("worker generation unreadable or inactive at C")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usableLocked(); err != nil {
		return err
	}
	rec := r.proj.Dispatches[g.requestID]
	if rec == nil || rec.Phase != phaseInFlight {
		return invalidated("no in-flight intent for this C")
	}
	if rec.CancelRequested || rec.StopEffectID != "" {
		return invalidated("cancel or stop intent recorded before C")
	}
	if r.proj.RemovalRecorded {
		return invalidated("removal intent recorded before C")
	}
	if !facts.DropVerified {
		return invalidated("privilege drop not verified")
	}
	if facts.Receiver != rec.Receiver || facts.WorkerUID != rec.Worker.UID || facts.Generation != rec.Worker.Generation || gen.Generation != rec.Worker.Generation || facts.ExecutionID != rec.ExecutionID || facts.EffectID != rec.EffectID || facts.ControllerEpoch != r.proj.Epoch {
		return invalidated("receiver facts differ from the original record")
	}
	if !startPermitted(now.Unix, r.proj.Watermark, rec.Deadlines.Expiry) {
		return invalidated("expiry reached before C")
	}
	return r.appendLocked(&frame{Type: frameCommit, Now: now.Unix, Commit: &commitFrame{RequestID: g.requestID, Unix: now.Unix}})
}

func cancelOutcome(c *cancelRecord, rec *dispatchRecord, replayed bool) (*Result, error) {
	res := &Result{RequestID: c.RequestID, Op: OpCancel, EffectID: c.EffectID, Replayed: replayed}
	if c.NoEffect {
		res.State = string(phaseCancelled)
		return res, nil
	}
	switch rec.StopOutcome {
	case stopAbsent:
		res.State = stopAbsent
		return res, nil
	case stopUnknown:
		res.State = stopUnknown
		return res, refuse(CodeObservationUnknown, "stop outcome unknown; claim and stop debt retained")
	}
	res.State = "stop_pending"
	return res, refuse(CodeObservationUnknown, "stop not yet confirmed")
}

func (r *Runtime) cancel(ctx context.Context, peer uint32, req *Request) (*Result, error) {
	principal := req.Principal()
	now, clockErr := r.seams.Clock.Now()
	if clockErr != nil {
		now = Reading{}
	}
	r.mu.Lock()
	existing := r.proj.Cancels[req.RequestID]
	err := r.usableLocked()
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Principal != principal || !bytes.Equal(existing.Canonical, req.Canonical) {
			return nil, refuse(CodeRequestIDConflict, "request body differs from the remembered original")
		}
		if err := req.Verify(); err != nil {
			return nil, err
		}
		if peer != existing.PeerUID {
			return nil, refuse(CodeCallerNotPermitted, "kernel peer differs from the original caller")
		}
	}
	inst, err := r.currentInstallation(now, false)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	target := r.snapshotLocked(req.DispatchRequestID)
	r.mu.Unlock()
	if target == nil {
		return nil, refuse(CodeRunNotFound, "no original dispatch with that request ID")
	}
	// Owned cancel: only the original owner, authenticated now, may cancel.
	if req.Association != target.Association || peer != target.PeerUID {
		return nil, refuse(CodeRunNotOwned, "cancel is not from the original owner")
	}
	if req.Receiver != target.Receiver || req.Policy != target.Policy || req.Worker != target.Worker || req.Plan != target.Plan {
		return nil, invalid("cancel pins differ from the original record")
	}
	if err := req.Verify(); err != nil {
		return nil, err
	}
	if inst.Catalog.Cancel != nil && !bytes.Equal(req.Canonical, inst.Catalog.Cancel.Request.Canonical) {
		return nil, invalid("request is not the protected cancel signing plan")
	}
	if err := r.generationCurrent(req.Association.UID, req.Association.Generation, "caller"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if err := r.usableLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	target = r.proj.Dispatches[req.DispatchRequestID]
	c := r.proj.Cancels[req.RequestID]
	if c == nil {
		// Table row 12: a terminal stop transaction (absent, or the no-effect
		// cancel of a reservation) accepts no new cancel; only the same-ID
		// replay of the cancel that ended it returns its original outcome.
		if terminalStop(target.StopOutcome) {
			r.mu.Unlock()
			return nil, refuse(CodeRunTerminal, "original run's stop transaction is terminal")
		}
		if err := r.reserveFitsLocked(); err != nil {
			r.mu.Unlock()
			return nil, err
		}
		noEffect := target.Phase == phaseReserved
		event := timeOrZero(now)
		stopEffect := effectID(principal, target.RequestID, lineageRecordID, componentLaunch, operationStop, 1)
		// Rows 1, 3 and 6: the cancel record is the owner's stop intent with
		// the cancel obligation. It never replaces earlier obligations: a
		// recorded removal is already on the record, and the frame's own time
		// makes the reducer record expiry detection before the cancel
		// obligation when the original expiry has passed.
		if err := r.appendLocked(&frame{Type: frameCancel, Now: event, Cancel: &cancelFrame{
			RequestID: req.RequestID, Principal: principal, PeerUID: peer, Canonical: string(req.Canonical),
			Target: target.RequestID, EffectID: stopEffect, Event: event, Detection: event, NoEffect: noEffect,
		}}); err != nil {
			r.mu.Unlock()
			return nil, err
		}
		if noEffect {
			// Row 1: a reservation never reached intent, so no receiver effect
			// exists; the transaction ends with no call.
			if err := r.appendLocked(&frame{Type: frameStop, Now: event, Stop: &stopFrame{RequestID: target.RequestID, Outcome: stopNoEffect}}); err != nil {
				r.mu.Unlock()
				return nil, err
			}
			if err := r.materializeLocked(); err != nil {
				r.mu.Unlock()
				return nil, err
			}
		}
		c = r.proj.Cancels[req.RequestID]
	}
	// Rows 1, 7 and 12: a terminal transaction is answered from the record; an
	// unresolved one (rows 8 and 9) is continued by the stop pass under the
	// same original effect and obligations, and the reply is the cancel's own
	// outcome.
	if !target.stopUnresolved() {
		res, err := cancelOutcome(c, target, existing != nil)
		r.mu.Unlock()
		return res, err
	}
	r.mu.Unlock()
	if err := r.stopPass(ctx, target.RequestID); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cancelOutcome(r.proj.Cancels[req.RequestID], r.proj.Dispatches[target.RequestID], existing != nil)
}

// stopRequestOf assembles the stop effect of one dispatch from its recorded
// obligations: the original effect identity, the first obligation's event and
// detection, every retained obligation and the earliest ceiling over them.
func stopRequestOf(rec *dispatchRecord) StopRequest {
	return StopRequest{
		EffectID: rec.StopEffectID, ExecutionID: rec.ExecutionID, Worker: rec.Worker,
		Event: rec.StopEvent, Detection: rec.StopDetection,
		Obligations: append([]StopObligation(nil), rec.StopObligations...),
		Ceiling:     rec.earliestCeiling(),
	}
}

// stopPass is the ONLY caller of Launcher.Stop. One pass drives one run's
// unresolved stop transaction through table rows 8, 9, 14 and 15:
//
//   - the durable intent is already journaled (R1), by the cancel, removal or
//     expiry frame that created it; a pass without an intent is inconsistent;
//   - a fresh transaction (no outcome frame and a call permit held by this
//     process) issues the Stop directly (row 8); any other unresolved
//     transaction observes the original stop effect and journals the class
//     first (row 9), because a reopened process cannot tell «never called»
//     from «called, reply lost» and an unknown outcome is retried only after
//     a query (R3);
//   - the projection is materialized by barrierLocked before every call (R2);
//   - the permit is consumed before the call, so no later pass in this
//     process skips the observation unless a new observation grants one;
//   - the receipt decides: receiver-owned absence with bounded evidence is
//     terminal (row 14); an error, a non-absent receipt or empty or oversized
//     evidence is journaled as unknown and keeps the claim (row 15); a lost
//     outcome frame leaves the transaction unresolved for the next pass (row
//     16).
//
// Two passes for the same run never overlap: the second waits for the first
// and returns, the record then carries the receipt.
func (r *Runtime) stopPass(ctx context.Context, requestID string) error {
	r.mu.Lock()
	if err := r.usableLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	rec := r.proj.Dispatches[requestID]
	if rec == nil || rec.StopEffectID == "" {
		r.mu.Unlock()
		return inconsistent("stop pass without a recorded intent")
	}
	if !rec.stopUnresolved() {
		r.mu.Unlock()
		return nil
	}
	if wait := r.inflight[requestID]; wait != nil {
		r.mu.Unlock()
		<-wait
		return nil
	}
	done := r.claimStopPassLocked(requestID)
	defer r.releaseStopPass(requestID, done)
	fresh := rec.StopOutcome == "" && r.permits[requestID]
	r.mu.Unlock()
	if !fresh {
		// Row 9 then row 17: the class, a failure included, is recorded and
		// the pass continues with the Stop whatever it was.
		if _, err := r.observeEffect(ctx, requestID, observeStop); err != nil {
			return err
		}
	}
	r.mu.Lock()
	if err := r.usableLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	rec = r.proj.Dispatches[requestID]
	if rec == nil || !rec.stopUnresolved() {
		r.mu.Unlock()
		return nil
	}
	if err := r.barrierLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	delete(r.permits, requestID)
	stop := stopRequestOf(rec)
	r.mu.Unlock()
	receipt, callErr := r.seams.Launcher.Stop(ctx, stop)
	after, clockErr := r.seams.Clock.Now()
	if clockErr != nil || !after.Confident {
		after = Reading{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usableLocked(); err != nil {
		return err
	}
	rec = r.proj.Dispatches[requestID]
	if rec == nil || !rec.stopUnresolved() {
		return nil
	}
	s := &stopFrame{RequestID: requestID, Outcome: stopUnknown}
	if callErr == nil && receipt.Absent && len(receipt.Evidence) > 0 && len(receipt.Evidence) <= maxEvidenceBytes {
		s.Outcome = stopAbsent
		s.Evidence = receipt.Evidence
	}
	if err := r.appendLocked(&frame{Type: frameStop, Now: timeOrZero(after), Stop: s}); err != nil {
		return err
	}
	return r.materializeLocked()
}

// observeEffect is the ONLY caller of Launcher.Observe. It queries one
// original effect of a run read-only, after the barrier, and journals the
// class: a receiver error or an unlisted class is recorded as unknown (row
// 17). The class is evidence for reconciliation only; it never changes the
// stop state or releases the claim (R5). A stop observation journaled while
// no stop pass owns the run grants the run's call permit (rule b); one
// journaled by the pass itself, or during another pass's call, grants
// nothing.
//
// queryErr is the receiver's failure, returned after the unknown class was
// recorded so a caller may report it; err is a ledger or runtime failure
// that ends the caller's work.
func (r *Runtime) observeEffect(ctx context.Context, requestID, effect string) (queryErr, err error) {
	r.mu.Lock()
	if err := r.usableLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	rec := r.proj.Dispatches[requestID]
	if rec == nil {
		r.mu.Unlock()
		return nil, inconsistent("observation of an unknown dispatch")
	}
	var q EffectQuery
	switch effect {
	case observeStop:
		if !rec.stopUnresolved() {
			r.mu.Unlock()
			return nil, nil
		}
		q = EffectQuery{EffectID: rec.StopEffectID, Operation: operationStop, ArgumentsDigest: rec.ExecutionID}
	default:
		if !rec.launchUnresolved() {
			r.mu.Unlock()
			return nil, nil
		}
		q = EffectQuery{EffectID: rec.EffectID, Operation: operationStart, ArgumentsDigest: rec.EntryDigest}
	}
	if err := r.barrierLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	r.mu.Unlock()
	class, oerr := r.seams.Launcher.Observe(ctx, q)
	if oerr != nil || (class != ObservationPresent && class != ObservationAbsent && class != ObservationContradictory) {
		class = ObservationUnknown
	}
	if oerr != nil {
		queryErr = asRefusal(oerr, CodeObservationUnknown, "effect.observe failed")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usableLocked(); err != nil {
		return queryErr, err
	}
	rec = r.proj.Dispatches[requestID]
	if rec == nil || (effect == observeStop && !rec.stopUnresolved()) {
		return queryErr, nil
	}
	if err := r.appendLocked(&frame{Type: frameObserve, Observe: &observeFrame{RequestID: requestID, Class: class, Effect: effect}}); err != nil {
		return queryErr, err
	}
	if effect == observeStop && r.inflight[requestID] == nil {
		r.grantPermitLocked(requestID)
	}
	return queryErr, nil
}

// unresolvedLocked selects, in a stable order, the runs whose stop transaction
// is unresolved and the runs whose launch effect is unresolved. It is the one
// predicate shared by the cancel tail, Sweep and Reconcile.
func (r *Runtime) unresolvedLocked() (stops, launches []string) {
	ids := make([]string, 0, len(r.proj.Dispatches))
	for id := range r.proj.Dispatches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rec := r.proj.Dispatches[id]
		if rec.stopUnresolved() {
			stops = append(stops, id)
		}
		if rec.launchUnresolved() {
			launches = append(launches, id)
		}
	}
	return stops, launches
}

// Reconcile observes every unresolved original effect through the launcher's
// read-only effect.observe and records the class (table row 10): every
// unresolved stop transaction, with or without an outcome frame, and every
// unresolved launch. It never issues a start or a stop; a recorded stop
// observation permits the next stop pass of a fresh transaction to proceed
// without observing again.
func (r *Runtime) Reconcile(ctx context.Context) error {
	r.mu.Lock()
	err := r.usableLocked()
	stops, launches := r.unresolvedLocked()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	for _, id := range stops {
		// Row 17: every class, a failure included, is recorded as evidence
		// only; the receiver's failure is not an error of the reconciliation.
		if _, err := r.observeEffect(ctx, id, observeStop); err != nil {
			return err
		}
	}
	var first error
	for _, id := range launches {
		queryErr, err := r.observeEffect(ctx, id, observeStart)
		if err != nil {
			return err
		}
		if queryErr != nil && first == nil {
			first = queryErr
		}
	}
	return first
}

// Sweep is the dispatcher's periodic stop pass. It records the expiry stop
// obligation when the qualified clock first reaches a run's original expiry
// and no earlier frame already proved it (table row 5), then runs one stop
// pass over every unresolved stop transaction (rows 8 and 9). The removal
// obligation is never added here: the removal frame already carried it to
// every run with its original event and detection (row 4). Sweep never starts
// anything (R6).
func (r *Runtime) Sweep(ctx context.Context) error {
	now, clockErr := r.seams.Clock.Now()
	if clockErr != nil {
		now = Reading{}
	}
	if _, err := r.currentInstallation(now, false); err != nil && CodeOf(err) != CodePolicyRemoved {
		return err
	}
	r.mu.Lock()
	if err := r.usableLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	for _, rec := range r.proj.Dispatches {
		expired := now.Confident && effectiveNow(now.Unix, r.proj.Watermark) >= rec.Deadlines.Expiry
		if rec.stopDebtPossible() && expired && !rec.hasObligation(sourceExpiry) {
			// The frame's time is the durable expiry detection; the reducer
			// records the obligation from it (detectExpiry) before applying
			// this intent, so both carry the same event and detection.
			if err := r.appendLocked(&frame{Type: frameStopIntent, Now: timeOrZero(now), StopIntent: &stopIntentFrame{RequestID: rec.RequestID, EffectID: rec.stopEffect(), Event: rec.Deadlines.Expiry, Detection: timeOrZero(now), Source: sourceExpiry}}); err != nil {
				r.mu.Unlock()
				return err
			}
		}
	}
	work, _ := r.unresolvedLocked()
	r.mu.Unlock()
	var first error
	for _, id := range work {
		if err := r.stopPass(ctx, id); err != nil && first == nil {
			first = err
		}
	}
	return first
}
