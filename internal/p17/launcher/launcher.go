// Package launcher composes the P17 hooks H0, H1, H2, H3, H5 and H6 for the
// launcher L around the standalone clients: isolation, source, keymaker, the
// durable run record, incarnations and the uid sweep.
//
// It is the hook point the S2-M0 launch path calls: Begin runs H0, H1 and
// H2 and returns a bound run; Spawn and ReleaseInput are the H3 gates; End is
// H5; Restart is the H6 startup sweep and the reaper's table. Nothing here
// spawns a process, reads policy, or dispatches; those stay with S2-M0.
//
// Hook text of record: P17 Curator-side hook interface r2.13 (sha256
// 6849a4edc98b8aa4b257498f5c5c1efa9d5b25d09187474ee7ef284c89eb607a).
//
// Order of H2 (steps 4 and 5): validate on Source, then one fresh
// Keymaker connection WITH its eager hello, then the durable binding record,
// then the bind frame. A connect or hello failure is run_bind_unavailable
// with no record; the next attempt starts from a fresh H1.
//
// The bind is written at most keymaker.BindAttemptLimit times, a fixed
// constant: the exact same-ID retry follows only a read, correlated,
// retryable refusal on the original live connection. Everything else, and
// the limit itself, is HELD (run_bind_unknown) with the record in binding.
//
// The uid R_i leased at H0 is the input principal's uid. A lease taken before
// the record exists is returned on a refusal path only when BOTH hold: the
// privilege-drop helper was never invoked for that lease (spawn or sweep
// mode; tracked per lease in the quarantine), AND the D5a zero-process proof
// on a qualified procfs view shows zero processes. Otherwise the uid is
// quarantined and the refusal says so in its detail.
package launcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/relux-works/a2a-swarm/runorigin"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Config is the launcher's own configuration. The bind attempt limit is not
// here: it is the fixed constant keymaker.BindAttemptLimit.
type Config struct {
	LauncherUID uint32
	// Station is the complete current station registration number.
	Station string
	// RequestTimeout is the Keymaker per-request timeout (1..2000 ms).
	RequestTimeout time.Duration
	// MaxRestarts bounds H1 restarts after a lost validate.
	MaxRestarts int
	// ProcRoot is the procfs root for the D5a proof; empty means /proc.
	ProcRoot string
	// ProcMountInfo is the mountinfo file that describes the mount at
	// ProcRoot; empty means /proc/self/mountinfo. Only tests with a fixture
	// proc tree set it.
	ProcMountInfo string
	SweepBudget   sweep.Budget
}

// RecordStore is the durable run record store the launcher writes.
// *runrecord.Store implements it.
type RecordStore interface {
	Create(r *runrecord.Record) error
	Update(key string, mutate func(*runrecord.Record) error) (*runrecord.Record, error)
	Get(key string) (*runrecord.Record, bool)
	Keys() []string
	Fenced(key string) bool
}

// Deps are the injected clients and seams. Nil seams are the refusing
// defaults; Source, Records and Incarnations are required. Source is shared
// by every run and is never mutated by the launcher.
type Deps struct {
	Placement    isolation.Placement
	Source       *source.Client
	Generations  source.Generations
	Keymaker     keymaker.Dialer
	KeymakerWire keymaker.Wire
	Records      RecordStore
	Incarnations incarnation.Reader
	Killer       sweep.Killer
	Quarantine   *sweep.Quarantine
	// WorkProductExport and OperatorRelease are the R_i release seams (book ch. 07,
	// Core G1). Nil is the refusing default; they are installed into the
	// Quarantine gate when it has none.
	WorkProductExport sweep.WorkProductExport
	OperatorRelease   sweep.OperatorRelease
	Nonce             func() (string, error)
}

// Launcher is L.
type Launcher struct {
	cfg Config
	d   Deps
}

var (
	// ErrRebindUnsupported: j7_run_rebind is out of v1 (D3).
	ErrRebindUnsupported = errors.New("launcher: rebind is refused in v1")
	// ErrNotBound refuses an H3 step before a durable bound record.
	ErrNotBound = errors.New("launcher: run record is not durably bound")
	// ErrFenced refuses input on a fenced run.
	ErrFenced = errors.New("launcher: run is fenced")
	// ErrParent refuses a child run whose parent record is not live and bound.
	ErrParent = errors.New("launcher: parent run is not a live bound record of this launcher")
	// ErrEndCalled refuses a second End on the same Run. The first End already
	// ran teardown and the uid sweep; an unacknowledged end is retried with
	// RetryEnd, never by sweeping the uid again from a stale Run.
	ErrEndCalled = errors.New("launcher: End was already called for this run")
	// ErrLeaseQuarantined is joined to a Begin error that is not a typed
	// refusal when the pre-record lease could not be returned.
	ErrLeaseQuarantined = errors.New("launcher: leased uid quarantined: zero-process proof not established")
)

