//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

var (
	luid = uint32(os.Getuid())
	cuid = uint32(2001)
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func journalCfg(t *testing.T) journal.Config {
	t.Helper()
	d := t.TempDir()
	must(t, os.Chmod(d, 0o700))
	for _, n := range []string{"records", "inputs"} {
		must(t, os.Mkdir(filepath.Join(d, n), 0o700))
		must(t, os.Chmod(filepath.Join(d, n), 0o700))
	}
	var fs syscall.Statfs_t
	must(t, syscall.Statfs(d, &fs))
	if fs.Type != 0xEF53 && fs.Type != 0x58465342 {
		t.Skipf("real storage UNRUN: fixture filesystem type %#x is neither ext4 nor XFS", fs.Type)
	}
	m := uint32(0o077)
	return journal.Config{Directory: d, Records: "records", Inputs: "inputs", DeclaredUmask: &m, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 64 << 20}
}

type env struct {
	t         *testing.T
	jcfg      journal.Config
	store     *runrecord.Store
	src       *fakes.SourceScript
	srcDialer *fakes.SourceDialer
	km        *fakes.KeymakerScript
	kmDialer  *fakes.KeymakerDialer
	proc      *fakes.ProcTree
	killer    *fakes.Killer
	quar      *sweep.Quarantine
	archival  sweep.StaticArchival
	operator  sweep.StaticOperatorRelease
	placement isolation.Placement
	cfg       launcher.Config
	deps      launcher.Deps
	l         *launcher.Launcher

	mu     sync.Mutex
	nonces int
}

// seqPlacement leases a fresh uid per run, as a real R_i pool would.
type seqPlacement struct {
	mu   sync.Mutex
	next uint32
}

func (p *seqPlacement) ChildUID(uint32) (uint32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	uid := p.next
	p.next++
	return uid, nil
}

func (e *env) nonce() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nonces++
	return fmt.Sprintf("n-%d", e.nonces), nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, jcfg: journalCfg(t)}
	st, err := runrecord.Initialize(e.jcfg)
	must(t, err)
	t.Cleanup(func() { _ = st.Close() })
	e.store = st
	root := t.TempDir()
	must(t, os.Chmod(root, 0o700))
	dir, stop, err := fakes.ControlDir(root, nil)
	must(t, err)
	t.Cleanup(stop)
	e.src = &fakes.SourceScript{}
	e.srcDialer = &fakes.SourceDialer{Script: e.src, Peer: source.Credential{UID: luid, PID: 1}}
	e.km = &fakes.KeymakerScript{}
	e.kmDialer = &fakes.KeymakerDialer{Script: e.km}
	e.proc, err = fakes.NewProcTree(t.TempDir(), "boot-1")
	must(t, err)
	must(t, e.proc.Add(os.Getpid(), [4]uint32{luid, luid, luid, luid}, 1))
	e.killer = &fakes.Killer{Tree: e.proc}
	e.archival = sweep.StaticArchival{}
	e.operator = sweep.StaticOperatorRelease{}
	e.quar = &sweep.Quarantine{Gate: sweep.ReleaseGate{Archival: e.archival, Operator: e.operator}}
	e.placement = isolation.Fixed(cuid)
	e.cfg = launcher.Config{
		LauncherUID:    luid,
		Station:        "station-1",
		RequestTimeout: 100 * time.Millisecond,
		BindAttempts:   3,
		MaxRestarts:    2,
		ProcRoot:       e.proc.Root,
		SweepBudget:    sweep.Budget{Attempts: 3},
	}
	e.deps = launcher.Deps{
		Source: &source.Client{
			Pins:        source.Pins{Root: root, ControlDir: dir, SourceUID: luid, SourceGeneration: "gen-a"},
			Dialer:      e.srcDialer,
			Wire:        fakes.SourceWire{},
			Generations: source.StaticGenerations{luid: "gen-a"},
			Budget:      500 * time.Millisecond,
		},
		Generations:  source.StaticGenerations{luid: "gen-a"},
		Keymaker:     e.kmDialer,
		KeymakerWire: fakes.KeymakerWire{},
		Incarnations: e.proc.Incarnations(),
		Nonce:        e.nonce,
	}
	return e
}

// build creates the launcher from the current env fields.
func (e *env) build() *launcher.Launcher {
	e.t.Helper()
	e.deps.Placement = e.placement
	e.deps.Records = e.store
	if e.deps.Killer == nil {
		e.deps.Killer = e.killer
	}
	e.deps.Quarantine = e.quar
	l, err := launcher.New(e.cfg, e.deps)
	must(e.t, err)
	e.l = l
	return l
}

// reopen closes the store and opens it again, as a restarted launcher would.
func (e *env) reopen() {
	e.t.Helper()
	must(e.t, e.store.Close())
	st, err := runrecord.Open(e.jcfg)
	must(e.t, err)
	e.t.Cleanup(func() { _ = st.Close() })
	e.store = st
}

