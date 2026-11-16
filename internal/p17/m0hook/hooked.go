// Package m0hook connects the P17 launcher hooks to the M0 launch path as a
// decorator of the M0 launcher seam. In the P17 narrow path the dispatcher is
// launcher L: Hooked wraps the S1 user-launcher seam so that no M0 start can
// reach it without a durably bound run record (H0, H1, H2 before any
// Inner.Start), the child incarnation is recorded at H3, a known live Stop
// runs H5 unless fatal duty forbids state I/O, and H6 precedes any new H1.
//
// Hook text of record: P17 Curator-side hook interface r2.16 (sha256
// 1ff91660ea77af7b0a826c6b8e2978e54770e2a67447f8f5e50bccc2dba922e1).
//
// Nothing in this repository activates the decorator: the daemon stays on
// m0.DefaultSeams(). The package does not change M0 frames, records, refusal
// codes or receipts; it maps P17 outcomes onto the existing StartReceipt and
// error contract of m0.Launcher.
//
// Process-exit duty. When a P17 durable step fails with the journal's sync
// failure (launcher.MustExit, EndOutcome.MustExit), the error Start or Stop
// returns satisfies m0.MustExit, which is the M0 fatal class, and wraps
// ErrMustExit. The duty is also latched: ExitRequired reports it, as
// m0.Runtime.ExitRequired does for the M0 ledger, and every later Start is
// refused before any contact. The latch exists because the M0 runtime
// journals a launcher error as an unknown outcome and does not return it, so
// the composition that owns the process has to read the latch.
//
// ReleaseInput is bookkeeping here, not an input gate: atomic Inner.Start
// has already received its input. A real gate needs split spawn-held/release
// seams. This waiver and the process-owner latch duty must be resolved by the
// composition before activation.
//
// Run.End is called at most once per run: the live run of an effect is
// dropped in the same step that ends it, and an unacknowledged end is retried
// only by H6 at the next restart.
package m0hook