// MustExit reports the journal's process-exit duty: an error from this
// package that wraps a journal sync failure means the process must exit and
// must not continue with state I/O. Refusals built here also carry
// Detail["must_exit"]="true" in that case.
func MustExit(err error) bool { return runrecord.MustExit(err) }

// durable builds the refusal for a failed durable step and marks the
// process-exit duty.
func durable(code codes.Code, op string, cause error) *codes.Refusal {
	r := codes.Refuse(code, op, cause)
	if runrecord.MustExit(cause) {
		r.With("must_exit", "true")
	}
	return r
}

func utoa(u uint32) string { return strconv.FormatUint(uint64(u), 10) }

func defaultNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// New validates the composition.
func New(cfg Config, d Deps) (*Launcher, error) {
	if d.Source == nil || d.Records == nil || d.Incarnations == nil {
		return nil, errors.New("launcher: source client, record store and incarnation reader are required")
	}
	if cfg.Station == "" {
		return nil, errors.New("launcher: station registration required")
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = time.Second
	}
	if !codes.RequestTimeoutOK(cfg.RequestTimeout.Milliseconds()) || cfg.RequestTimeout < time.Millisecond {
		return nil, keymaker.ErrTimeout
	}
	if cfg.MaxRestarts <= 0 {
		cfg.MaxRestarts = 2
	}
	if cfg.SweepBudget.Attempts <= 0 {
		cfg.SweepBudget = sweep.DefaultBudget
	}
	if d.Placement == nil {
		d.Placement = isolation.Unpinned{}
	}
	if d.Generations == nil {
		d.Generations = source.UnpinnedGenerations{}
	}
	if d.KeymakerWire == nil {
		d.KeymakerWire = keymaker.Unpinned{}
	}
	if d.Killer == nil {
		d.Killer = sweep.UnpinnedKiller{}
	}
	if d.WorkProductExport == nil {
		d.WorkProductExport = sweep.UnpinnedWorkProductExport{}
	}
	if d.OperatorRelease == nil {
		d.OperatorRelease = sweep.UnpinnedOperatorRelease{}
	}
	if d.Quarantine == nil {
		d.Quarantine = &sweep.Quarantine{}
	}
	if d.Quarantine.Gate.WorkProductExport == nil {
		d.Quarantine.Gate.WorkProductExport = d.WorkProductExport
	}
	if d.Quarantine.Gate.Operator == nil {
		d.Quarantine.Gate.Operator = d.OperatorRelease
	}
	if d.Nonce == nil {
		d.Nonce = defaultNonce
	}
	// Every sweep-mode helper invocation is recorded against the uid's lease.
	d.Killer = helperKiller{k: d.Killer, q: d.Quarantine}
	return &Launcher{cfg: cfg, d: d}, nil
}

// helperKiller records every sweep-mode invocation of the privilege-drop
// helper in the quarantine before it runs.
type helperKiller struct {
	k sweep.Killer
	q *sweep.Quarantine
}

func (h helperKiller) KillAll(ctx context.Context, uid uint32) error {
	h.q.MarkHelper(uid)
	return h.k.KillAll(ctx, uid)
}

// Run is one bound run owned by this launcher process.
type Run struct {
	mu       sync.Mutex
	l        *Launcher
	Key      string
	RunID    string
	ChildUID uint32
	km       *keymaker.Conn
	fenced   bool
	endDone  bool
}

// Rebind is refused in v1 (D3).
func (l *Launcher) Rebind(string) error { return ErrRebindUnsupported }

func (l *Launcher) nonce() (string, error) {
	n, err := l.d.Nonce()
	if err != nil {
		return "", err
	}
	if n == "" {
		return "", errors.New("launcher: empty nonce")
	}
	return n, nil
}

func (l *Launcher) view() sweep.View {
	return sweep.View{ProcRoot: l.cfg.ProcRoot, MountInfo: l.cfg.ProcMountInfo}
}

// Begin runs H0, H1 and H2 for a root run and returns a bound run. event is
// this run's retained event; it is passed to Source per call, so concurrent
// runs share Deps.Source without mutating it. inputPrincipal.UID must equal
// the uid leased for R_i. On every refusal nothing is launched; a HELD bind
// leaves the record in binding.
func (l *Launcher) Begin(ctx context.Context, event runorigin.EventRef, inputPrincipal keymaker.Principal) (*Run, error) {
	return l.begin(ctx, &event, inputPrincipal, nil)
}