func (e *env) spawnChild(pid int) func(uint32) (incarnation.Incarnation, error) {
	return func(uid uint32) (incarnation.Incarnation, error) {
		if uid != cuid {
			return incarnation.Incarnation{}, errors.New("spawn under the wrong uid")
		}
		if err := e.proc.Add(pid, [4]uint32{uid, uid, uid, uid}, 42); err != nil {
			return incarnation.Incarnation{}, err
		}
		return e.proc.Incarnations().Read(pid)
	}
}

func (e *env) record(key string) *runrecord.Record {
	e.t.Helper()
	r, ok := e.store.Get(key)
	if !ok {
		e.t.Fatalf("no record %s", key)
	}
	return r
}

// Claim (H1, H2, positive control): Begin runs isolation, prepare, validate,
// one fresh Keymaker connection, a durable binding record and the bind; the
// record ends bound with the run id, the origin bytes as validate returned
// them, source_incarnation, prepared_id and the bind request id, and the
// bind request carried those origin bytes verbatim with parent_run "".
func TestBeginBindsDurably(t *testing.T) {
	e := newEnv(t)
	l := e.build()
	run, err := l.Begin(context.Background(), "input-1")
	must(t, err)
	rec := e.record(run.Key)
	if rec.State != runrecord.Bound || rec.RunID != "run-1" || run.RunID != "run-1" || !bytes.Equal(rec.Origin, fakes.ValidOrigin()) ||
		rec.SourceIncarnation != "inc-1" || rec.PreparedID != "prep-1" || rec.BindRequestID == "" || rec.ChildUID != cuid || rec.Launcher == nil || rec.Launcher.PID != os.Getpid() {
		t.Fatalf("record %+v", rec)
	}
	binds := e.km.Binds()
	if len(binds) != 1 || e.kmDialer.Dials() != 1 {
		t.Fatalf("binds=%d dials=%d", len(binds), e.kmDialer.Dials())
	}
	id, body, err := fakes.ParseBind(binds[0])
	must(t, err)
	if id != rec.BindRequestID || !bytes.Equal(body.Origin, rec.Origin) || body.ParentRun != "" || body.InputPrincipal != "input-1" || body.IdentityRegistrationNumber != "station-1" {
		t.Fatalf("bind %q %+v", id, body)
	}
	if _, err := os.Stat(filepath.Join(e.jcfg.Directory, "records", runrecord.RecordName(run.Key))); err != nil {
		t.Fatal(err)
	}
	if !e.quar.Held(cuid) {
		t.Fatal("leased uid not held")
	}
}

// Claim (D1, "the isolation refusal makes zero Source connections"; "with
// R_i equal to L, enforcing refuses"): an unpinned placement and a placement
// equal to L each refuse run_isolation_unqualified with zero Source and zero
// Keymaker connections and no record. Positive control: TestBeginBindsDurably.
func TestIsolationRefusalZeroContact(t *testing.T) {
	for _, p := range []isolation.Placement{isolation.Unpinned{}, isolation.Fixed(luid)} {
		e := newEnv(t)
		e.placement = p
		l := e.build()
		_, err := l.Begin(context.Background(), "input-1")
		if !codes.Is(err, codes.IsolationUnqualified) {
			t.Fatalf("%T: %v", p, err)
		}
		srcDials, _ := e.srcDialer.Dials()
		if srcDials != 0 || e.kmDialer.Dials() != 0 || len(e.store.Keys()) != 0 {
			t.Fatalf("%T: source=%d keymaker=%d records=%d", p, srcDials, e.kmDialer.Dials(), len(e.store.Keys()))
		}
	}
}

// Claim (D5a, "a new run never gets that uid"): a quarantined uid refuses the
// launch before any contact.
func TestQuarantinedUIDNeverLeased(t *testing.T) {
	e := newEnv(t)
	e.quar.Hold(cuid)
	l := e.build()
	_, err := l.Begin(context.Background(), "input-1")
	if !codes.Is(err, codes.IsolationUnqualified) || !errors.Is(err, sweep.ErrQuarantined) {
		t.Fatalf("%v", err)
	}
	if d, _ := e.srcDialer.Dials(); d != 0 || e.kmDialer.Dials() != 0 {
		t.Fatal("contact despite quarantine")
	}
}