import (
	"context"
	"errors"
	"sync"

	"github.com/relux-works/a2a-swarm/runorigin"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/hygiene"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// ChildIncarnations turns the S1 start receipt of a committed start into the
// child's kernel incarnation (pid, uid R_i, start time), for the H3 record.
// The S1 receipt format is unpublished, so nothing in this package parses
// Receipt bytes; the producer of this seam belongs to slice S1. The default
// refuses by name.
type ChildIncarnations interface {
	Child(req m0.StartRequest, receipt m0.StartReceipt) (incarnation.Incarnation, error)
}

// ChildInterface describes what the child of one start is about to receive,
// for the H3 hygiene assertion: argv, env, the protected paths and the
// protected and inherited descriptors. The S1 user-launcher owns that
// interface; this package never guesses it. The default refuses by name.
type ChildInterface interface {
	Launch(req m0.StartRequest) (hygiene.Launch, error)
}

// RunEvents names the retained event of one start, which H1 passes to Source
// (launcher.Begin takes it per run). The M0 start request carries no event
// reference and this package never derives one from Entry bytes. The default
// refuses by name.
type RunEvents interface {
	Event(req m0.StartRequest) (runorigin.EventRef, error)
}

// StopRequests supplies M0's emergency-stop identity and metadata for an
// execution. This package never derives an effect id from the start id.
// The returned request must name that same execution and worker.
type StopRequests interface {
	StopRequest(m0.StartRequest) (m0.StopRequest, error)
}

// Pins the four seams wait for.
const (
	IncarnationsPin = "S1 user-launcher start receipt (child pid and start time), unpublished"
	InterfacePin    = "S1 user-launcher child interface (argv, env, descriptors), unpublished"
	EventsPin       = "retained event reference of an M0 start (the start request carries none), unpublished"
	StopsPin        = "M0 emergency-stop request and distinct effect identity, unpublished"
)

// UnpinnedChildIncarnations refuses by name.
type UnpinnedChildIncarnations struct{}

func (UnpinnedChildIncarnations) Child(m0.StartRequest, m0.StartReceipt) (incarnation.Incarnation, error) {
	return incarnation.Incarnation{}, codes.Unpinned("child incarnation from the start receipt", IncarnationsPin)
}

// UnpinnedChildInterface refuses by name.
type UnpinnedChildInterface struct{}

func (UnpinnedChildInterface) Launch(m0.StartRequest) (hygiene.Launch, error) {
	return hygiene.Launch{}, codes.Unpinned("child interface for the hygiene assertion", InterfacePin)
}

// UnpinnedRunEvents refuses by name.
type UnpinnedRunEvents struct{}

func (UnpinnedRunEvents) Event(m0.StartRequest) (runorigin.EventRef, error) {
	return runorigin.EventRef{}, codes.Unpinned("retained event of the start", EventsPin)
}

// UnpinnedStopRequests refuses without sending an invented stop.
type UnpinnedStopRequests struct{}

func (UnpinnedStopRequests) StopRequest(m0.StartRequest) (m0.StopRequest, error) {
	return m0.StopRequest{}, codes.Unpinned("M0 emergency-stop identity", StopsPin)
}

// Deps are the seams of one Hooked. L and Installation are required. The
// caller supplies the same explicit ledger fact as launcher.Config.Installation;
// it owns its durable flip after the first successful Quarantine.Start, before
// any Begin (independently of the journal fact). Nothing here infers either fact.
// A nil Inner is m0.UnpinnedLauncher{}; the other nil seams refuse by name.
type Deps struct {
	Installation sweep.Installation
	Stops        StopRequests
	Inner        m0.Launcher
	L            *launcher.Launcher
	Incarnations ChildIncarnations
	Hygiene      ChildInterface
	Events       RunEvents
}

var (
	// ErrLauncherRequired refuses a construction without L.
	ErrLauncherRequired = errors.New("m0hook: P17 launcher required")
	// ErrNotRestarted refuses Start on a Hooked that did not come out of New:
	// H6 has not run, so no new H1 may begin.
	ErrNotRestarted = errors.New("m0hook: restart sweep has not run; construct with New")
	// ErrEffectRequired refuses a start or stop without an effect id.
	ErrEffectRequired = errors.New("m0hook: effect id required")
	// ErrExecutionRequired refuses a start no later stop could identify.
	ErrExecutionRequired = errors.New("m0hook: execution id required")
	// ErrStopAmbiguous leaves H5 untouched and is retained on every candidate.
	ErrStopAmbiguous = errors.New("m0hook: stop matches multiple effects")
	// ErrStopIdentity refuses a malformed emergency-stop request from its seam.
	ErrStopIdentity = errors.New("m0hook: emergency-stop identity differs from the execution")
	// ErrInstallationRequired refuses an absent or invalid explicit ledger fact.
	ErrInstallationRequired = errors.New("m0hook: explicit quarantine installation fact required")
	// ErrEffectKnown refuses a second Start for an effect this process has
	// already attempted: one run per effect id, never a second bind.
	ErrEffectKnown = errors.New("m0hook: effect already attempted in this process")
	// ErrNotCommitted marks an inner start that returned without a commit.
	ErrNotCommitted = errors.New("m0hook: inner start did not commit")
	// ErrUnproved marks a committed inner start whose child incarnation could
	// not be recorded; Attempt reports emergency-stop and H5 evidence.
	ErrUnproved = errors.New("m0hook: child incarnation not proved after a committed start")
	// ErrSpawnGate marks a spawn gate refusal before any inner call.
	ErrSpawnGate = errors.New("m0hook: spawn gate refused before the inner start")
	// ErrMustExit marks the process-exit duty of a P17 durable step. Every
	// error that wraps it also satisfies m0.MustExit.
	ErrMustExit = errors.New("m0hook: P17 run record sync failure; the process must exit")
)

// maxRetained bounds the terminal attempts kept for Code and Last. Live
// runs and HELD effects are never evicted.
const maxRetained = 256

// Attempt is the retained outcome of the last Start for one effect.
type Attempt struct {
	// Key is the run record key when a record was created.
	Key string
	// Code is the P17 code of the attempt, empty when it carried none.
	Code codes.Code
	// Err is the decisive error of the attempt, nil on a live run.
	Err error
	// Ended reports that H5 ran for this effect in this process.
	Ended bool
	// End is the H5 outcome when H5 ran. Its child verdict is not a known exit.
	End *launcher.EndOutcome
	// ChildStatusKnown is false: M0 receipts supply no child exit status.
	// Never interpret End.Verdict.Status == 0 as a known successful child exit.
	ChildStatusKnown bool
	// InnerStopAttempted and InnerStopErr retain an emergency stop. False
	// with a nonnil error explicitly means no inner stop was possible.
	InnerStopAttempted bool
	InnerStopErr       error
	// EndErr is the H5 refusal, run_end_unknown included. It is nil after an
	// acknowledged, durably recorded end.
	EndErr error
}

type effect struct {
	mu sync.Mutex
	id string
	// execution and worker are immutable after claim. M0 gives Stop its own
	// effect id; the retained execution and worker identify the original run.
	execution string
	worker    m0.Worker
	done      bool
	held      bool
	run       *launcher.Run
	a         Attempt
}

// Hooked implements m0.Launcher around the S1 seam with the P17 gates.
type Hooked struct {
	inner        m0.Launcher
	l            *launcher.Launcher
	incarnations ChildIncarnations
	childIface   ChildInterface
	events       RunEvents
	stops        StopRequests

	restarted bool
	reports   []launcher.Report

	mu      sync.Mutex
	effects map[string]*effect
	// seen is process-life duplicate suppression, independent of diagnostics.
	// It grows by one entry per distinct attempted effect and is never evicted.
	seen map[string]struct{}
	// order lists terminal effects oldest first, for bounded retention.
	order []string
	// exit is the latched process-exit duty; it is never cleared.
	exit error
}

// New composes the decorator and runs H6 once: L.Restart(ctx) before any
// Start is accepted. Its reports stay readable through Reports.
//
// Any recovery error returns the Hooked with readable Reports and a nonnil
// error; that Hooked refuses every Start. Fatal reports additionally latch
// an error satisfying m0.MustExit, including outcome-only fatal flags.
func New(ctx context.Context, d Deps) (*Hooked, error) {
	if d.L == nil {
		return nil, ErrLauncherRequired
	}
	if d.Installation != sweep.Fresh && d.Installation != sweep.Established {
		return nil, ErrInstallationRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if d.Inner == nil {
		d.Inner = m0.UnpinnedLauncher{}
	}
	if d.Incarnations == nil {
		d.Incarnations = UnpinnedChildIncarnations{}
	}
	if d.Hygiene == nil {
		d.Hygiene = UnpinnedChildInterface{}
	}
	if d.Events == nil {
		d.Events = UnpinnedRunEvents{}
	}
	if d.Stops == nil {
		d.Stops = UnpinnedStopRequests{}
	}
	h := &Hooked{inner: d.Inner, l: d.L, incarnations: d.Incarnations, childIface: d.Hygiene, events: d.Events, stops: d.Stops, effects: make(map[string]*effect), seen: make(map[string]struct{})}
	return h, h.acceptRestart(d.L.Restart(ctx))
}

// acceptRestart retains all reports and refuses admission on any recovery
// error. A per-record HELD report is surfaced too, not hidden as startup success.
func (h *Hooked) acceptRestart(reports []launcher.Report) error {
	h.reports = append([]launcher.Report(nil), reports...)
	var causes []error
	fatal := false
	for _, rep := range h.reports {
		causes = append(causes, rep.Err)
		if rep.Outcome != nil {
			causes = append(causes, rep.Outcome.RecordErr)
			fatal = fatal || rep.Outcome.MustExit || launcher.MustExit(rep.Outcome.RecordErr)
		}
		fatal = fatal || launcher.MustExit(rep.Err)
	}
	if fatal {
		return h.latch(fatalOf(causes...))
	}
	err := errors.Join(causes...)
	h.restarted = err == nil
	return err
}

// Reports returns the H6 reports recorded at construction.
func (h *Hooked) Reports() []launcher.Report {
	return append([]launcher.Report(nil), h.reports...)
}

// ExitRequired reports the latched process-exit duty: a P17 durable step of
// this Hooked failed with the journal's sync failure. The owning process must
// exit and must not continue with state I/O.
func (h *Hooked) ExitRequired() bool { return h.ExitCause() != nil }

// ExitCause returns the first error that latched the process-exit duty, or
// nil. It satisfies m0.MustExit and wraps ErrMustExit.
func (h *Hooked) ExitCause() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exit
}

// fatalOf builds the error of the M0 fatal class from the causes.
func fatalOf(causes ...error) error {
	err := errors.Join(append([]error{ErrMustExit}, causes...)...)
	if !m0.MustExit(err) {
		// The duty was reported by a flag only: carry the sentinel itself.
		err = errors.Join(err, journal.ErrSyncFailure)
	}
	return err
}

// latch records the first process-exit duty and returns err.
func (h *Hooked) latch(err error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.exit == nil {
		h.exit = err
	}
	return err
}

// surface turns an error that carries the launcher's process-exit duty into
// the M0 fatal class and latches it. Any other error is returned unchanged.
func (h *Hooked) surface(err error) error {
	if err == nil || errors.Is(err, ErrMustExit) || !launcher.MustExit(err) {
		return err
	}
	return h.latch(fatalOf(err))
}

// withFatal retains the primary cause alongside any cleanup error, fatal or not.
func withFatal(err, fatal error) error {
	if fatal == nil {
		return err
	}
	return errors.Join(err, fatal)
}

// Code returns the P17 code of the last attempt for the effect; ok is false
// when the effect is unknown here or its attempt carried no code.
func (h *Hooked) Code(effectID string) (codes.Code, bool) {
	a, ok := h.Last(effectID)
	if !ok || a.Code == "" {
		return "", false
	}
	return a.Code, true
}

// Last returns the retained attempt for the effect.
func (h *Hooked) Last(effectID string) (Attempt, bool) {
	h.mu.Lock()
	e, ok := h.effects[effectID]
	h.mu.Unlock()
	if !ok {
		return Attempt{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.a, true
}

// claim returns the effect entry, creating it when absent. The caller holds
// no lock; it takes e.mu afterwards.
func (h *Hooked) claim(req m0.StartRequest) *effect {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.effects == nil {
		h.effects = make(map[string]*effect)
	}
	if e, ok := h.effects[req.EffectID]; ok {
		return e
	}
	if h.seen == nil {
		h.seen = make(map[string]struct{})
	}
	_, seen := h.seen[req.EffectID]
	e := &effect{id: req.EffectID, execution: req.ExecutionID, worker: req.Worker, done: seen}
	if !seen {
		h.seen[req.EffectID] = struct{}{}
		h.effects[req.EffectID] = e
	}
	return e
}

// lookupStop joins M0's distinct stop effect to the original execution. A
// missing, foreign or ambiguous identity must never end another run.
func (h *Hooked) lookupStop(req m0.StopRequest) []*effect {
	h.mu.Lock()
	defer h.mu.Unlock()
	if req.ExecutionID == "" {
		return nil
	}
	var found []*effect
	for _, e := range h.effects {
		if e.execution == req.ExecutionID && e.worker == req.Worker {
			found = append(found, e)
		}
	}
	return found
}

// retire records a terminal effect (no live run, not HELD) and evicts the
// oldest terminal effects beyond maxRetained. Callers hold e.mu; only
// terminal effects are ever listed here, so eviction needs no other lock.
func (h *Hooked) retire(e *effect) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.order = append(h.order, e.id)
	for len(h.order) > maxRetained {
		old := h.order[0]
		h.order = h.order[1:]
		if x, ok := h.effects[old]; ok && x != e {
			delete(h.effects, old)
		}
	}
}

// innerOf never dereferences a nil seam: a Hooked that skipped New still
// refuses by name.
func (h *Hooked) innerOf() m0.Launcher {
	if h.inner == nil {
		return m0.UnpinnedLauncher{}
	}
	return h.inner
}

// principalOf is the input principal of the run: the worker the dispatcher
// asked for. The launcher refuses at H0, before any contact, when its uid is
// not the leased R_i.
func principalOf(req m0.StartRequest) keymaker.Principal {
	return keymaker.Principal{UID: req.Worker.UID, Generation: req.Worker.Generation}
}

// Start runs H0, H1 and H2 through L.Begin, then only on a durably bound
// record the hygiene assertion, H3 around the single Inner.Start, and the
// input release. Every pre-bound refusal returns Committed:false with a nil
// error and zero Inner and gate calls; a HELD bind returns its error with the
// same zero calls. Ordinary failures after a bound record end through H5;
// fatal duty instead forbids further launcher state I/O. An error carrying it is
// returned in the M0 fatal class on every path.
func (h *Hooked) Start(ctx context.Context, req m0.StartRequest, gate m0.CommitGate) (m0.StartReceipt, error) {
	if cause := h.ExitCause(); cause != nil {
		// The process must exit: no new H1, no contact, no state I/O.
		return m0.StartReceipt{}, cause
	}
	if !h.restarted || h.l == nil {
		return m0.StartReceipt{}, ErrNotRestarted
	}
	if req.EffectID == "" {
		return m0.StartReceipt{}, ErrEffectRequired
	}
	if req.ExecutionID == "" {
		return m0.StartReceipt{}, ErrExecutionRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return h.startEffect(ctx, h.claim(req), req, gate)
}

// startEffect orders a claimed effect with Stop, including callers that waited
// for its lock while another effect latched fatal duty.
func (h *Hooked) startEffect(ctx context.Context, e *effect, req m0.StartRequest, gate m0.CommitGate) (m0.StartReceipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cause := h.ExitCause(); cause != nil {
		return m0.StartReceipt{}, cause
	}
	if e.done {
		// One run per effect id. A HELD effect reports its retained refusal;
		// nothing binds again.
		return m0.StartReceipt{}, errors.Join(ErrEffectKnown, e.a.Err)
	}
	e.done = true
	defer func() {
		if e.run == nil && !e.held {
			h.retire(e)
		}
	}()
	receipt, err := h.start(ctx, e, req, gate)
	return receipt, h.surface(err)
}

// start is the body of Start. e.mu is held.
func (h *Hooked) start(ctx context.Context, e *effect, req m0.StartRequest, gate m0.CommitGate) (m0.StartReceipt, error) {
	// The retained event of this start, before any contact. A refusal here
	// launched and contacted nothing: definite.
	event, err := h.events.Event(req)
	if err != nil {
		e.a = Attempt{Err: err}
		return m0.StartReceipt{Committed: false}, h.refusal(e, err)
	}

	run, err := h.l.Begin(ctx, event, principalOf(req))
	if err != nil {
		e.a = Attempt{Err: err}
		code, ok := codes.CodeOf(err)
		if ok {
			e.a.Code = code
		}
		// Exit duty precedes every nil-error mapping, even a terminal attempt.
		var record *launcher.BeginRecordError
		if errors.As(err, &record) {
			e.a.Key = record.RecordKey()
		}
		if fatal := h.refusal(e, err); fatal != nil {
			return m0.StartReceipt{}, fatal
		}
		if record != nil {
			// Under uncertainty the state is ATTEMPTED, not a durable fact.
			if record.RecordUncertain() {
				e.held = true
				return m0.StartReceipt{}, err
			}
			switch record.RecordState() {
			case runrecord.Unsent:
				if code == codes.BindUnavailable {
					return m0.StartReceipt{Committed: false}, nil
				}
			case runrecord.Refused:
				if code == codes.BindRefused {
					return m0.StartReceipt{Committed: false}, nil
				}
			}
			// A record that is not certainly terminal cannot be a no-start.
			e.held = true
			return m0.StartReceipt{}, err
		}
		switch {
		case ok && code == codes.BindUnknown:
			e.held = true
			return m0.StartReceipt{}, err
		case ok:
			if cl, known := code.Class(); known && cl != codes.Unknown {
				return m0.StartReceipt{Committed: false}, nil
			}
			return m0.StartReceipt{}, err
		case codes.IsUnpinned(err):
			// A seam refusing by name launched nothing: definite.
			return m0.StartReceipt{Committed: false}, nil
		}
		return m0.StartReceipt{}, err
	}
	e.a = Attempt{Key: run.Key}

	// H0 in the concrete launcher already enforces worker uid == leased uid.
	// Do not add a dead second guard: principalOf forwards the exact worker.

	// H3 interface assertion, only on the bound record and before any spawn.
	launch, err := h.childIface.Launch(req)
	if err == nil {
		err = hygiene.Assert(launch)
	}
	if err != nil {
		return m0.StartReceipt{Committed: false}, h.end(ctx, e, run, err)
	}

	var (
		receipt  m0.StartReceipt
		innerErr error
		started  bool
	)
	spawnErr := run.Spawn(func(childUID uint32) (incarnation.Incarnation, error) {
		started = true
		receipt, innerErr = h.innerOf().Start(ctx, req, gate)
		if innerErr != nil {
			return incarnation.Incarnation{}, innerErr
		}
		if !receipt.Committed {
			return incarnation.Incarnation{}, ErrNotCommitted
		}
		inc, err := h.incarnations.Child(req, receipt)
		if err != nil {
			return incarnation.Incarnation{}, errors.Join(ErrUnproved, err)
		}
		if !inc.Valid() || inc.UID != childUID {
			return incarnation.Incarnation{}, errors.Join(ErrUnproved, errors.New("m0hook: child incarnation incomplete or under another uid"))
		}
		return inc, nil
	})
	switch {
	case spawnErr == nil:
		// Bookkeeping only: atomic Inner.Start already received its input.
		// A real input gate requires split spawn-held and release seams.
		if err := run.ReleaseInput(func() error { return nil }); err != nil {
			// The child exists and the release record failed: stop it, end.
			err = errors.Join(ErrUnproved, err)
			err = errors.Join(err, h.stopInner(ctx, e, req))
			return receipt, withFatal(err, h.end(ctx, e, run, err))
		}
		e.run = run
		return receipt, nil
	case !started:
		// The spawn gate refused before any inner call: nothing launched.
		return m0.StartReceipt{Committed: false}, h.end(ctx, e, run, errors.Join(ErrSpawnGate, spawnErr))
	case innerErr != nil:
		if receipt.Committed {
			innerErr = errors.Join(innerErr, h.stopInner(ctx, e, req))
		}
		return receipt, withFatal(innerErr, h.end(ctx, e, run, innerErr))
	case !receipt.Committed:
		return receipt, h.end(ctx, e, run, spawnErr)
	}
	// Committed but not proved or not recorded: a child may exist.
	spawnErr = errors.Join(spawnErr, h.stopInner(ctx, e, req))
	return receipt, withFatal(spawnErr, h.end(ctx, e, run, spawnErr))
}

// refusal is the single fatal check for errors about to become nil-error
// no-starts. e.mu is held; ordinary refusals stay retained in Attempt.Err.
func (h *Hooked) refusal(e *effect, err error) error {
	if launcher.MustExit(err) {
		e.held = true
		return h.surface(err)
	}
	return nil
}

// stopInner uses only the request supplied by M0. With no valid identity no
// inner stop is sent: the retained failure is loud and H5's uid sweep is the
// backstop. e.mu is held.
func (h *Hooked) stopInner(ctx context.Context, e *effect, req m0.StartRequest) error {
	stop, err := h.stops.StopRequest(req)
	if err == nil && (stop.EffectID == "" || stop.EffectID == req.EffectID || stop.ExecutionID != req.ExecutionID || stop.Worker != req.Worker) {
		err = ErrStopIdentity
	}
	if err == nil {
		e.a.InnerStopAttempted = true
		_, err = h.innerOf().Stop(ctx, stop)
	}
	e.a.InnerStopErr = err
	return err
}

// end runs H5 for the run and retains the outcome. e.mu is held. It is the
// only caller of Run.End, and it drops the live run in the same step, so a
// run is never ended twice. It returns every decisive end failure, with
// process-exit duty taking precedence over ordinary unknown outcomes.
//
// The decorator has no child exit status (the M0 stop receipt carries none),
// so it calls End with an unknown status. An acknowledged and durably
// recorded end then comes back as codes.ErrUnclassified, which only says that
// no exit status can be computed here; that is not an end failure and is not
// retained as one.
func (h *Hooked) end(ctx context.Context, e *effect, run *launcher.Run, cause error) error {
	if cause != nil {
		e.a.Err = cause
		if code, ok := codes.CodeOf(cause); ok {
			e.a.Code = code
		}
	}
	// Do not begin more launcher state I/O after a fatal seam failure or a
	// concurrent latch. Inner.Stop remains allowed; the process owner exits.
	if fatal := h.refusal(e, cause); fatal != nil {
		e.run = run
		return fatal
	}
	if fatal := h.ExitCause(); fatal != nil {
		e.run, e.held = run, true
		return fatal
	}
	out, err := run.End(ctx, 0, false, nil)
	if out.Acknowledged && out.RecordErr == nil && !out.MustExit && errors.Is(err, codes.ErrUnclassified) {
		err = nil
	}
	e.run = nil
	e.held = err != nil
	e.a.Ended = true
	e.a.End = &out
	e.a.EndErr = err
	if err != nil {
		if code, ok := codes.CodeOf(err); ok {
			e.a.Code = code
		}
	}
	if out.MustExit || launcher.MustExit(err) {
		return h.latch(fatalOf(err, out.RecordErr))
	}
	return err
}

// Stop looks up the execution first and locks a unique known effect before
// Inner.Stop, ordering it with Start. Unknown effects remain unlocked. Inner
// always runs, even before successful restart or after the fatal latch; in
// those cases no launcher state I/O follows and the gate error is returned.
// Empty effect ids are refused AFTER Inner.Stop. Ambiguity is retained on
// every candidate and returned loudly, without choosing one run for H5.
func (h *Hooked) Stop(ctx context.Context, req m0.StopRequest) (m0.StopReceipt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	matches := h.lookupStop(req)
	var e *effect
	if len(matches) == 1 {
		e = matches[0]
		e.mu.Lock()
		defer e.mu.Unlock()
	}
	receipt, err := h.innerOf().Stop(ctx, req)
	err = h.surface(err)
	if req.EffectID == "" {
		err = errors.Join(err, ErrEffectRequired)
	}
	if len(matches) > 1 {
		for _, candidate := range matches {
			candidate.mu.Lock()
			candidate.a.Err = errors.Join(candidate.a.Err, ErrStopAmbiguous)
			candidate.mu.Unlock()
		}
		err = errors.Join(err, ErrStopAmbiguous)
	}
	if fatal := h.ExitCause(); fatal != nil {
		return receipt, errors.Join(err, fatal)
	}
	if !h.restarted || h.l == nil {
		return receipt, errors.Join(err, ErrNotRestarted)
	}
	if req.EffectID == "" || e == nil {
		return receipt, err
	}
	if e.run != nil {
		endErr := h.end(ctx, e, e.run, nil)
		if !e.held {
			h.retire(e)
		}
		return receipt, withFatal(err, endErr)
	}
	return receipt, err
}

// Observe passes through to Inner. It contacts neither Source nor Keymaker.
func (h *Hooked) Observe(ctx context.Context, q m0.EffectQuery) (m0.Observation, error) {
	return h.innerOf().Observe(ctx, q)
}