// BeginChild runs the same gates for a child run (H4 lifecycle binding only):
// parent_run comes from this launcher's live parent record, the origin is
// null and Keymaker inherits the immutable origin. There is no parameter by
// which an agent could supply parent_run, and no Source contact.
func (l *Launcher) BeginChild(ctx context.Context, parent *Run, inputPrincipal keymaker.Principal) (*Run, error) {
	if parent == nil || parent.l != l {
		return nil, ErrParent
	}
	rec, ok := l.d.Records.Get(parent.Key)
	if !ok || rec.State != runrecord.Bound || rec.RunID == "" || rec.RunID != parent.RunID || l.d.Records.Fenced(parent.Key) {
		return nil, ErrParent
	}
	return l.begin(ctx, nil, inputPrincipal, rec)
}

// returnLease handles a lease on a Begin path that ends before a run record
// exists. The lease is returned only when the privilege-drop helper was never
// invoked for it AND the D5a enumeration on a qualified view shows zero
// processes; the proof is never skipped, and a helper invocation is never
// excused by a clean proof. Otherwise the uid is quarantined; the Begin
// error keeps its code and reports that in its detail.
func (l *Launcher) returnLease(uid uint32, err error) error {
	e := sweep.Enumerate(l.view(), uid)
	if l.d.Quarantine.ReturnUnused(uid, e) == nil {
		return err
	}
	reason := "enumeration failed or procfs view not qualified"
	switch {
	case l.d.Quarantine.HelperInvoked(uid):
		reason = "privilege-drop helper was invoked for this lease"
	case e.Err != nil:
	case len(e.Matched) > 0:
		reason = "process found under the uid"
	case len(e.Unreadable) > 0:
		reason = "unreadable pid"
	}
	var r *codes.Refusal
	if errors.As(err, &r) && r != nil {
		r.With("lease", "quarantined").With("lease_uid", utoa(uid)).With("lease_reason", reason)
		return err
	}
	return errors.Join(err, ErrLeaseQuarantined, errors.New("launcher: uid "+utoa(uid)+": "+reason))
}