// Claim (H3, "no input before a durable bound record"): ReleaseInput before
// the child is recorded refuses and does not call release; after Spawn the
// release is already durable in the record when release runs. The binding
// record plant is in the package-internal test.
func TestInputGates(t *testing.T) {
	e := newEnv(t)
	l := e.build()
	run, err := l.Begin(context.Background(), "input-1")
	must(t, err)
	called := false
	if err := run.ReleaseInput(func() error { called = true; return nil }); !errors.Is(err, launcher.ErrNotBound) || called {
		t.Fatalf("release before spawn: %v called=%v", err, called)
	}
	must(t, run.Spawn(e.spawnChild(777)))
	if rec := e.record(run.Key); rec.Child == nil || rec.Child.PID != 777 || rec.Child.UID != cuid || rec.InputReleased {
		t.Fatalf("after spawn %+v", rec)
	}
	if err := run.Spawn(e.spawnChild(778)); err == nil {
		t.Fatal("second spawn accepted")
	}
	must(t, run.ReleaseInput(func() error {
		if !e.record(run.Key).InputReleased {
			t.Error("release ran before the durable record")
		}
		called = true
		return nil
	}))
	if !called {
		t.Fatal("release not called")
	}
	if err := run.ReleaseInput(func() error { return nil }); err == nil {
		t.Fatal("second release accepted")
	}
}

// Claim (H2 step 6, "lost bind: retry on the same connection succeeds"): with
// the first bind response dropped the launcher retries the identical bytes on
// the same connection and binds; no second Keymaker connection is opened.
func TestLostBindRetrySameConnection(t *testing.T) {
	e := newEnv(t)
	e.km.BindMode = "drop-first"
	l := e.build()
	run, err := l.Begin(context.Background(), "input-1")
	must(t, err)
	binds := e.km.Binds()
	if len(binds) != 2 || !bytes.Equal(binds[0], binds[1]) || e.kmDialer.Dials() != 1 || e.record(run.Key).State != runrecord.Bound {
		t.Fatalf("binds=%d dials=%d", len(binds), e.kmDialer.Dials())
	}
}

// Claim (H2 step 6 and H6 row 1, "after connection loss the run is HELD,
// never bound blindly"): a Keymaker connection closed before the bind reply
// yields run_bind_unknown, the record stays binding, no new connection is
// dialed, and the restart sweep HOLDs it: no end is sent and the record is
// unchanged.
func TestBindConnectionGoneHeld(t *testing.T) {
	e := newEnv(t)
	e.km.BindMode = "close"
	l := e.build()
	_, err := l.Begin(context.Background(), "input-1")
	if !codes.Is(err, codes.BindUnknown) {
		t.Fatalf("%v", err)
	}
	keys := e.store.Keys()
	if len(keys) != 1 || e.record(keys[0]).State != runrecord.Binding || e.kmDialer.Dials() != 1 {
		t.Fatalf("keys=%v dials=%d", keys, e.kmDialer.Dials())
	}
	e.reopen()
	e.km.SetBindMode("")
	l = e.build()
	reports := l.Restart(context.Background())
	if len(reports) != 1 || reports[0].Action != runrecord.ActionHold || reports[0].Code != codes.BindUnknown || !codes.Is(reports[0].Err, codes.BindUnknown) {
		t.Fatalf("reports %+v", reports)
	}
	if len(e.km.Ends()) != 0 || e.kmDialer.Dials() != 1 || e.record(keys[0]).State != runrecord.Binding || !e.store.Fenced(keys[0]) {
		t.Fatal("held record was touched")
	}
	if !e.quar.Held(cuid) {
		t.Fatal("held run's uid not quarantined")
	}
}

// Claim: an exact Keymaker refusal of the bind is definite; the record moves
// to refused, which the restart table never adopts; the never-used uid stays
// quarantined until an operator release with the zero-process proof.
func TestBindRefusedDefinite(t *testing.T) {
	e := newEnv(t)
	e.km.BindMode = "refuse"
	l := e.build()
	_, err := l.Begin(context.Background(), "input-1")
	if !codes.Is(err, codes.OriginRefused) || !errors.Is(err, keymaker.ErrRefused) {
		t.Fatalf("%v", err)
	}
	keys := e.store.Keys()
	if len(keys) != 1 || e.record(keys[0]).State != runrecord.Refused || !e.quar.Held(cuid) {
		t.Fatalf("keys %v held=%v", keys, e.quar.Held(cuid))
	}
	// Only an explicit operator release frees the never-used uid (no end and
	// no archival can exist for it), and only with the zero-process proof.
	e.operator[keys[0]] = cuid
	if d, err := e.quar.Release(keys[0], cuid, false, sweep.Enumerate(e.proc.Root, cuid)); err != nil || !d.Release || e.quar.Held(cuid) {
		t.Fatalf("operator release: %+v %v", d, err)
	}
	e.reopen()
	l = e.build()
	if reports := l.Restart(context.Background()); len(reports) != 1 || reports[0].Action != runrecord.ActionNeverAdopt || len(e.km.Ends()) != 0 {
		t.Fatalf("%+v", reports)
	}
}

func endReady(t *testing.T, e *env) *launcher.Run {
	t.Helper()
	l := e.build()
	run, err := l.Begin(context.Background(), "input-1")
	must(t, err)
	must(t, run.Spawn(e.spawnChild(777)))
	must(t, run.ReleaseInput(func() error { return nil }))
	return run
}

