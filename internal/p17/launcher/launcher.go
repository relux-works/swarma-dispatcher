// Package launcher composes the P17 hooks H0, H1, H2, H3, H5 and H6 for the
// launcher L around the standalone clients: isolation, source, keymaker, the
// durable run record, incarnations and the uid sweep.
//
// It is the hook point the S2-M0 launch path calls: Begin runs H0, H1 and
// H2 and returns a bound run; Spawn and ReleaseInput are the H3 gates; End is
// H5; Restart is the H6 startup sweep and the reaper's table. Nothing here
// spawns a process, reads policy, or dispatches; those stay with S2-M0.
package launcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Config is the launcher's own configuration.
type Config struct {
	LauncherUID uint32
	// Station is the complete current station registration number.
	Station string
	// RequestTimeout is the Keymaker per-request timeout (1..2000 ms).
	RequestTimeout time.Duration
	BindAttempts   int
	// MaxRestarts bounds H1 restarts after a lost validate.
	MaxRestarts int
	// ProcRoot is the procfs root for the D5a proof; empty means /proc.
	ProcRoot    string
	SweepBudget sweep.Budget
}

// Deps are the injected clients and seams. Nil seams are the refusing
// defaults; Source, Records and Incarnations are required.
type Deps struct {
	Placement    isolation.Placement
	Source       *source.Client
	Generations  source.Generations
	Keymaker     keymaker.Dialer
	KeymakerWire keymaker.Wire
	Records      *runrecord.Store
	Incarnations incarnation.Reader
	Killer       sweep.Killer
	Quarantine   *sweep.Quarantine
	// Archival and OperatorRelease are the two R_i release seams (book ch. 07,
	// Core G1). Nil is the refusing default; they are installed into the
	// Quarantine gate when it has none.
	Archival        sweep.Archival
	OperatorRelease sweep.OperatorRelease
	Nonce           func() (string, error)
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
	// ErrKeymakerUnavailable reports a Keymaker connection failure before any
	// request was sent. r2.6 assigns no code to this case; it is not UNKNOWN,
	// because nothing was sent, and it is reported as its own typed error.
	ErrKeymakerUnavailable = errors.New("launcher: keymaker connection unavailable before bind")
	// ErrParent refuses a child run whose parent record is not live and bound.
	ErrParent = errors.New("launcher: parent run is not a live bound record of this launcher")
)

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
	if cfg.BindAttempts <= 0 {
		cfg.BindAttempts = keymaker.DefaultBindAttempts
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
	if d.Archival == nil {
		d.Archival = sweep.UnpinnedArchival{}
	}
	if d.OperatorRelease == nil {
		d.OperatorRelease = sweep.UnpinnedOperatorRelease{}
	}
	if d.Quarantine == nil {
		d.Quarantine = &sweep.Quarantine{}
	}
	if d.Quarantine.Gate.Archival == nil {
		d.Quarantine.Gate.Archival = d.Archival
	}
	if d.Quarantine.Gate.Operator == nil {
		d.Quarantine.Gate.Operator = d.OperatorRelease
	}
	if d.Nonce == nil {
		d.Nonce = defaultNonce
	}
	return &Launcher{cfg: cfg, d: d}, nil
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

// Begin runs H0, H1 and H2 for a root run and returns a bound run. On every
// refusal nothing is launched; a HELD bind leaves the record in binding.
func (l *Launcher) Begin(ctx context.Context, inputPrincipal string) (*Run, error) {
	return l.begin(ctx, inputPrincipal, nil)
}

// BeginChild runs the same gates for a child run (H4 lifecycle binding only):
// parent_run comes from this launcher's live parent record, the origin is
// null and Keymaker inherits the immutable origin. There is no parameter by
// which an agent could supply parent_run.
func (l *Launcher) BeginChild(ctx context.Context, parent *Run, inputPrincipal string) (*Run, error) {
	if parent == nil || parent.l != l {
		return nil, ErrParent
	}
	rec, ok := l.d.Records.Get(parent.Key)
	if !ok || rec.State != runrecord.Bound || rec.RunID == "" || rec.RunID != parent.RunID || l.d.Records.Fenced(parent.Key) {
		return nil, ErrParent
	}
	return l.begin(ctx, inputPrincipal, rec)
}

func (l *Launcher) begin(ctx context.Context, inputPrincipal string, parent *runrecord.Record) (*Run, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if inputPrincipal == "" {
		return nil, errors.New("launcher: input principal required")
	}
	// H0 before any contact.
	dec, err := isolation.Check(l.d.Placement, l.cfg.LauncherUID)
	if err != nil {
		return nil, err
	}
	if err := l.d.Quarantine.Lease(dec.ChildUID); err != nil {
		return nil, codes.Refuse(codes.IsolationUnqualified, "child uid quarantined", err)
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
		// H1 and the validate step of H2, restarting from H1 on a lost validate.
		for restarts := 0; ; restarts++ {
			nonce, err = l.nonce()
			if err != nil {
				return nil, err
			}
			prepared, err := l.d.Source.Prepare(ctx, nonce)
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
	} else {
		nonce, err = l.nonce()
		if err != nil {
			return nil, err
		}
	}
	// H2 step 4: one fresh direct Keymaker connection, then the durable record.
	if l.d.Keymaker == nil {
		return nil, errors.Join(ErrKeymakerUnavailable, errors.New("launcher: no keymaker dialer"))
	}
	km, err := keymaker.Open(ctx, l.d.Keymaker, l.d.KeymakerWire, l.cfg.RequestTimeout)
	if err != nil {
		return nil, errors.Join(ErrKeymakerUnavailable, err)
	}
	bindID, err := l.nonce()
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
		InputPrincipal:      inputPrincipal,
		LaunchNonce:         nonce,
		BindRequestID:       bindID,
		ChildUID:            dec.ChildUID,
		Launcher:            &self,
	}
	req := keymaker.BindRequest{IdentityRegistrationNumber: l.cfg.Station, InputPrincipal: inputPrincipal}
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
	if err := l.d.Records.Create(rec); err != nil {
		_ = km.Close()
		return nil, err
	}
	// H2 step 5 and 6.
	res, _, err := km.Bind(bindID, req, l.cfg.BindAttempts)
	if err != nil {
		_ = km.Close()
		if errors.Is(err, keymaker.ErrRefused) {
			if _, uerr := l.d.Records.Update(nonce, func(r *runrecord.Record) error { r.State = runrecord.Refused; return nil }); uerr != nil {
				return nil, errors.Join(err, uerr)
			}
			// Nothing was bound or spawned, but the uid still passes the release
			// gate: proof plus an operator release, since no end and no archival
			// can exist for a run that never ran.
			_, _ = l.d.Quarantine.Release(nonce, dec.ChildUID, false, sweep.Enumerate(l.cfg.ProcRoot, dec.ChildUID))
			return nil, codes.Refuse(codes.OriginRefused, "keymaker refused bind", err)
		}
		if codes.IsUnpinned(err) && km.Requests() == 0 {
			// Nothing was sent: the seam refused before the request existed.
			if _, uerr := l.d.Records.Update(nonce, func(r *runrecord.Record) error { r.State = runrecord.Refused; return nil }); uerr != nil {
				return nil, errors.Join(err, uerr)
			}
			_, _ = l.d.Quarantine.Release(nonce, dec.ChildUID, false, sweep.Enumerate(l.cfg.ProcRoot, dec.ChildUID))
			return nil, err
		}
		// HELD or undecodable: the record stays binding.
		return nil, err
	}
	if _, err := l.d.Records.Update(nonce, func(r *runrecord.Record) error {
		r.State = runrecord.Bound
		r.RunID = res.RunID
		return nil
	}); err != nil {
		_ = km.Close()
		return nil, err
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
	Verdict codes.Verdict
	// Acknowledged is true only on the exact ended result.
	Acknowledged bool
	// TeardownErr is the local teardown error, if any; teardown always ran.
	TeardownErr error
	// Sweep is the uid sweep outcome after teardown.
	Sweep sweep.Outcome
	// Release is the R_i release gate decision (proof AND archival or
	// operator release); UIDReleased is its outcome.
	Release     sweep.ReleaseDecision
	UIDReleased bool
}

// End is H5: fence, record ending with the exact end request, send the end,
// run teardown always, move to ended only on the exact acknowledgement, then
// sweep the uid. The returned error is the refusal that decides the exit;
// the verdict is computed from it and the child's status.
func (r *Run) End(ctx context.Context, childStatus int, childKnown bool, teardown func() error) (EndOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fenced = true
	var out EndOutcome
	l := r.l
	rec, ok := l.d.Records.Get(r.Key)
	if !ok || rec.RunID == "" {
		return out, ErrNotBound
	}
	var endErr error
	if rec.State == runrecord.Bound {
		endID, err := l.nonce()
		if err != nil {
			return out, err
		}
		body, err := r.km.EncodeEnd(endID, keymaker.EndRequest{RunID: rec.RunID})
		if err != nil {
			return out, err
		}
		if _, err := l.d.Records.Update(r.Key, func(x *runrecord.Record) error {
			x.State = runrecord.Ending
			x.EndRequestID = endID
			x.EndRequestBody = body
			return nil
		}); err != nil {
			return out, err
		}
		rec, _ = l.d.Records.Get(r.Key)
	}
	if rec.State != runrecord.Ending {
		return out, errors.New("launcher: end on a record that is neither bound nor ending")
	}
	if r.km != nil && r.km.Live() {
		_, _, endErr = r.km.End(rec.EndRequestID, keymaker.EndRequest{RunID: rec.RunID})
	} else {
		endErr = codes.Refuse(codes.EndUnknown, "no live assigned connection for end", nil).With("run_id", rec.RunID)
	}
	// Local teardown always runs, on the error path too, and is recorded.
	if teardown != nil {
		out.TeardownErr = teardown()
	}
	if r.km != nil {
		_ = r.km.Close()
	}
	if _, err := l.d.Records.Update(r.Key, func(x *runrecord.Record) error { x.TeardownDone = true; return nil }); err != nil {
		return out, err
	}
	if endErr == nil {
		if _, err := l.d.Records.Update(r.Key, func(x *runrecord.Record) error {
			x.State = runrecord.Ended
			x.EndAcknowledged = true
			return nil
		}); err != nil {
			return out, err
		}
		out.Acknowledged = true
	}
	out.Sweep = sweep.Sweep(ctx, l.cfg.ProcRoot, r.ChildUID, l.d.Killer, l.cfg.SweepBudget)
	var rerr error
	out.Release, rerr = l.d.Quarantine.Release(r.Key, r.ChildUID, out.Acknowledged, out.Sweep.Final)
	out.UIDReleased = rerr == nil
	if endErr != nil || childKnown {
		v, verr := codes.Exit(endErr, childStatus, childKnown)
		if verr != nil {
			return out, errors.Join(endErr, verr)
		}
		out.Verdict = v
	}
	return out, endErr
}

// RetryEnd retries the end of an ending record on a fresh connection with the
// recorded exact request. Teardown here is the uid sweep.
func (l *Launcher) RetryEnd(ctx context.Context, key string) (EndOutcome, error) {
	var out EndOutcome
	rec, ok := l.d.Records.Get(key)
	if !ok || rec.State != runrecord.Ending {
		return out, errors.New("launcher: retry end requires an ending record")
	}
	var endErr error
	if l.d.Keymaker == nil {
		endErr = codes.Refuse(codes.EndUnknown, "no keymaker dialer", ErrKeymakerUnavailable)
	} else if km, err := keymaker.Open(ctx, l.d.Keymaker, l.d.KeymakerWire, l.cfg.RequestTimeout); err != nil {
		endErr = codes.Refuse(codes.EndUnknown, "keymaker connect", err).With("run_id", rec.RunID)
	} else {
		_, _, endErr = km.End(rec.EndRequestID, keymaker.EndRequest{RunID: rec.RunID})
		_ = km.Close()
	}
	out.Sweep = sweep.Sweep(ctx, l.cfg.ProcRoot, rec.ChildUID, l.d.Killer, l.cfg.SweepBudget)
	if _, err := l.d.Records.Update(key, func(x *runrecord.Record) error { x.TeardownDone = true; return nil }); err != nil {
		return out, err
	}
	if endErr == nil {
		if _, err := l.d.Records.Update(key, func(x *runrecord.Record) error {
			x.State = runrecord.Ended
			x.EndAcknowledged = true
			return nil
		}); err != nil {
			return out, err
		}
		out.Acknowledged = true
	}
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
}

// Restart is H6: the startup sweep and the reaper table. Every persisted run
// is fenced by the store. A record is orphaned only when its recorded
// launcher incarnation is dead; that this launcher restarted is not evidence.
func (l *Launcher) Restart(ctx context.Context) []Report {
	if ctx == nil {
		ctx = context.Background()
	}
	var reports []Report
	for _, key := range l.d.Records.Keys() {
		rec, ok := l.d.Records.Get(key)
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
		l.d.Quarantine.Hold(rec.ChildUID)
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
		}
		reports = append(reports, rep)
	}
	return reports
}

// endOrphan moves a bound orphan to ending with a fresh exact end request,
// then follows RetryEnd.
func (l *Launcher) endOrphan(ctx context.Context, key string, rec *runrecord.Record) (EndOutcome, error) {
	var out EndOutcome
	endID, err := l.nonce()
	if err != nil {
		return out, err
	}
	body, err := l.d.KeymakerWire.Encode(keymaker.Request{ID: endID, Method: keymaker.MethodEnd, End: &keymaker.EndRequest{RunID: rec.RunID}})
	if err != nil {
		return out, err
	}
	if _, err := l.d.Records.Update(key, func(x *runrecord.Record) error {
		x.State = runrecord.Ending
		x.EndRequestID = endID
		x.EndRequestBody = body
		return nil
	}); err != nil {
		return out, err
	}
	return l.RetryEnd(ctx, key)
}