func (l *Launcher) begin(ctx context.Context, event *runorigin.EventRef, inputPrincipal keymaker.Principal, parent *runrecord.Record) (_ *Run, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if inputPrincipal.UID == 0 || inputPrincipal.Generation == "" {
		return nil, errors.New("launcher: input principal required")
	}
	registration, perr := strconv.ParseUint(l.cfg.Station, 10, 64)
	if perr != nil || registration == 0 || strconv.FormatUint(registration, 10) != l.cfg.Station {
		return nil, errors.New("launcher: station registration must be a canonical positive integer")
	}
	// H0 before any contact.
	dec, err := isolation.Check(l.d.Placement, l.cfg.LauncherUID)
	if err != nil {
		return nil, err
	}
	if err := l.d.Quarantine.Lease(dec.ChildUID); err != nil {
		return nil, codes.Refuse(codes.IsolationUnqualified, "child uid quarantined", err)
	}
	// From here until a run record exists the lease is returned on every
	// return path (after the zero-process proof), so that a retryable
	// failure stays retryable with the same uid.
	recorded := false
	defer func() {
		if err != nil && !recorded {
			err = l.returnLease(dec.ChildUID, err)
		}
	}()
	// The input principal is R_i: its uid must be the leased uid. This is
	// still H0: no Source and no Keymaker contact has happened.
	if inputPrincipal.UID != dec.ChildUID {
		return nil, codes.Refuse(codes.IsolationUnqualified, "input principal uid differs from the leased child uid", nil).
			With("uid", utoa(dec.ChildUID)).With("input_uid", utoa(inputPrincipal.UID))
	}
	gen, err := l.d.Generations.Current(l.cfg.LauncherUID)
	if err != nil {
		if codes.IsUnpinned(err) {
			return nil, err
		}
		return nil, codes.Refuse(codes.OriginRefused, "launcher generation", err)
	}
	self, err := l.d.Incarnations.Self()
	if err != nil {
		return nil, err
	}
	var validated *source.Validated
	var nonce string
	if parent == nil {
		// Source's numeric generation is a strict subset of Keymaker's opaque
		// generation. Never silently coerce or default a different principal.
		generation, gerr := strconv.ParseUint(inputPrincipal.Generation, 10, 64)
		if gerr != nil || generation == 0 || strconv.FormatUint(generation, 10) != inputPrincipal.Generation {
			return nil, codes.Refuse(codes.OriginRefused, "Source input generation", gerr)
		}
		input := runorigin.Principal{UID: inputPrincipal.UID, Generation: generation}
		// H1 and the validate step of H2, restarting from H1 on a lost validate.
		for restarts := 0; ; restarts++ {
			nonce, err = l.nonce()
			if err != nil {
				return nil, err
			}
			prepared, err := l.d.Source.PrepareFor(ctx, nonce, *event, input)
			if err != nil {
				return nil, err
			}
			validated, err = prepared.Validate(ctx)
			if err == nil {
				break
			}
			if source.IsRestart(err) && restarts < l.cfg.MaxRestarts {
				continue
			}
			if source.IsRestart(err) {
				return nil, codes.Refuse(codes.OriginUnavailable, "validate lost after restarts", err)
			}
			return nil, err
		}
		// The station binding must be present and equal, for every Wire: an
		// absent binding is a refusal, never a skipped check.
		if got, ok := validated.Bindings["identity_registration_number"]; !ok {
			return nil, codes.Refuse(codes.OriginRefused, "Source returned no identity_registration_number binding", nil).With("binding", "identity_registration_number")
		} else if got != l.cfg.Station {
			return nil, codes.Refuse(codes.OriginRefused, "Source registration differs from station", nil).With("binding", "identity_registration_number")
		}
	} else {
		nonce, err = l.nonce()
		if err != nil {
			return nil, err
		}
	}
	bindID, err := l.nonce()
	if err != nil {
		return nil, err
	}
	// H2 step 4: one fresh direct Keymaker connection and its hello, then the
	// durable record. Hello carries no mutation: a connect or hello failure
	// is run_bind_unavailable with no record.
	if l.d.Keymaker == nil {
		return nil, codes.Refuse(codes.BindUnavailable, "no keymaker dialer", nil)
	}
	km, err := keymaker.Open(ctx, l.d.Keymaker, l.d.KeymakerWire, l.cfg.RequestTimeout)
	if err != nil {
		if codes.IsUnpinned(err) {
			return nil, err
		}
		return nil, codes.Refuse(codes.BindUnavailable, "keymaker connect or hello", err)
	}
	principalJSON, err := json.Marshal(struct {
		UID        uint32 `json:"uid"`
		Generation string `json:"generation"`
	}{inputPrincipal.UID, inputPrincipal.Generation})
	if err != nil {
		_ = km.Close()
		return nil, err
	}
	rec := &runrecord.Record{
		Version:             runrecord.Version,
		Kind:                runrecord.Kind,
		Key:                 nonce,
		State:               runrecord.Binding,
		LauncherUID:         l.cfg.LauncherUID,
		LauncherGeneration:  gen,
		StationRegistration: l.cfg.Station,
		InputPrincipal:      string(principalJSON),
		LaunchNonce:         nonce,
		BindRequestID:       bindID,
		ChildUID:            dec.ChildUID,
		Launcher:            &self,
	}
	req := keymaker.BindRequest{IdentityRegistrationNumber: registration, InputPrincipal: inputPrincipal}
	if parent == nil {
		rec.Origin = append([]byte(nil), validated.Origin...)
		rec.SourceIncarnation = validated.SourceIncarnation
		rec.PreparedID = validated.PreparedID
		req.Origin = validated.Origin
	} else {
		rec.Origin = []byte("null")
		rec.ParentRun = parent.RunID
		req.ParentRun = parent.RunID
	}
	// Deterministic local encoding can refuse before any record or mutation.
	if _, err := km.EncodeBind(bindID, req); err != nil {
		_ = km.Close()
		switch {
		case codes.IsUnpinned(err):
			return nil, err
		case errors.Is(err, source.ErrOrigin):
			return nil, codes.Refuse(codes.OriginRefused, "origin not encodable as an admission origin", err)
		}
		return nil, codes.Refuse(codes.BindUnknown, "bind request not encodable; nothing sent", err).With("record", "none").With("bind_sent", "false")
	}
	if err := l.d.Records.Create(rec); err != nil {
		_ = km.Close()
		_, exists := l.d.Records.Get(nonce)
		if exists || runrecord.Uncertain(err) {
			// The binding frame may be durable: restart would HOLD it, so the
			// uid stays quarantined.
			recorded = true
			return nil, durable(codes.BindUnknown, "binding record commit failed; bind not sent", err).With("key", nonce).With("record", "uncertain").With("bind_sent", "false")
		}
		return nil, durable(codes.BindUnknown, "binding record refused; bind not sent", err).With("record", "none").With("bind_sent", "false")
	}
	recorded = true
	// H2 step 5 and 6. The bind frame is written only now.
	res, _, err := km.Bind(bindID, req)
	if err != nil {
		_ = km.Close()
		if errors.Is(err, keymaker.ErrRefused) && codes.Is(err, codes.BindRefused) {
			if _, uerr := l.d.Records.Update(nonce, func(r *runrecord.Record) error { r.State = runrecord.Refused; return nil }); uerr != nil {
				// The record is still binding: restart will HOLD it.
				return nil, durable(codes.BindUnknown, "no-effect bind refusal not durably recorded; record stays binding", errors.Join(uerr, err)).With("key", nonce)
			}
			// Nothing was bound or spawned, but the uid still passes the release
			// gate: proof plus an operator release, since no end and no export
			// can exist for a run that never ran.
			_, _ = l.d.Quarantine.Release(nonce, dec.ChildUID, false, sweep.Enumerate(l.view(), dec.ChildUID))
			return nil, err
		}
		// HELD: the record stays binding.
		if _, ok := codes.CodeOf(err); !ok {
			return nil, codes.Refuse(codes.BindUnknown, "bind failed locally after the binding record; run HELD", err).With("key", nonce)
		}
		return nil, err
	}
	if _, err := l.d.Records.Update(nonce, func(r *runrecord.Record) error {
		r.State = runrecord.Bound
		r.RunID = res.RunID
		return nil
	}); err != nil {
		// Live at Keymaker, binding locally: exactly D4 UNKNOWN, H6 HOLD.
		_ = km.Close()
		return nil, durable(codes.BindUnknown, "Keymaker bound the run but the bound record is not durable; run HELD", err).With("key", nonce).With("run_id", res.RunID)
	}
	return &Run{l: l, Key: nonce, RunID: res.RunID, ChildUID: dec.ChildUID, km: km}, nil
}