// Claim (H5, positive control; release gate positive control): on the exact
// ended acknowledgement the record moves ending -> ended with the exact end
// request id and body, teardown ran, the launcher exits with the child's
// status, the uid sweep killed the child through the helper, and with
// archival evidence for this run the uid left quarantine.
func TestEndExactAck(t *testing.T) {
	e := newEnv(t)
	run := endReady(t, e)
	e.archival[run.Key] = true
	torn := false
	out, err := run.End(context.Background(), 3, true, func() error { torn = true; return nil })
	must(t, err)
	rec := e.record(run.Key)
	ends := e.km.Ends()
	if rec.State != runrecord.Ended || !rec.EndAcknowledged || !rec.TeardownDone || !torn || len(ends) != 1 || !bytes.Equal(ends[0], rec.EndRequestBody) || rec.EndRequestID == "" {
		t.Fatalf("record %+v ends=%d torn=%v", rec, len(ends), torn)
	}
	if out.Verdict.Status != 3 || !out.Acknowledged || !out.UIDReleased || out.Sweep.Err != nil || e.quar.Held(cuid) || !out.Release.Archived || !out.Release.Proved {
		t.Fatalf("outcome %+v held=%v", out, e.quar.Held(cuid))
	}
	if uids := e.killer.UIDs(); len(uids) != 1 || uids[0] != cuid {
		t.Fatalf("killer uids %v", uids)
	}
	if _, err := os.Stat(filepath.Join(e.proc.Root, "777")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("child survived the sweep")
	}
	if _, err := run.End(context.Background(), 3, true, nil); err == nil {
		t.Fatal("second end on an ended record accepted")
	}
}

// Claim (release gate, book ch. 07 / Core G1, negatives with the positive
// control in TestEndExactAck): an acknowledged end with zero processes but
// no archival evidence and no operator release keeps the uid quarantined and
// a new run never gets it; an unpinned archival seam (the default) behaves
// the same; an explicit operator release for this run then frees it, with a
// fresh zero-process proof.
func TestEndAckNoArchivalKeepsQuarantine(t *testing.T) {
	e := newEnv(t)
	run := endReady(t, e)
	out, err := run.End(context.Background(), 0, true, nil)
	must(t, err)
	if !out.Acknowledged || out.UIDReleased || !out.Release.Proved || out.Release.Archived || out.Release.OperatorReleased || !e.quar.Held(cuid) || e.record(run.Key).State != runrecord.Ended {
		t.Fatalf("outcome %+v held=%v", out, e.quar.Held(cuid))
	}
	if _, err := e.l.Begin(context.Background(), "input-2"); !codes.Is(err, codes.IsolationUnqualified) {
		t.Fatalf("new run received the quarantined uid: %v", err)
	}
	e.operator[run.Key] = cuid
	if d, err := e.quar.Release(run.Key, cuid, true, sweep.Enumerate(e.proc.Root, cuid)); err != nil || !d.Release || !d.OperatorReleased || e.quar.Held(cuid) {
		t.Fatalf("operator release: %+v %v", d, err)
	}
	// Unpinned default seams: same refusal, with the unpinned causes recorded.
	e = newEnv(t)
	e.deps.Archival = nil
	e.deps.OperatorRelease = nil
	e.quar = &sweep.Quarantine{}
	run = endReady(t, e)
	out, err = run.End(context.Background(), 0, true, nil)
	must(t, err)
	if out.UIDReleased || !codes.IsUnpinned(out.Release.ArchivalErr) || !codes.IsUnpinned(out.Release.OperatorErr) || !e.quar.Held(cuid) {
		t.Fatalf("unpinned seams: %+v", out)
	}
}

// Claim (H5 steps 3-4, D4; "lost end leads to ending and a retry; teardown
// happens on the error path"; "run_end_unknown exits 17, never the child
// status"): a dropped end response leaves the record ending and fenced with
// the exact request stored, teardown still ran, the exit is 17 with
// child_status 3 in the detail, input is refused afterwards and the uid stays
// quarantined; a retry on a fresh connection sends the identical bytes and
// moves the record to ended.
func TestLostEndStaysEndingThenRetry(t *testing.T) {
	e := newEnv(t)
	e.km.EndMode = "drop"
	run := endReady(t, e)
	e.archival[run.Key] = true
	torn := false
	out, err := run.End(context.Background(), 3, true, func() error { torn = true; return nil })
	if !codes.Is(err, codes.EndUnknown) || !torn {
		t.Fatalf("%v torn=%v", err, torn)
	}
	if out.Verdict.Status != 17 || out.Verdict.Detail["child_status"] != "3" || out.Acknowledged || out.UIDReleased || !e.quar.Held(cuid) {
		t.Fatalf("outcome %+v", out)
	}
	rec := e.record(run.Key)
	ends := e.km.Ends()
	if rec.State != runrecord.Ending || !rec.TeardownDone || rec.EndAcknowledged || len(ends) != 1 || !bytes.Equal(ends[0], rec.EndRequestBody) {
		t.Fatalf("record %+v ends=%d", rec, len(ends))
	}
	if err := run.ReleaseInput(func() error { return nil }); err == nil {
		t.Fatal("input released on a fenced ending run")
	}
	e.km.SetEndMode("")
	out, err = e.l.RetryEnd(context.Background(), run.Key)
	must(t, err)
	ends = e.km.Ends()
	if len(ends) != 2 || !bytes.Equal(ends[0], ends[1]) || e.kmDialer.Dials() != 2 {
		t.Fatalf("retry ends=%d dials=%d", len(ends), e.kmDialer.Dials())
	}
	if rec := e.record(run.Key); rec.State != runrecord.Ended || !rec.EndAcknowledged || !out.Acknowledged || !out.UIDReleased || e.quar.Held(cuid) {
		t.Fatalf("after retry %+v %+v", rec, out)
	}
}

// Claim ("EOF on Keymaker is NOT an end"; exact result only): EOF before the
// end reply, ended:false and an ended result for another run each leave the
// record ending with run_end_unknown.
func TestEndNotExactStaysEnding(t *testing.T) {
	for _, mode := range []string{"close", "not-ended", "other-run", "refuse"} {
		e := newEnv(t)
		e.km.EndMode = mode
		run := endReady(t, e)
		_, err := run.End(context.Background(), 0, true, nil)
		if !codes.Is(err, codes.EndUnknown) || e.record(run.Key).State != runrecord.Ending || !e.quar.Held(cuid) {
			t.Fatalf("%s: %v state=%s", mode, err, e.record(run.Key).State)
		}
	}
}

// Claim (D5c): an exact end ACK with a descendant still alive under R_i that
// the helper cannot kill leaves the uid quarantined and the sweep not
// proved, while the record is ended; with the helper working (positive
// control in TestEndExactAck) the uid is released.
func TestEndAckWithSurvivorKeepsQuarantine(t *testing.T) {
	e := newEnv(t)
	e.killer.Immortal = true
	run := endReady(t, e)
	e.archival[run.Key] = true                                // archival present: the proof alone blocks the release
	must(t, e.proc.Add(778, [4]uint32{cuid, 0, cuid, 0}, 43)) // setuid descendant
	out, err := run.End(context.Background(), 0, true, nil)
	must(t, err)
	if !out.Acknowledged || out.UIDReleased || !errors.Is(out.Sweep.Err, sweep.ErrNotProved) || !e.quar.Held(cuid) || e.record(run.Key).State != runrecord.Ended || !out.Release.Archived || out.Release.Proved {
		t.Fatalf("%+v held=%v", out, e.quar.Held(cuid))
	}
	if len(e.killer.UIDs()) != e.cfg.SweepBudget.Attempts {
		t.Fatalf("kill rounds %d", len(e.killer.UIDs()))
	}
	l := e.build()
	if _, err := l.Begin(context.Background(), "input-2"); !codes.Is(err, codes.IsolationUnqualified) {
		t.Fatalf("new run received the quarantined uid: %v", err)
	}
}

// Claim (D3): Rebind is refused in v1; the positive control is that Begin
// still binds.
func TestRebindRefused(t *testing.T) {
	e := newEnv(t)
	l := e.build()
	run, err := l.Begin(context.Background(), "input-1")
	must(t, err)
	if err := l.Rebind(run.Key); !errors.Is(err, launcher.ErrRebindUnsupported) {
		t.Fatalf("%v", err)
	}
}

func endBody(t *testing.T, id, runID string) []byte {
	t.Helper()
	b, err := fakes.KeymakerWire{}.Encode(keymaker.Request{ID: id, Method: keymaker.MethodEnd, End: &keymaker.EndRequest{RunID: runID}})
	must(t, err)
	return b
}