func (r *Run) durablyBound() (*runrecord.Record, error) {
	rec, ok := r.l.d.Records.Get(r.Key)
	if !ok || rec.State != runrecord.Bound || rec.RunID == "" {
		return nil, ErrNotBound
	}
	if r.fenced || r.l.d.Records.Fenced(r.Key) {
		return nil, ErrFenced
	}
	return rec, nil
}

// Spawn is the first H3 gate: only after the record is bound and durable may
// spawn run. spawn receives the leased uid and returns the child incarnation,
// which is recorded before return.
func (r *Run) Spawn(spawn func(childUID uint32) (incarnation.Incarnation, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.durablyBound()
	if err != nil {
		return err
	}
	if rec.Child != nil {
		return errors.New("launcher: child already recorded")
	}
	// spawn is the helper's spawn mode: recorded before it runs.
	r.l.d.Quarantine.MarkHelper(r.ChildUID)
	child, err := spawn(r.ChildUID)
	if err != nil {
		return err
	}
	_, err = r.l.d.Records.Update(r.Key, func(x *runrecord.Record) error { x.Child = &child; return nil })
	return err
}

// ReleaseInput is the second H3 gate: the record must be bound, durable and
// carry the child; the release is recorded durably before release runs.
func (r *Run) ReleaseInput(release func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.durablyBound()
	if err != nil {
		return err
	}
	if rec.Child == nil {
		return errors.Join(ErrNotBound, errors.New("launcher: child incarnation not recorded"))
	}
	if rec.InputReleased {
		return errors.New("launcher: input already released")
	}
	if _, err := r.l.d.Records.Update(r.Key, func(x *runrecord.Record) error { x.InputReleased = true; return nil }); err != nil {
		return err
	}
	return release()
}

// EndOutcome reports H5.
type EndOutcome struct {
	// Verdict is the exit decision of Run.End. RetryEnd and Restart set it
	// only together with an error.
	Verdict codes.Verdict
	// Acknowledged is true only when the exact ended result was read AND the
	// record durably moved to ended.
	Acknowledged bool
	// TeardownErr is the local teardown error, if any; teardown always ran.
	TeardownErr error
	// RecordErr is a failure to record teardown or the acknowledgement.
	RecordErr error
	// MustExit is the journal's process-exit duty after a sync failure.
	MustExit bool
	// Sweep is the uid sweep outcome after teardown.
	Sweep sweep.Outcome
	// Release is proof AND (operator release OR (end ACK AND work-product
	// export)); UIDReleased is its outcome.
	Release     sweep.ReleaseDecision
	UIDReleased bool
}

// endUnknown builds run_end_unknown for a failed durable step of the end.
func endUnknown(op string, cause error, key, runID string) *codes.Refusal {
	r := durable(codes.EndUnknown, op, cause).With("key", key)
	if runID != "" {
		r.With("run_id", runID)
	}
	return r
}

// recordEnd records teardown and, when the exact acknowledgement was read,
// the ended state. It returns whether the record is durably ended and the
// refusal that replaces endErr when a durable step failed.
func (l *Launcher) recordEnd(key, runID string, ackRead bool, endErr error, out *EndOutcome) (bool, error) {
	if _, ok := l.d.Records.Get(key); !ok {
		return false, endErr
	}
	if _, err := l.d.Records.Update(key, func(x *runrecord.Record) error { x.TeardownDone = true; return nil }); err != nil {
		out.RecordErr = err
		out.MustExit = out.MustExit || runrecord.MustExit(err)
		if endErr == nil {
			// Acknowledged on the wire, but not even teardown could be
			// recorded: the record stays ending and H6 retries the end.
			return false, endUnknown("acknowledged end not durably recorded; record stays ending", err, key, runID)
		}
		return false, endErr
	}
	if !ackRead || endErr != nil {
		return false, endErr
	}
	if _, err := l.d.Records.Update(key, func(x *runrecord.Record) error {
		x.State = runrecord.Ended
		x.EndAcknowledged = true
		return nil
	}); err != nil {
		out.RecordErr = err
		out.MustExit = out.MustExit || runrecord.MustExit(err)
		return false, endUnknown("acknowledged end not durably recorded; record stays ending", err, key, runID)
	}
	return true, nil
}

// End is H5: fence, record ending with the exact end request, send the end,
// run teardown always, move to ended only on the exact acknowledgement, then
// sweep the uid and evaluate the release gate.
//
// Once the run is fenced, local teardown, the close of the Keymaker
// connection, the uid sweep and the quarantine evaluation run on EVERY
// return path of the first call. If the ending intent cannot be made
// durable, no end is sent (there would be no durable same-ID intent to
// retry) and the result is run_end_unknown with the cause. The release gate
// sees an acknowledgement only when the exact ack was read and recorded.
//
// The returned error is the refusal that decides the exit; the verdict is
// computed from it and the child's status. An acknowledged end without a
// known child status is codes.ErrUnclassified, never a zero verdict with a
// nil error. A second End on the same Run returns ErrEndCalled and does
// nothing.
func (r *Run) End(ctx context.Context, childStatus int, childKnown bool, teardown func() error) (EndOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out EndOutcome
	if r.endDone {
		return out, ErrEndCalled
	}
	r.fenced = true
	r.endDone = true
	l := r.l

	// H5 step 2: the durable ending intent.
	var endErr error
	rec, ok := l.d.Records.Get(r.Key)
	switch {
	case !ok || rec.RunID == "":
		endErr = codes.Refuse(codes.EndUnknown, "no bound record for this run; end not sent", ErrNotBound).With("key", r.Key)
	case rec.State == runrecord.Bound:
		endErr = func() error {
			endID, err := l.nonce()
			if err != nil {
				return endUnknown("end request id unavailable; end not sent", err, r.Key, rec.RunID)
			}
			if r.km == nil {
				return endUnknown("no assigned connection to encode the end; end not sent", nil, r.Key, rec.RunID)
			}
			body, err := r.km.EncodeEnd(endID, keymaker.EndRequest{RunID: rec.RunID})
			if err != nil {
				return endUnknown("end request not encodable; end not sent", err, r.Key, rec.RunID)
			}
			next, err := l.d.Records.Update(r.Key, func(x *runrecord.Record) error {
				x.State = runrecord.Ending
				x.EndRequestID = endID
				x.EndRequestBody = body
				return nil
			})
			if err != nil {
				out.RecordErr = err
				out.MustExit = runrecord.MustExit(err)
				return endUnknown("ending intent not durable; end not sent", err, r.Key, rec.RunID)
			}
			rec = next
			return nil
		}()
	case rec.State == runrecord.Ending:
	default:
		endErr = codes.Refuse(codes.EndUnknown, "record is neither bound nor ending; end not sent", nil).With("key", r.Key).With("state", string(rec.State))
	}

	// H5 step 3: send the end only with a durable intent.
	ackRead := false
	if endErr == nil {
		if r.km != nil && r.km.Live() {
			_, _, endErr = r.km.EndExact(rec.EndRequestID, keymaker.EndRequest{RunID: rec.RunID}, rec.EndRequestBody)
			ackRead = endErr == nil
		} else {
			endErr = codes.Refuse(codes.EndUnknown, "no live assigned connection for end", nil).With("run_id", rec.RunID)
		}
	}

	// H5 step 4: local teardown always runs, on the error path too.
	if teardown != nil {
		out.TeardownErr = teardown()
	}
	if r.km != nil {
		_ = r.km.Close()
	}
	runID := ""
	if rec != nil {
		runID = rec.RunID
	}
	out.Acknowledged, endErr = l.recordEnd(r.Key, runID, ackRead, endErr, &out)

	// The uid sweep and the quarantine evaluation, on every path.
	out.Sweep = sweep.Sweep(ctx, l.view(), r.ChildUID, l.d.Killer, l.cfg.SweepBudget)
	var rerr error
	out.Release, rerr = l.d.Quarantine.Release(r.Key, r.ChildUID, out.Acknowledged, out.Sweep.Final)
	out.UIDReleased = rerr == nil

	v, verr := codes.Exit(endErr, childStatus, childKnown)
	if verr != nil {
		if endErr == nil {
			return out, errors.Join(verr, errors.New("launcher: acknowledged end without a known child status"))
		}
		return out, errors.Join(endErr, verr)
	}
	out.Verdict = v
	return out, endErr
}

// RetryEnd retries the end of an ending record on a fresh connection with the
// recorded exact request. Teardown here is the uid sweep. Every failure of a
// durable step is run_end_unknown with the cause.
func (l *Launcher) RetryEnd(ctx context.Context, key string) (EndOutcome, error) {
	rec, ok := l.d.Records.Get(key)
	if !ok || rec.State != runrecord.Ending {
		return EndOutcome{}, errors.New("launcher: retry end requires an ending record")
	}
	return l.finishEnd(ctx, key, rec, nil)
}

// finishEnd sends the recorded exact end on a fresh connection unless
// intentErr says there is no durable intent, then always sweeps the uid,
// records the outcome and evaluates the release gate.
func (l *Launcher) finishEnd(ctx context.Context, key string, rec *runrecord.Record, intentErr error) (EndOutcome, error) {
	var out EndOutcome
	endErr := intentErr
	ackRead := false
	if endErr == nil {
		if l.d.Keymaker == nil {
			endErr = codes.Refuse(codes.EndUnknown, "no keymaker dialer", nil).With("run_id", rec.RunID)
		} else if km, err := keymaker.Open(ctx, l.d.Keymaker, l.d.KeymakerWire, l.cfg.RequestTimeout); err != nil {
			endErr = codes.Refuse(codes.EndUnknown, "keymaker connect or hello", err).With("run_id", rec.RunID)
		} else {
			_, _, endErr = km.EndExact(rec.EndRequestID, keymaker.EndRequest{RunID: rec.RunID}, rec.EndRequestBody)
			ackRead = endErr == nil
			_ = km.Close()
		}
	}
	out.Sweep = sweep.Sweep(ctx, l.view(), rec.ChildUID, l.d.Killer, l.cfg.SweepBudget)
	out.Acknowledged, endErr = l.recordEnd(key, rec.RunID, ackRead, endErr, &out)
	var rerr error
	out.Release, rerr = l.d.Quarantine.Release(key, rec.ChildUID, out.Acknowledged, out.Sweep.Final)
	out.UIDReleased = rerr == nil
	if endErr != nil {
		v, verr := codes.Exit(endErr, 0, false)
		if verr != nil {
			return out, errors.Join(endErr, verr)
		}
		out.Verdict = v
	}
	return out, endErr
}

// Report is one H6 table application.
type Report struct {
	Key      string
	State    runrecord.State
	Action   runrecord.RestartAction
	Code     codes.Code
	Outcome  *EndOutcome
	Err      error
	Orphaned bool
	// UIDReleased reports, for a record that was already terminal at
	// restart, whether its uid left quarantine in this restart.
	UIDReleased bool
}

// releaseTerminal re-evaluates, at restart, a uid on which every record is
// terminal. The quarantine is in memory, so without this every uid a
// previous process had released would stay unleasable. Every input is
// re-derived: a fresh zero-process proof, and for each ended record the
// release gate with its recorded acknowledgement. A refused record never
// reached bound, so nothing ever ran under its lease: the proof alone
// returns it. One failing record keeps the uid quarantined.
func (l *Launcher) releaseTerminal(uid uint32, recs []*runrecord.Record) bool {
	e := sweep.Enumerate(l.view(), uid)
	if !e.Proved() {
		return false
	}
	endedKey := ""
	for _, rec := range recs {
		if rec.State != runrecord.Ended {
			continue
		}
		if !l.d.Quarantine.Gate.Decide(rec.Key, uid, rec.EndAcknowledged, e).Release {
			return false
		}
		endedKey = rec.Key
	}
	if endedKey != "" {
		_, err := l.d.Quarantine.Release(endedKey, uid, true, e)
		return err == nil
	}
	return l.d.Quarantine.ReturnUnused(uid, e) == nil
}

// Restart is H6: the startup sweep and the reaper table. Every persisted run
// is fenced by the store. A record is orphaned only when its recorded
// launcher incarnation is dead; that this launcher restarted is not evidence.
//
// Every recorded uid starts out held. A uid on which every record is terminal
// is then re-evaluated (releaseTerminal); a uid with any binding, bound or
// ending record stays held unless the end path of this restart releases it.
func (l *Launcher) Restart(ctx context.Context) []Report {
	if ctx == nil {
		ctx = context.Background()
	}
	keys := l.d.Records.Keys()
	recs := make(map[string]*runrecord.Record, len(keys))
	terminal := map[uint32][]*runrecord.Record{}
	unsettled := map[uint32]bool{}
	for _, key := range keys {
		rec, ok := l.d.Records.Get(key)
		if !ok {
			continue
		}
		recs[key] = rec
		l.d.Quarantine.Hold(rec.ChildUID)
		if rec.Validate() == nil && rec.Terminal() {
			terminal[rec.ChildUID] = append(terminal[rec.ChildUID], rec)
		} else {
			unsettled[rec.ChildUID] = true
		}
	}
	released := map[uint32]bool{}
	for uid, list := range terminal {
		if !unsettled[uid] {
			released[uid] = l.releaseTerminal(uid, list)
		}
	}
	var reports []Report
	for _, key := range keys {
		rec, ok := recs[key]
		if !ok {
			continue
		}
		rep := Report{Key: key, State: rec.State}
		var alive bool
		var lerr error
		if rec.Launcher != nil {
			alive, lerr = incarnation.Alive(l.d.Incarnations, *rec.Launcher)
		} else {
			lerr = incarnation.ErrUnreadable
		}
		rep.Action = runrecord.Decide(rec, alive, lerr)
		switch rep.Action {
		case runrecord.ActionHold:
			rep.Code = codes.BindUnknown
			rep.Err = codes.Refuse(codes.BindUnknown, "binding record HELD at restart", nil).With("key", key)
		case runrecord.ActionEndAndKill, runrecord.ActionEndThenSweep:
			rep.Orphaned = true
			out, err := l.endOrphan(ctx, key, rec)
			rep.Outcome = &out
			rep.Err = err
			if err != nil {
				rep.Code, _ = codes.CodeOf(err)
			}
		case runrecord.ActionRetryEnd:
			out, err := l.RetryEnd(ctx, key)
			rep.Outcome = &out
			rep.Err = err
			if err != nil {
				rep.Code, _ = codes.CodeOf(err)
			}
		case runrecord.ActionUndetermined:
			rep.Err = lerr
		case runrecord.ActionNeverAdopt:
			rep.UIDReleased = released[rec.ChildUID]
		}
		reports = append(reports, rep)
	}
	return reports
}

// endOrphan moves a bound orphan to ending with a fresh exact end request,
// then sends it. If the ending intent cannot be made durable no end is sent,
// but the uid is still swept and the gate evaluated: the orphan is killed
// even when the store refuses the update.
func (l *Launcher) endOrphan(ctx context.Context, key string, rec *runrecord.Record) (EndOutcome, error) {
	var recordErr error
	intentErr := func() error {
		endID, err := l.nonce()
		if err != nil {
			return endUnknown("end request id unavailable; end not sent", err, key, rec.RunID)
		}
		body, err := l.d.KeymakerWire.Encode(keymaker.Request{ID: endID, TimeoutMS: l.cfg.RequestTimeout.Milliseconds(), Method: keymaker.MethodEnd, End: &keymaker.EndRequest{RunID: rec.RunID}})
		if err != nil {
			return endUnknown("end request not encodable; end not sent", err, key, rec.RunID)
		}
		next, err := l.d.Records.Update(key, func(x *runrecord.Record) error {
			x.State = runrecord.Ending
			x.EndRequestID = endID
			x.EndRequestBody = body
			return nil
		})
		if err != nil {
			recordErr = err
			return endUnknown("ending intent not durable; end not sent", err, key, rec.RunID)
		}
		rec = next
		return nil
	}()
	out, err := l.finishEnd(ctx, key, rec, intentErr)
	if recordErr != nil && out.RecordErr == nil {
		out.RecordErr = recordErr
	}
	out.MustExit = out.MustExit || runrecord.MustExit(recordErr)
	return out, err
}