// Claim (H6 table, "every crash boundary leads to its stated action"; "the
// reaper ends an orphan after L is killed"; an orphan is detected by a dead
// recorded L incarnation): after a restart, binding is HELD; bound without
// release under a dead launcher is ended and its child killed; bound with
// input released under a dead launcher is ended then swept; ending is
// retried with the stored exact request; ended is never adopted; a bound
// record whose recorded launcher is alive is owned and left alone. Release
// gate: the swept uids with archival evidence are released, the one without
// stays quarantined although its end was acknowledged and its child killed.
func TestRestartTable(t *testing.T) {
	e := newEnv(t)
	dead := &incarnation.Incarnation{PID: 4242, UID: luid, StartTicks: 1, BootID: "boot-1"}
	self, err := e.proc.Incarnations().Self()
	must(t, err)
	mk := func(key string, child uint32, inc *incarnation.Incarnation) *runrecord.Record {
		return &runrecord.Record{Version: runrecord.Version, Kind: runrecord.Kind, Key: key, State: runrecord.Binding,
			LauncherUID: luid, LauncherGeneration: "gen-a", StationRegistration: "station-1", InputPrincipal: "input-1",
			Origin: fakes.ValidOrigin(), LaunchNonce: key, SourceIncarnation: "inc-1", PreparedID: "prep-1",
			BindRequestID: "bind-" + key, ChildUID: child, Launcher: inc}
	}
	bound := func(r *runrecord.Record) error { r.State = runrecord.Bound; r.RunID = "run-" + r.Key; return nil }
	childOf := func(pid int, uid uint32) func(*runrecord.Record) error {
		return func(r *runrecord.Record) error {
			must(t, e.proc.Add(pid, [4]uint32{uid, uid, uid, uid}, 5))
			inc, err := e.proc.Incarnations().Read(pid)
			r.Child = &inc
			return err
		}
	}
	released := func(r *runrecord.Record) error { r.InputReleased = true; return nil }
	must(t, e.store.Create(mk("binding", 2001, dead)))
	must(t, e.store.Create(mk("nochild", 2002, dead)))
	_, err = e.store.Update("nochild", bound)
	must(t, err)
	must(t, e.store.Create(mk("spawned", 2003, dead)))
	_, err = e.store.Update("spawned", bound)
	must(t, err)
	_, err = e.store.Update("spawned", childOf(300, 2003))
	must(t, err)
	must(t, e.store.Create(mk("orphan", 2004, dead)))
	_, err = e.store.Update("orphan", bound)
	must(t, err)
	_, err = e.store.Update("orphan", childOf(400, 2004))
	must(t, err)
	_, err = e.store.Update("orphan", released)
	must(t, err)
	must(t, e.proc.Add(401, [4]uint32{2004, 0, 2004, 0}, 6)) // setuid descendant of the orphan
	must(t, e.store.Create(mk("ending", 2005, dead)))
	_, err = e.store.Update("ending", bound)
	must(t, err)
	_, err = e.store.Update("ending", func(r *runrecord.Record) error {
		r.State = runrecord.Ending
		r.EndRequestID = "end-ending"
		r.EndRequestBody = endBody(t, "end-ending", "run-ending")
		return nil
	})
	must(t, err)
	must(t, e.store.Create(mk("ended", 2006, dead)))
	_, err = e.store.Update("ended", bound)
	must(t, err)
	_, err = e.store.Update("ended", func(r *runrecord.Record) error {
		r.State = runrecord.Ending
		r.EndRequestID = "end-ended"
		r.EndRequestBody = endBody(t, "end-ended", "run-ended")
		return nil
	})
	must(t, err)
	_, err = e.store.Update("ended", func(r *runrecord.Record) error { r.State = runrecord.Ended; r.EndAcknowledged = true; return nil })
	must(t, err)
	must(t, e.store.Create(mk("noarchive", 2008, dead)))
	_, err = e.store.Update("noarchive", bound)
	must(t, err)
	_, err = e.store.Update("noarchive", childOf(800, 2008))
	must(t, err)
	_, err = e.store.Update("noarchive", released)
	must(t, err)
	must(t, e.store.Create(mk("owned", 2007, &self)))
	_, err = e.store.Update("owned", bound)
	must(t, err)
	_, err = e.store.Update("owned", childOf(700, 2007))
	must(t, err)
	_, err = e.store.Update("owned", released)
	must(t, err)

	for _, k := range []string{"nochild", "spawned", "orphan", "ending"} {
		e.archival[k] = true
	}
	e.reopen()
	l := e.build()
	reports := l.Restart(context.Background())
	got := map[string]launcher.Report{}
	for _, r := range reports {
		got[r.Key] = r
	}
	want := map[string]runrecord.RestartAction{
		"binding": runrecord.ActionHold, "nochild": runrecord.ActionEndAndKill, "spawned": runrecord.ActionEndAndKill,
		"orphan": runrecord.ActionEndThenSweep, "ending": runrecord.ActionRetryEnd, "ended": runrecord.ActionNeverAdopt, "owned": runrecord.ActionOwned,
		"noarchive": runrecord.ActionEndThenSweep,
	}
	if len(got) != len(want) {
		t.Fatalf("reports %+v", reports)
	}
	for k, a := range want {
		if got[k].Action != a {
			t.Fatalf("%s: action %s want %s (err %v)", k, got[k].Action, a, got[k].Err)
		}
	}
	states := map[string]runrecord.State{"binding": runrecord.Binding, "nochild": runrecord.Ended, "spawned": runrecord.Ended, "orphan": runrecord.Ended, "ending": runrecord.Ended, "ended": runrecord.Ended, "owned": runrecord.Bound, "noarchive": runrecord.Ended}
	for k, s := range states {
		if rec := e.record(k); rec.State != s || !e.store.Fenced(k) {
			t.Fatalf("%s: state %s want %s fenced=%v", k, rec.State, s, e.store.Fenced(k))
		}
	}
	// Ends: one per orphaned run, with the stored exact request for "ending".
	endIDs := map[string]bool{}
	var endingBytes []byte
	for _, raw := range e.km.Ends() {
		var req struct {
			ID   string `json:"id"`
			Body struct {
				RunID string `json:"run_id"`
			} `json:"body"`
		}
		must(t, json.Unmarshal(raw, &req))
		endIDs[req.Body.RunID] = true
		if req.ID == "end-ending" {
			endingBytes = raw
		}
	}
	for _, run := range []string{"run-nochild", "run-spawned", "run-orphan", "run-ending", "run-noarchive"} {
		if !endIDs[run] {
			t.Fatalf("no end for %s: %v", run, endIDs)
		}
	}
	if endIDs["run-ended"] || endIDs["run-owned"] || endIDs["run-binding"] || len(endIDs) != 5 {
		t.Fatalf("ends sent to the wrong runs: %v", endIDs)
	}
	if !bytes.Equal(endingBytes, e.record("ending").EndRequestBody) {
		t.Fatal("retry did not send the stored exact end request")
	}
	// Kills by uid and quarantine outcomes.
	killed := map[uint32]bool{}
	for _, u := range e.killer.UIDs() {
		killed[u] = true
	}
	if !killed[2003] || !killed[2004] || !killed[2008] || killed[2007] || killed[2001] {
		t.Fatalf("killer uids %v", e.killer.UIDs())
	}
	for _, pid := range []string{"300", "400", "401", "800"} {
		if _, err := os.Stat(filepath.Join(e.proc.Root, pid)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pid %s survived", pid)
		}
	}
	if _, err := os.Stat(filepath.Join(e.proc.Root, "700")); err != nil {
		t.Fatal("owned run's child was killed")
	}
	// 2008 ended with a zero-process proof but without archival: quarantined.
	for uid, held := range map[uint32]bool{2001: true, 2002: false, 2003: false, 2004: false, 2005: false, 2006: true, 2007: true, 2008: true} {
		if e.quar.Held(uid) != held {
			t.Fatalf("uid %d held=%v want %v", uid, e.quar.Held(uid), held)
		}
	}
}

// Claim (H6, fail closed): a bound record whose recorded launcher cannot be
// read is undetermined: nothing is ended, killed or adopted.
func TestRestartUndetermined(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file; unreadable plant needs a non-root test process")
	}
	e := newEnv(t)
	must(t, e.proc.Add(4242, [4]uint32{luid, luid, luid, luid}, 1))
	inc, err := e.proc.Incarnations().Read(4242)
	must(t, err)
	must(t, e.proc.MakeUnreadable(4242))
	rec := &runrecord.Record{Version: runrecord.Version, Kind: runrecord.Kind, Key: "u", State: runrecord.Binding,
		LauncherUID: luid, LauncherGeneration: "gen-a", StationRegistration: "station-1", InputPrincipal: "input-1",
		Origin: fakes.ValidOrigin(), LaunchNonce: "u", SourceIncarnation: "inc-1", PreparedID: "prep-1",
		BindRequestID: "bind-u", ChildUID: 2008, Launcher: &inc}
	must(t, e.store.Create(rec))
	_, err = e.store.Update("u", func(r *runrecord.Record) error { r.State = runrecord.Bound; r.RunID = "run-u"; return nil })
	must(t, err)
	e.reopen()
	l := e.build()
	reports := l.Restart(context.Background())
	if len(reports) != 1 || reports[0].Action != runrecord.ActionUndetermined || reports[0].Err == nil || len(e.km.Ends()) != 0 || len(e.killer.UIDs()) != 0 || e.record("u").State != runrecord.Bound {
		t.Fatalf("%+v", reports)
	}
}

// Claim (H4 lifecycle only; "a child passes the same gates; an agent cannot
// supply parent_run"): a child run binds with parent_run from the
// launcher's live parent record and a null origin, through its own fresh
// Keymaker connection and its own binding record; after the parent ended,
// a child of it is refused. The API has no parameter for an agent-supplied
// parent_run.
func TestChildRunGates(t *testing.T) {
	e := newEnv(t)
	e.placement = &seqPlacement{next: cuid} // the child needs its own R_j, distinct from the parent's R_i
	l := e.build()
	parent, err := l.Begin(context.Background(), "input-1")
	must(t, err)
	e.km.SetRunID("run-child")
	child, err := l.BeginChild(context.Background(), parent, "input-2")
	must(t, err)
	rec := e.record(child.Key)
	if rec.State != runrecord.Bound || rec.ParentRun != parent.RunID || string(rec.Origin) != "null" || rec.RunID != "run-child" || rec.ChildUID != cuid+1 || e.kmDialer.Dials() != 2 {
		t.Fatalf("child record %+v dials=%d", rec, e.kmDialer.Dials())
	}
	binds := e.km.Binds()
	if len(binds) != 2 {
		t.Fatalf("binds %d", len(binds))
	}
	_, body, err := fakes.ParseBind(binds[1])
	must(t, err)
	if body.ParentRun != parent.RunID || body.Origin != nil || body.InputPrincipal != "input-2" {
		t.Fatalf("child bind %+v", body)
	}
	must(t, parent.Spawn(e.spawnChild(777)))
	if _, err := parent.End(context.Background(), 0, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.BeginChild(context.Background(), parent, "input-3"); !errors.Is(err, launcher.ErrParent) {
		t.Fatalf("child of an ended parent: %v", err)
	}
	if _, err := l.BeginChild(context.Background(), nil, "input-3"); !errors.Is(err, launcher.ErrParent) {
		t.Fatalf("child of nil parent: %v", err)
	}
}

// Claim (H2 step 2, "lost prepare or validate leads to a fresh prepare"): a
// validate lost on the first connection restarts from H1 on a new connection
// with a new prepared_id, and the old prepared_id is never validated again;
// when every validate is lost the launch fails run_origin_unavailable after
// the bounded restarts with zero Keymaker contact.
func TestSourceRestartFreshPrepare(t *testing.T) {
	e := newEnv(t)
	e.src.FirstValidateMode = "close"
	l := e.build()
	run, err := l.Begin(context.Background(), "input-1")
	must(t, err)
	_, prepares, _, conns := e.src.Counts()
	ids := e.src.ValidatedIDs()
	if conns != 2 || prepares != 2 || len(ids) != 2 || ids[0] != "prep-1" || ids[1] != "prep-2" || e.record(run.Key).PreparedID != "prep-2" {
		t.Fatalf("conns=%d prepares=%d ids=%v record=%s", conns, prepares, ids, e.record(run.Key).PreparedID)
	}
	e = newEnv(t)
	e.src.ValidateMode = "close"
	l = e.build()
	_, err = l.Begin(context.Background(), "input-1")
	if !codes.Is(err, codes.OriginUnavailable) {
		t.Fatalf("%v", err)
	}
	if _, _, _, conns := e.src.Counts(); conns != e.cfg.MaxRestarts+1 || e.kmDialer.Dials() != 0 || len(e.store.Keys()) != 0 {
		t.Fatalf("conns=%d keymaker=%d records=%d", conns, e.kmDialer.Dials(), len(e.store.Keys()))
	}
}

// Claim (Controls, Source side; H1 "nothing is launched on any refusal"): a
// Source without the capability, a wrong peer, and a Matrix-ID origin each
// refuse run_origin_refused with zero Keymaker connections and no record.
func TestSourceRefusalsNoKeymaker(t *testing.T) {
	for _, mut := range []func(*env){
		func(e *env) { e.src.NoCapability = true },
		func(e *env) { e.srcDialer.Peer = source.Credential{UID: luid + 1} },
		func(e *env) { e.src.ValidateMode = "matrix-origin" },
	} {
		e := newEnv(t)
		mut(e)
		l := e.build()
		_, err := l.Begin(context.Background(), "input-1")
		if !codes.Is(err, codes.OriginRefused) || e.kmDialer.Dials() != 0 || len(e.store.Keys()) != 0 {
			t.Fatalf("%v keymaker=%d records=%d", err, e.kmDialer.Dials(), len(e.store.Keys()))
		}
	}
}

// Claim (seams refuse by name, never fall back): with the Keymaker codec
// unpinned the bind is refused before any frame is sent and the record is
// refused rather than HELD; with the sweep helper unpinned an acknowledged
// end leaves the uid quarantined; with no Keymaker dialer the launch fails
// with ErrKeymakerUnavailable after Source validated and before any record.
func TestUnpinnedSeams(t *testing.T) {
	e := newEnv(t)
	e.deps.KeymakerWire = nil
	l := e.build()
	_, err := l.Begin(context.Background(), "input-1")
	if !codes.IsUnpinned(err) || len(e.km.Binds()) != 0 {
		t.Fatalf("unpinned wire: %v binds=%d", err, len(e.km.Binds()))
	}
	if keys := e.store.Keys(); len(keys) != 1 || e.record(keys[0]).State != runrecord.Refused {
		t.Fatalf("record after unpinned wire: %v", keys)
	}
	e = newEnv(t)
	e.deps.Killer = sweep.UnpinnedKiller{}
	run := endReady(t, e)
	out, err := run.End(context.Background(), 0, true, nil)
	must(t, err)
	if !out.Acknowledged || out.UIDReleased || !codes.IsUnpinned(out.Sweep.Err) || !e.quar.Held(cuid) {
		t.Fatalf("unpinned killer: %+v", out)
	}
	e = newEnv(t)
	e.deps.Keymaker = nil
	l = e.build()
	_, err = l.Begin(context.Background(), "input-1")
	if !errors.Is(err, launcher.ErrKeymakerUnavailable) || len(e.store.Keys()) != 0 {
		t.Fatalf("no dialer: %v", err)
	}
	if _, _, validates, _ := e.src.Counts(); validates != 1 {
		t.Fatal("Source was not validated before the Keymaker refusal")
	}
}
