//go:build linux && (amd64 || arm64)

package m0hook_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/hygiene"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/m0hook"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

var (
	luid = uint32(os.Getuid())
	// cuid is the R_i the fixture placement leases; it is also the worker uid
	// the dispatcher asks for in the positive rows (the input principal uid
	// must equal the leased uid).
	cuid = uint32(30001)
	// fixtureEvent is the retained event the RunEvents fixture names.
	fixtureEvent = runorigin.EventRef{RoomID: "!room:example", EventID: "$retained-event"}
	// errStoreRefused is an ordinary store refusal, without the sync failure.
	errStoreRefused = errors.New("fixture: store refuses this transition")
)

const (
	childPID = 777
	effectA  = "effect-a"
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

// innerFake is the counting S1 seam: it commits through the gate, places the
// child in the fake procfs under the worker uid and answers a synthetic
// receipt whose bytes nothing parses.
type innerFake struct {
	mu          sync.Mutex
	proc        *fakes.ProcTree
	pid         int
	startErr    error
	stopErr     error
	stopReceipt *m0.StopReceipt
	uncommitted bool
	observation m0.Observation
	// atStop runs inside Stop, before the count; tests use it to observe
	// the run record at the moment the inner stop happens.
	atStop   func()
	starts   int
	stops    []m0.StopRequest
	observes int
}

func (f *innerFake) Start(_ context.Context, req m0.StartRequest, gate m0.CommitGate) (m0.StartReceipt, error) {
	f.mu.Lock()
	f.starts++
	startErr, uncommitted, pid := f.startErr, f.uncommitted, f.pid
	f.mu.Unlock()
	if startErr != nil {
		return m0.StartReceipt{}, startErr
	}
	if uncommitted {
		return m0.StartReceipt{Committed: false}, nil
	}
	facts := m0.ReceiverFacts{DropVerified: true, Receiver: "fake-receiver", WorkerUID: req.Worker.UID, Generation: req.Worker.Generation, ControllerEpoch: req.ControllerEpoch, ExecutionID: req.ExecutionID, EffectID: req.EffectID}
	if err := gate.Commit(facts); err != nil {
		return m0.StartReceipt{Committed: false}, nil
	}
	uid := req.Worker.UID
	if err := f.proc.Add(pid, [4]uint32{uid, uid, uid, uid}, 42); err != nil {
		return m0.StartReceipt{}, err
	}
	return m0.StartReceipt{Committed: true, CommitUnix: 1700000000, ExecUnix: 1700000001, Receipt: []byte("synthetic-receipt")}, nil
}

func (f *innerFake) Stop(_ context.Context, req m0.StopRequest) (m0.StopReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.atStop != nil {
		f.atStop()
	}
	f.stops = append(f.stops, req)
	if f.stopReceipt != nil {
		return *f.stopReceipt, f.stopErr
	}
	return m0.StopReceipt{Absent: true, Evidence: []byte("synthetic fenced absence")}, f.stopErr
}

func (f *innerFake) Observe(context.Context, m0.EffectQuery) (m0.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observes++
	if f.observation == "" {
		return m0.ObservationUnknown, nil
	}
	return f.observation, nil
}

func (f *innerFake) counts() (starts, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, len(f.stops)
}

// gateFake counts receiver commits.
type gateFake struct {
	mu      sync.Mutex
	commits int
}

func (g *gateFake) Commit(m0.ReceiverFacts) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commits++
	return nil
}

func (g *gateFake) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.commits
}

// childReader is the ChildIncarnations fixture. It reads the pid the fixture
// placed, never the receipt bytes: the S1 receipt format is unpublished.
type childReader struct {
	proc *fakes.ProcTree
	pid  int
	err  error
}

func (r childReader) Child(m0.StartRequest, m0.StartReceipt) (incarnation.Incarnation, error) {
	if r.err != nil {
		return incarnation.Incarnation{}, r.err
	}
	return r.proc.Incarnations().Read(r.pid)
}

// staticInterface is the ChildInterface fixture.
type staticInterface struct {
	launch hygiene.Launch
	err    error
}

func (s staticInterface) Launch(m0.StartRequest) (hygiene.Launch, error) { return s.launch, s.err }

// eventsFake is the RunEvents fixture: it names one retained event and
// records the effects it was asked about.
type eventsFake struct {
	mu    sync.Mutex
	event runorigin.EventRef
	err   error
	asked []string
}

func (f *eventsFake) Event(req m0.StartRequest) (runorigin.EventRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, req.EffectID)
	if f.err != nil {
		return runorigin.EventRef{}, f.err
	}
	return f.event, nil
}

func (f *eventsFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

// gateStore is a launcher.RecordStore over the fixture store that can refuse
// an update by the state the record would move to.
type gateStore struct {
	e      *env
	mu     sync.Mutex
	refuse func(next *runrecord.Record) error
}

func (g *gateStore) set(refuse func(next *runrecord.Record) error) {
	g.mu.Lock()
	g.refuse = refuse
	g.mu.Unlock()
}
func (g *gateStore) StateDirectory() string           { return g.e.store.StateDirectory() }
func (g *gateStore) Create(r *runrecord.Record) error { return g.e.store.Create(r) }
func (g *gateStore) Update(key string, mutate func(*runrecord.Record) error) (*runrecord.Record, error) {
	return g.e.store.Update(key, func(r *runrecord.Record) error {
		if err := mutate(r); err != nil {
			return err
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.refuse != nil {
			return g.refuse(r)
		}
		return nil
	})
}
func (g *gateStore) Get(key string) (*runrecord.Record, bool) { return g.e.store.Get(key) }
func (g *gateStore) Keys() []string                           { return g.e.store.Keys() }
func (g *gateStore) Fenced(key string) bool                   { return g.e.store.Fenced(key) }

// gated puts a gateStore between the launcher and the fixture store.
func gated(e *env) *gateStore {
	g := &gateStore{e: e}
	e.records = g
	return g
}

// refuseState refuses the update that moves a record to state s.
func refuseState(s runrecord.State, cause error) func(*runrecord.Record) error {
	return func(next *runrecord.Record) error {
		if next.State == s {
			return cause
		}
		return nil
	}
}

type env struct {
	t     *testing.T
	jcfg  journal.Config
	store *runrecord.Store
	// records, when set, replaces store as the launcher's RecordStore.
	records   launcher.RecordStore
	src       *fakes.SourceScript
	srcDialer *fakes.SourceDialer
	km        *fakes.KeymakerScript
	kmDialer  *fakes.KeymakerDialer
	proc      *fakes.ProcTree
	killer    *fakes.Killer
	quar      *sweep.Quarantine
	export    sweep.StaticWorkProductExport
	placement isolation.Placement
	cfg       launcher.Config
	deps      launcher.Deps
	inner     *innerFake
	gate      *gateFake
	reader    childReader
	iface     staticInterface
	events    *eventsFake

	mu     sync.Mutex
	nonces int
}

func (e *env) nonce() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nonces++
	return fmt.Sprintf("n-%d", e.nonces), nil
}

func cleanLaunch(t *testing.T) hygiene.Launch {
	t.Helper()
	a, b, err := fakes.SocketPair()
	must(t, err)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	fa, err := fakes.RawFD(a)
	must(t, err)
	fb, err := fakes.RawFD(b)
	must(t, err)
	return hygiene.Launch{
		Argv:           []string{"/usr/bin/agent", "--run"},
		Env:            []string{"PATH=/usr/bin", "HOME=/home/worker"},
		ProtectedPaths: []string{"/run/source/origin.sock", "/var/lib/launcher"},
		ProtectedFDs:   []int{fa, fb},
		InheritedFDs:   []int{0, 1, 2},
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, jcfg: journalCfg(t)}
	e.quar = &sweep.Quarantine{}
	must(t, e.quar.Start(e.jcfg.Directory, sweep.Fresh))
	t.Cleanup(func() { _ = e.quar.Close() })
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
	e.export = sweep.StaticWorkProductExport{}
	e.placement = isolation.Fixed(cuid)
	e.cfg = launcher.Config{
		Installation:   sweep.Fresh,
		LauncherUID:    luid,
		Station:        "1",
		RequestTimeout: 100 * time.Millisecond,
		MaxRestarts:    2,
		ProcRoot:       e.proc.Root,
		ProcMountInfo:  e.proc.MountInfoPath(),
		SweepBudget:    sweep.Budget{Attempts: 3},
	}
	e.deps = launcher.Deps{
		WorkProductExport: e.export,
		OperatorRelease:   sweep.StaticOperatorRelease{},
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
		Killer:       e.killer,
		Nonce:        e.nonce,
	}
	e.inner = &innerFake{proc: e.proc, pid: childPID}
	e.gate = &gateFake{}
	e.reader = childReader{proc: e.proc, pid: childPID}
	e.iface = staticInterface{launch: cleanLaunch(t)}
	e.events = &eventsFake{event: fixtureEvent}
	return e
}

func (e *env) launcher() *launcher.Launcher {
	e.t.Helper()
	e.deps.Placement = e.placement
	if e.records != nil {
		e.deps.Records = e.records
	} else {
		e.deps.Records = e.store
	}
	e.deps.Quarantine = e.quar
	l, err := launcher.New(e.cfg, e.deps)
	must(e.t, err)
	return l
}

// build composes the decorator through New with the fixture seams.
func (e *env) build() *m0hook.Hooked {
	e.t.Helper()
	h, err := m0hook.New(context.Background(), e.hookDeps())
	must(e.t, err)
	return h
}

// stopRequestsFake supplies M0's fixture identity, never a production derivation.
type stopRequestsFake struct{}

func (stopRequestsFake) StopRequest(req m0.StartRequest) (m0.StopRequest, error) {
	return m0.StopRequest{EffectID: "stop-" + req.EffectID, ExecutionID: req.ExecutionID, Worker: req.Worker,
		Event: 1700000100, Detection: 1700000101,
		Obligations: []m0.StopObligation{{Source: "cancel", Event: 1700000100, Detection: 1700000101}},
		Ceiling:     m0.StopCeiling(1700000100, 1700000101, req.Deadlines.DetectSeconds, req.Deadlines.StopSeconds)}, nil
}

func (e *env) hookDeps() m0hook.Deps {
	return m0hook.Deps{Installation: e.cfg.Installation, Inner: e.inner, L: e.launcher(), Incarnations: e.reader, Hygiene: e.iface, Events: e.events, Stops: stopRequestsFake{}}
}

// reopen closes the store before the lifetime owner, then takes that lock before Open.
func (e *env) reopen() {
	e.t.Helper()
	must(e.t, e.store.Close())
	must(e.t, e.quar.Close())
	e.cfg.Installation = sweep.Established
	e.quar = &sweep.Quarantine{}
	must(e.t, e.quar.Start(e.jcfg.Directory, sweep.Established))
	st, err := runrecord.Open(e.jcfg, e.quar)
	must(e.t, err)
	e.t.Cleanup(func() { _ = st.Close() })
	e.store = st
}

func (e *env) record(key string) *runrecord.Record {
	e.t.Helper()
	r, ok := e.store.Get(key)
	if !ok {
		e.t.Fatalf("no record %q", key)
	}
	return r
}

func (e *env) only() *runrecord.Record {
	e.t.Helper()
	keys := e.store.Keys()
	if len(keys) != 1 {
		e.t.Fatalf("records %v, want exactly one", keys)
	}
	return e.record(keys[0])
}

func startReq(effect string, uid uint32) m0.StartRequest {
	return m0.StartRequest{
		EffectID:        effect,
		ExecutionID:     "exec-1",
		ControllerEpoch: 1,
		Worker:          m0.Worker{UID: uid, Generation: "cccccccccccccccccccccccccccccccc"},
		ExecProfile:     "profile",
		Entry:           []byte("{}"),
		EntryDigest:     "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Deadlines:       m0.Deadlines{ReservedAt: 1700000000, Expiry: 1700000200, DetectSeconds: 5, StopSeconds: 10, DeadlineMs: 1700000200000},
	}
}

func stopReq(effect string, uid uint32) m0.StopRequest {
	return m0.StopRequest{EffectID: effect, ExecutionID: "exec-1", Worker: m0.Worker{UID: uid, Generation: "cccccccccccccccccccccccccccccccc"}, Event: 1700000100, Detection: 1700000101}
}

// Claim: a bound start records incarnations and release bookkeeping; Stop preserves its receipt, ends/sweeps once and explicitly retains unknown child status.
func TestBoundRunStartsOnceAndStopsEnded(t *testing.T) {
	e := newEnv(t)
	h := e.build()
	receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	must(t, err)
	starts, stops := e.inner.counts()
	if !receipt.Committed || receipt.ExecUnix != 1700000001 || starts != 1 || stops != 0 || e.gate.count() != 1 || len(e.km.Binds()) != 1 || e.kmDialer.Dials() != 1 {
		t.Fatalf("receipt %+v starts=%d stops=%d commits=%d binds=%d dials=%d", receipt, starts, stops, e.gate.count(), len(e.km.Binds()), e.kmDialer.Dials())
	}
	a, ok := h.Last(effectA)
	if !ok || a.Key == "" || a.Err != nil || a.Ended || e.events.count() != 1 {
		t.Fatalf("attempt %+v ok=%v events=%d", a, ok, e.events.count())
	}
	rec := e.record(a.Key)
	if rec.State != runrecord.Bound || rec.RunID != "run-1" || rec.Launcher == nil || rec.Launcher.PID != os.Getpid() || rec.Child == nil || rec.Child.PID != childPID || rec.Child.UID != cuid || !rec.InputReleased {
		t.Fatalf("record %+v", rec)
	}
	if _, ok := h.Code(effectA); ok {
		t.Fatal("a live run carries no P17 code")
	}
	// Inner.Stop runs first: at that moment the record is still bound.
	var stateAtInnerStop runrecord.State
	e.inner.atStop = func() { stateAtInnerStop = e.record(a.Key).State }
	stopReceipt, err := h.Stop(context.Background(), stopReq(effectA, cuid))
	must(t, err)
	if stateAtInnerStop != runrecord.Bound {
		t.Fatalf("record was %q when Inner.Stop ran, want bound", stateAtInnerStop)
	}
	if !stopReceipt.Absent || string(stopReceipt.Evidence) != "synthetic fenced absence" {
		t.Fatalf("stop receipt changed: %+v", stopReceipt)
	}
	starts, stops = e.inner.counts()
	rec = e.record(a.Key)
	if starts != 1 || stops != 1 || e.inner.stops[0].EffectID != effectA || rec.State != runrecord.Ended || !rec.EndAcknowledged || !rec.TeardownDone || len(e.km.Ends()) != 1 {
		t.Fatalf("after stop starts=%d stops=%d record %+v ends=%d", starts, stops, rec, len(e.km.Ends()))
	}
	if uids := e.killer.UIDs(); len(uids) != 1 || uids[0] != cuid {
		t.Fatalf("killer uids %v", uids)
	}
	if _, err := os.Stat(filepath.Join(e.proc.Root, "777")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("child survived the sweep")
	}
	a, _ = h.Last(effectA)
	if !a.Ended || a.EndErr != nil || a.End == nil || !a.End.Acknowledged || a.ChildStatusKnown {
		t.Fatalf("attempt after stop %+v", a)
	}
	// A second Stop for the ended effect is Inner.Stop only: no second end.
	_, err = h.Stop(context.Background(), stopReq(effectA, cuid))
	must(t, err)
	if _, stops = e.inner.counts(); stops != 2 || len(e.km.Ends()) != 1 {
		t.Fatalf("second stop stops=%d ends=%d", stops, len(e.km.Ends()))
	}
}

// Claim: pre-bound refusals retain their code with no start or commit; isolation contacts nothing; control: TestBoundRunStartsOnceAndStopsEnded.
func TestPreBoundRefusalsZeroInner(t *testing.T) {
	cases := []struct {
		name string
		code codes.Code
		mut  func(*env)
	}{
		{"isolation", codes.IsolationUnqualified, func(e *env) { e.placement = isolation.Unpinned{} }},
		{"origin-refused", codes.OriginRefused, func(e *env) { e.src.NoCapability = true }},
		{"origin-unavailable", codes.OriginUnavailable, func(e *env) { e.src.PrepareMode = "dependency" }},
		{"bind-unavailable", codes.BindUnavailable, func(e *env) { e.kmDialer.Fail = errors.New("synthetic keymaker dial failure") }},
		{"bind-refused", codes.BindRefused, func(e *env) { e.km.BindMode = "refuse" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.mut(e)
			h := e.build()
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			if err != nil || receipt.Committed {
				t.Fatalf("receipt %+v err %v", receipt, err)
			}
			starts, stops := e.inner.counts()
			if starts != 0 || stops != 0 || e.gate.count() != 0 {
				t.Fatalf("inner starts=%d stops=%d commits=%d", starts, stops, e.gate.count())
			}
			if code, ok := h.Code(effectA); !ok || code != c.code {
				t.Fatalf("code %q ok=%v want %q", code, ok, c.code)
			}
			srcDials, _ := e.srcDialer.Dials()
			if c.code == codes.IsolationUnqualified && (srcDials != 0 || e.kmDialer.Dials() != 0) {
				t.Fatalf("isolation refusal contacted source=%d keymaker=%d", srcDials, e.kmDialer.Dials())
			}
			if c.code == codes.BindRefused {
				if rec := e.only(); rec.State != runrecord.Refused {
					t.Fatalf("record %+v", rec)
				}
			} else if keys := e.store.Keys(); len(keys) != 0 {
				t.Fatalf("record created on %s: %v", c.code, keys)
			}
		})
	}
}

// Claim: HELD stays binding and every Stop retains run_bind_unknown without a rebind or end; acknowledged End is the adjacent control.
func TestHeldBindNeverStartsOrRebinds(t *testing.T) {
	t.Run("acknowledged", TestBoundRunStartsOnceAndStopsEnded)
	t.Run("held", heldBindNeverStartsOrRebinds)
}

func heldBindNeverStartsOrRebinds(t *testing.T) {
	e := newEnv(t)
	e.km.BindMode = "close"
	h := e.build()
	_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	if !codes.Is(err, codes.BindUnknown) {
		t.Fatalf("%v", err)
	}
	starts, _ := e.inner.counts()
	if starts != 0 || e.gate.count() != 0 || e.only().State != runrecord.Binding || len(e.km.Binds()) != 1 {
		t.Fatalf("starts=%d commits=%d record %+v binds=%d", starts, e.gate.count(), e.only(), len(e.km.Binds()))
	}
	if code, ok := h.Code(effectA); !ok || code != codes.BindUnknown {
		t.Fatalf("code %q ok=%v", code, ok)
	}
	e.km.SetBindMode("")
	_, err = h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	if !codes.Is(err, codes.BindUnknown) || !errors.Is(err, m0hook.ErrEffectKnown) {
		t.Fatalf("second start: %v", err)
	}
	starts, _ = e.inner.counts()
	if starts != 0 || len(e.km.Binds()) != 1 || e.kmDialer.Dials() != 1 || len(e.store.Keys()) != 1 || e.only().State != runrecord.Binding {
		t.Fatalf("second start bound again: starts=%d binds=%d dials=%d keys=%v", starts, len(e.km.Binds()), e.kmDialer.Dials(), e.store.Keys())
	}
	for i := 1; i <= 3; i++ {
		r, err := h.Stop(context.Background(), stopReq(effectA, cuid))
		if !codes.Is(err, codes.BindUnknown) || !r.Absent || string(r.Evidence) != "synthetic fenced absence" {
			t.Fatalf("Stop %d lost held bind refusal: %+v %v", i, r, err)
		}
		if _, stops := e.inner.counts(); stops != i || len(e.km.Ends()) != 0 || e.only().State != runrecord.Binding {
			t.Fatalf("stop on held: stops=%d ends=%d record %+v", stops, len(e.km.Ends()), e.only())
		}
	}
}

// Claim: a worker/lease uid mismatch refuses before contact or record creation; the matching uid then binds and commits (adjacent control).
func TestWorkerUIDMismatchRefusedBeforeContact(t *testing.T) {
	e := newEnv(t)
	h := e.build()
	receipt, err := h.Start(context.Background(), startReq(effectA, cuid+1), e.gate)
	if err != nil || receipt.Committed {
		t.Fatalf("receipt %+v err %v", receipt, err)
	}
	starts, stops := e.inner.counts()
	srcDials, _ := e.srcDialer.Dials()
	if starts != 0 || stops != 0 || e.gate.count() != 0 || srcDials != 0 || e.kmDialer.Dials() != 0 || len(e.store.Keys()) != 0 {
		t.Fatalf("starts=%d stops=%d commits=%d source=%d keymaker=%d keys=%v", starts, stops, e.gate.count(), srcDials, e.kmDialer.Dials(), e.store.Keys())
	}
	a, _ := h.Last(effectA)
	if code, ok := h.Code(effectA); !ok || code != codes.IsolationUnqualified || a.Ended || a.Key != "" {
		t.Fatalf("code %q ok=%v attempt %+v", code, ok, a)
	}
	receipt, err = h.Start(context.Background(), startReq("effect-b", cuid), e.gate)
	must(t, err)
	if rec := e.only(); !receipt.Committed || rec.State != runrecord.Bound || rec.ChildUID != cuid || len(e.km.Binds()) != 1 {
		t.Fatalf("matching uid: receipt %+v record %+v binds=%d", receipt, rec, len(e.km.Binds()))
	}
}

// Claim: failed inner starts end through H5; unproved commits preserve the receipt, return an error and stop the child; control: TestBoundRunStartsOnceAndStopsEnded.
func TestInnerFailuresEndThroughH5(t *testing.T) {
	synthetic := errors.New("synthetic inner start failure")
	unproved := errors.New("synthetic incarnation failure")
	cases := []struct {
		name      string
		mut       func(*env)
		wantErr   error
		wantStops int
		committed bool
		nilErr    bool
	}{
		{"start-error", func(e *env) { e.inner.startErr = synthetic }, synthetic, 0, false, false},
		{"not-committed", func(e *env) { e.inner.uncommitted = true }, nil, 0, false, true},
		{"unproved", func(e *env) { e.reader = childReader{proc: e.proc, pid: childPID, err: unproved} }, m0hook.ErrUnproved, 1, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.mut(e)
			h := e.build()
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			if c.nilErr != (err == nil) || (c.wantErr != nil && !errors.Is(err, c.wantErr)) || receipt.Committed != c.committed {
				t.Fatalf("receipt %+v err %v", receipt, err)
			}
			starts, stops := e.inner.counts()
			rec := e.only()
			if starts != 1 || stops != c.wantStops || rec.State != runrecord.Ended || !rec.EndAcknowledged || !rec.TeardownDone || len(e.km.Ends()) != 1 {
				t.Fatalf("starts=%d stops=%d record %+v ends=%d", starts, stops, rec, len(e.km.Ends()))
			}
			if c.name == "unproved" {
				if rec.Child != nil || e.inner.stops[0].EffectID != "stop-"+effectA || !errors.Is(err, unproved) {
					t.Fatalf("unproved: record %+v stops %+v err %v", rec, e.inner.stops, err)
				}
				if _, err := os.Stat(filepath.Join(e.proc.Root, "777")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unproved child survived the sweep")
				}
			}
			a, _ := h.Last(effectA)
			if !a.Ended || a.Err == nil {
				t.Fatalf("attempt %+v", a)
			}
		})
	}
}

// Claim: a lost end ACK retains run_end_unknown on every Stop, ending intent and quarantine without retry; acknowledged End is the adjacent control.
func TestLostEndAckStaysEndingQuarantined(t *testing.T) {
	t.Run("acknowledged", TestBoundRunStartsOnceAndStopsEnded)
	t.Run("lost", lostEndAckStaysEndingQuarantined)
}

func lostEndAckStaysEndingQuarantined(t *testing.T) {
	e := newEnv(t)
	e.km.EndMode = "close"
	h := e.build()
	_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	must(t, err)
	a, _ := h.Last(effectA)
	receipt, err := h.Stop(context.Background(), stopReq(effectA, cuid))
	if !codes.Is(err, codes.EndUnknown) || !receipt.Absent || string(receipt.Evidence) != "synthetic fenced absence" {
		t.Fatalf("stop receipt changed or failure lost: %+v err %v", receipt, err)
	}
	rec := e.record(a.Key)
	if rec.State != runrecord.Ending || rec.EndAcknowledged || !rec.TeardownDone || rec.EndRequestID == "" || len(rec.EndRequestBody) == 0 || len(e.km.Ends()) != 1 {
		t.Fatalf("record %+v ends=%d", rec, len(e.km.Ends()))
	}
	if !e.quar.Held(cuid) {
		t.Fatal("uid left quarantine without an acknowledged end")
	}
	a, _ = h.Last(effectA)
	if code, ok := h.Code(effectA); !ok || code != codes.EndUnknown || !a.Ended || !codes.Is(a.EndErr, codes.EndUnknown) || a.End == nil || a.End.Acknowledged {
		t.Fatalf("code %q ok=%v attempt %+v", code, ok, a)
	}
	for i := 2; i <= 3; i++ {
		r, err := h.Stop(context.Background(), stopReq(effectA, cuid))
		if !codes.Is(err, codes.EndUnknown) || !r.Absent || string(r.Evidence) != "synthetic fenced absence" {
			t.Fatalf("Stop %d lost retained end refusal: %+v %v", i, r, err)
		}
		if _, stops := e.inner.counts(); stops != i || len(e.km.Ends()) != 1 || e.record(a.Key).State != runrecord.Ending {
			t.Fatalf("repeated stop stops=%d ends=%d", stops, len(e.km.Ends()))
		}
	}
}

func plantBound(t *testing.T, e *env, key string, child uint32, pid int, launcherInc *incarnation.Incarnation) {
	t.Helper()
	rec := &runrecord.Record{Version: runrecord.Version, Kind: runrecord.Kind, Key: key, State: runrecord.Binding,
		LauncherUID: luid, LauncherGeneration: "gen-a", StationRegistration: "1", InputPrincipal: "input",
		Origin: fakes.ValidOrigin(), LaunchNonce: key, SourceIncarnation: "inc-1", PreparedID: "prep-1",
		BindRequestID: "bind-" + key, ChildUID: child, Launcher: launcherInc}
	must(t, e.store.Create(rec))
	_, err := e.store.Update(key, func(r *runrecord.Record) error { r.State = runrecord.Bound; r.RunID = "run-" + key; return nil })
	must(t, err)
	must(t, e.proc.Add(pid, [4]uint32{child, child, child, child}, 5))
	inc, err := e.proc.Incarnations().Read(pid)
	must(t, err)
	_, err = e.store.Update(key, func(r *runrecord.Record) error { r.Child = &inc; r.InputReleased = true; return nil })
	must(t, err)
}

// Claim: New ends and sweeps an orphan before the next bind; bypassing New refuses before contact; the post-New start is the control.
func TestNewRunsRestartBeforeStart(t *testing.T) {
	e := newEnv(t)
	dead := &incarnation.Incarnation{PID: 4242, UID: luid, StartTicks: 1, BootID: "boot-1"}
	plantBound(t, e, "old", 30002, 300, dead)
	e.export["old"] = sweep.Exported
	e.reopen()
	h := e.build()
	reports := h.Reports()
	if len(reports) != 1 || reports[0].Key != "old" || reports[0].Action != runrecord.ActionEndThenSweep || reports[0].Err != nil {
		t.Fatalf("reports %+v", reports)
	}
	if rec := e.record("old"); rec.State != runrecord.Ended || !rec.EndAcknowledged || len(e.km.Ends()) != 1 || len(e.km.Binds()) != 0 {
		t.Fatalf("record %+v ends=%d binds=%d", rec, len(e.km.Ends()), len(e.km.Binds()))
	}
	if _, err := os.Stat(filepath.Join(e.proc.Root, "300")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphan child survived the restart sweep")
	}
	if e.quar.Held(30002) {
		t.Fatal("orphan uid stayed quarantined after an acknowledged end with export")
	}
	// Stop for the old effect has no live run here: Inner.Stop only.
	_, err := h.Stop(context.Background(), stopReq("effect-old", 30002))
	must(t, err)
	if _, stops := e.inner.counts(); stops != 1 || len(e.km.Ends()) != 1 {
		t.Fatalf("stop after restart: stops=%d ends=%d", stops, len(e.km.Ends()))
	}
	receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	must(t, err)
	if !receipt.Committed || len(e.km.Binds()) != 1 {
		t.Fatalf("start after restart: %+v binds=%d", receipt, len(e.km.Binds()))
	}

	t.Run("without-new", func(t *testing.T) {
		e := newEnv(t)
		var h m0hook.Hooked
		_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
		if !errors.Is(err, m0hook.ErrNotRestarted) {
			t.Fatalf("%v", err)
		}
		srcDials, _ := e.srcDialer.Dials()
		if starts, _ := e.inner.counts(); starts != 0 || srcDials != 0 || e.kmDialer.Dials() != 0 || e.gate.count() != 0 {
			t.Fatal("a Hooked without New reached a seam")
		}
		// Stop and Observe on it refuse by name through the M0 default seam.
		if _, err := h.Stop(context.Background(), stopReq(effectA, cuid)); m0.CodeOf(err) != m0.CodeSeamUnpinned {
			t.Fatalf("stop: %v", err)
		}
		if _, err := h.Observe(context.Background(), m0.EffectQuery{EffectID: effectA}); m0.CodeOf(err) != m0.CodeSeamUnpinned {
			t.Fatalf("observe: %v", err)
		}
	})
	t.Run("without-launcher", func(t *testing.T) {
		if _, err := m0hook.New(context.Background(), m0hook.Deps{}); !errors.Is(err, m0hook.ErrLauncherRequired) {
			t.Fatalf("%v", err)
		}
	})
}

// Claim: missing inner, incarnation or hygiene seams refuse by name and end any bound run without fallback; control: TestBoundRunStartsOnceAndStopsEnded.
func TestDefaultSeamsRefuseAfterGates(t *testing.T) {
	t.Run("nil-inner", func(t *testing.T) {
		e := newEnv(t)
		d := e.hookDeps()
		d.Inner = nil
		h, err := m0hook.New(context.Background(), d)
		must(t, err)
		_, err = h.Start(context.Background(), startReq(effectA, cuid), e.gate)
		if m0.CodeOf(err) != m0.CodeSeamUnpinned {
			t.Fatalf("%v", err)
		}
		rec := e.only()
		if len(e.km.Binds()) != 1 || rec.State != runrecord.Ended || !rec.EndAcknowledged || e.gate.count() != 0 {
			t.Fatalf("binds=%d record %+v commits=%d", len(e.km.Binds()), rec, e.gate.count())
		}
		a, _ := h.Last(effectA)
		if !a.Ended || m0.CodeOf(a.Err) != m0.CodeSeamUnpinned {
			t.Fatalf("attempt %+v", a)
		}
	})
	t.Run("nil-incarnations", func(t *testing.T) {
		e := newEnv(t)
		d := e.hookDeps()
		d.Incarnations = nil
		h, err := m0hook.New(context.Background(), d)
		must(t, err)
		receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
		if !codes.IsUnpinned(err) || !errors.Is(err, m0hook.ErrUnproved) || !receipt.Committed {
			t.Fatalf("receipt %+v err %v", receipt, err)
		}
		starts, stops := e.inner.counts()
		rec := e.only()
		if starts != 1 || stops != 1 || e.gate.count() != 1 || rec.State != runrecord.Ended || rec.Child != nil {
			t.Fatalf("starts=%d stops=%d commits=%d record %+v", starts, stops, e.gate.count(), rec)
		}
	})
	t.Run("nil-interface", func(t *testing.T) {
		e := newEnv(t)
		d := e.hookDeps()
		d.Hygiene = nil
		h, err := m0hook.New(context.Background(), d)
		must(t, err)
		receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
		if err != nil || receipt.Committed {
			t.Fatalf("receipt %+v err %v", receipt, err)
		}
		starts, stops := e.inner.counts()
		rec := e.only()
		if starts != 0 || stops != 0 || e.gate.count() != 0 || rec.State != runrecord.Ended || len(e.km.Binds()) != 1 {
			t.Fatalf("starts=%d stops=%d commits=%d record %+v binds=%d", starts, stops, e.gate.count(), rec, len(e.km.Binds()))
		}
		a, _ := h.Last(effectA)
		if !a.Ended || !codes.IsUnpinned(a.Err) {
			t.Fatalf("attempt %+v", a)
		}
	})
}

// Claim: invalid child hygiene refuses before Inner.Start and ends the bound run; control: TestBoundRunStartsOnceAndStopsEnded.
func TestHygieneViolationRefusesBeforeInner(t *testing.T) {
	e := newEnv(t)
	launch := cleanLaunch(t)
	launch.Env = []string{"PATH=/usr/bin", "BROKEN"}
	e.iface = staticInterface{launch: launch}
	h := e.build()
	receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	if err != nil || receipt.Committed {
		t.Fatalf("receipt %+v err %v", receipt, err)
	}
	starts, _ := e.inner.counts()
	rec := e.only()
	if starts != 0 || e.gate.count() != 0 || rec.State != runrecord.Ended || rec.Child != nil {
		t.Fatalf("starts=%d commits=%d record %+v", starts, e.gate.count(), rec)
	}
	a, _ := h.Last(effectA)
	if !errors.Is(a.Err, hygiene.ErrEnvShape) || !errors.Is(a.Err, hygiene.ErrHygiene) || !a.Ended {
		t.Fatalf("attempt %+v", a)
	}
}

// Claim: forced in-flight Start/Stop interleavings bind and end once; second and third Stop retain unknown End debt or return nil after acknowledged End.
func TestConcurrentStartStopOneBind(t *testing.T) {
	for _, mode := range []string{"", "close"} {
		name := "ack"
		if mode != "" {
			name = "lost"
		}
		t.Run(name, func(t *testing.T) { concurrentStartStop(t, mode) })
	}
}

func concurrentStartStop(t *testing.T, mode string) {
	e := newEnv(t)
	e.km.EndMode = mode
	d := e.hookDeps()
	s := waitingEvents{make(chan struct{}), make(chan struct{})}
	d.Events = s
	h, err := m0hook.New(context.Background(), d)
	must(t, err)
	const n = 8
	waiting := make(chan struct{}, 2*n)
	m0hook.OnEffectWait(h, func() { waiting <- struct{}{} })
	firstDone := make(chan error, 1)
	go func() { _, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate); firstDone <- err }()
	<-s.entered
	var wg sync.WaitGroup
	errs := make([]error, n)
	stopErrs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = h.Start(context.Background(), startReq(effectA, cuid), e.gate)
		}(i)
		go func(i int) {
			defer wg.Done()
			_, stopErrs[i] = h.Stop(context.Background(), stopReq(effectA, cuid))
		}(i)
	}
	for i := 0; i < 2*n; i++ {
		<-waiting
	}
	close(s.release)
	must(t, <-firstDone)
	wg.Wait()
	for _, err := range stopErrs {
		if (err == nil) != (mode == "") || codes.Is(err, codes.EndUnknown) != (mode == "close") {
			t.Fatalf("concurrent Stop lost retained refusal: %v", err)
		}
	}
	starts, _ := e.inner.counts()
	srcDials, _ := e.srcDialer.Dials()
	if len(e.km.Binds()) > 1 || e.kmDialer.Dials() > 1 || starts > 1 || srcDials > 1 || len(e.store.Keys()) > 1 {
		t.Fatalf("binds=%d dials=%d starts=%d source=%d keys=%v", len(e.km.Binds()), e.kmDialer.Dials(), starts, srcDials, e.store.Keys())
	}
	first := 0
	for _, err := range errs {
		if !errors.Is(err, m0hook.ErrEffectKnown) {
			first++
		}
	}
	if first != 0 {
		t.Fatalf("%d duplicate starts were not refused as a known effect: %v", first, errs)
	}
	_, err = h.Stop(context.Background(), stopReq("final-stop", cuid))
	if err != nil && !(mode == "close" && codes.Is(err, codes.EndUnknown)) {
		t.Fatalf("final Stop: %v", err)
	}
	a, _ := h.Last(effectA)
	want := runrecord.Ended
	if mode == "close" {
		want = runrecord.Ending
	}
	if len(e.km.Ends()) != 1 || e.only().State != want || !a.Ended || (a.EndErr == nil) != (mode == "") || a.ChildStatusKnown {
		t.Fatalf("final attempt %+v record %+v ends=%d", a, e.only(), len(e.km.Ends()))
	}
	if _, err := os.Stat(filepath.Join(e.proc.Root, "777")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("child survived concurrent Stop")
	}
	kills := len(e.killer.UIDs())
	for i := 2; i <= 3; i++ {
		r, err := h.Stop(context.Background(), stopReq("later-stop", cuid))
		if (err == nil) != (mode == "") || codes.Is(err, codes.EndUnknown) != (mode == "close") || !r.Absent || string(r.Evidence) != "synthetic fenced absence" {
			t.Fatalf("Stop %d lost retained refusal: %+v %v", i, r, err)
		}
	}
	if len(e.km.Ends()) != 1 || len(e.killer.UIDs()) != kills {
		t.Fatal("live run retained after final Stop")
	}
}

// Claim: Observe returns the inner observation with no Source/Keymaker contact and no record creation.
func TestObservePassesThrough(t *testing.T) {
	e := newEnv(t)
	e.inner.observation = m0.ObservationPresent
	h := e.build()
	class, err := h.Observe(context.Background(), m0.EffectQuery{EffectID: effectA, Operation: "start"})
	must(t, err)
	srcDials, _ := e.srcDialer.Dials()
	if class != m0.ObservationPresent || e.inner.observes != 1 || srcDials != 0 || e.kmDialer.Dials() != 0 || len(e.store.Keys()) != 0 {
		t.Fatalf("class %q observes=%d source=%d keymaker=%d keys=%v", class, e.inner.observes, srcDials, e.kmDialer.Dials(), e.store.Keys())
	}
}

// Claim: missing or failing event seams retain the cause and refuse before any contact, record, start or commit; control: TestEventReachesSource.
func TestEventSeamRefusesBeforeContact(t *testing.T) {
	seamErr := errors.New("synthetic event lookup failure")
	check := func(t *testing.T, e *env, h *m0hook.Hooked) m0hook.Attempt {
		t.Helper()
		receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
		if err != nil || receipt.Committed {
			t.Fatalf("receipt %+v err %v", receipt, err)
		}
		starts, stops := e.inner.counts()
		srcDials, _ := e.srcDialer.Dials()
		if starts != 0 || stops != 0 || e.gate.count() != 0 || srcDials != 0 || e.kmDialer.Dials() != 0 || len(e.store.Keys()) != 0 {
			t.Fatalf("starts=%d stops=%d commits=%d source=%d keymaker=%d keys=%v", starts, stops, e.gate.count(), srcDials, e.kmDialer.Dials(), e.store.Keys())
		}
		a, ok := h.Last(effectA)
		if !ok || a.Err == nil || a.Key != "" || a.Ended {
			t.Fatalf("attempt %+v ok=%v", a, ok)
		}
		return a
	}
	t.Run("default-seam", func(t *testing.T) {
		e := newEnv(t)
		d := e.hookDeps()
		d.Events = nil
		h, err := m0hook.New(context.Background(), d)
		must(t, err)
		if a := check(t, e, h); !codes.IsUnpinned(a.Err) {
			t.Fatalf("default seam did not refuse by name: %v", a.Err)
		}
	})
	t.Run("seam-error", func(t *testing.T) {
		e := newEnv(t)
		e.events.err = seamErr
		h := e.build()
		if a := check(t, e, h); !errors.Is(a.Err, seamErr) || e.events.count() != 1 {
			t.Fatalf("attempt %+v events=%d", a, e.events.count())
		}
	})
}

// Claim: the pinned Source prepare receives the exact retained event and worker principal; positive control for TestEventSeamRefusesBeforeContact.
func TestEventReachesSource(t *testing.T) {
	e := newEnv(t)
	ps := &fakes.PinnedSource{Registration: "1", Peer: source.Credential{UID: luid, PID: 1}}
	e.deps.Source.Wire = source.Pinned{}
	e.deps.Source.Dialer = ps
	var n atomic.Uint64
	e.deps.Nonce = func() (string, error) { return fmt.Sprintf("%032x", n.Add(1)), nil }
	want := runorigin.EventRef{RoomID: "!room-b:example", EventID: "$event-b"}
	e.events.event = want
	h := e.build()
	receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	must(t, err)
	if errs := ps.Errs(); len(errs) != 0 {
		t.Fatalf("pinned source fixture: %v", errs)
	}
	prepares := ps.Prepares()
	if !receipt.Committed || len(prepares) != 1 || prepares[0].Event != want || prepares[0].InputPrincipal.UID != cuid || e.events.count() != 1 {
		t.Fatalf("receipt %+v prepares %+v events=%d", receipt, prepares, e.events.count())
	}
	_, err = h.Stop(context.Background(), stopReq(effectA, cuid))
	must(t, err)
}

// Claim: Begin/End/restart sync failures return and latch the M0 fatal class, blocking later starts; ordinary store refusals are adjacent controls.
func TestMustExitReachesFatalClass(t *testing.T) {
	syncFailure := errors.Join(errStoreRefused, journal.ErrSyncFailure)
	cases := []struct {
		name  string
		cause error
		fatal bool
	}{{"sync", syncFailure, true}, {"ordinary", errStoreRefused, false}}
	// refusedLater asserts that a Start for a new effect is refused in the
	// fatal class with no contact and no claim.
	refusedLater := func(t *testing.T, e *env, h *m0hook.Hooked) {
		t.Helper()
		srcBefore, _ := e.srcDialer.Dials()
		kmBefore, eventsBefore := e.kmDialer.Dials(), e.events.count()
		starts, _ := e.inner.counts()
		_, err := h.Start(context.Background(), startReq("effect-later", cuid), e.gate)
		srcAfter, _ := e.srcDialer.Dials()
		startsAfter, _ := e.inner.counts()
		if !m0.MustExit(err) || !errors.Is(err, m0hook.ErrMustExit) || srcAfter != srcBefore || e.kmDialer.Dials() != kmBefore || e.events.count() != eventsBefore || startsAfter != starts {
			t.Fatalf("start after the latch: %v source %d->%d keymaker %d->%d", err, srcBefore, srcAfter, kmBefore, e.kmDialer.Dials())
		}
		if _, ok := h.Last("effect-later"); ok {
			t.Fatal("a start refused by the latch claimed its effect")
		}
	}
	for _, c := range cases {
		t.Run("begin-"+c.name, func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			g.set(refuseState(runrecord.Bound, c.cause))
			h := e.build()
			_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			if !codes.Is(err, codes.BindUnknown) || m0.MustExit(err) != c.fatal || errors.Is(err, m0hook.ErrMustExit) != c.fatal {
				t.Fatalf("start: %v", err)
			}
			if h.ExitRequired() != c.fatal || m0.MustExit(h.ExitCause()) != c.fatal || errors.Is(h.ExitCause(), m0hook.ErrMustExit) != c.fatal {
				t.Fatalf("latch: required=%v cause=%v", h.ExitRequired(), h.ExitCause())
			}
			if starts, _ := e.inner.counts(); starts != 0 || e.gate.count() != 0 || e.only().State != runrecord.Binding {
				t.Fatalf("starts=%d commits=%d record %+v", starts, e.gate.count(), e.only())
			}
			g.set(nil)
			if c.fatal {
				refusedLater(t, e, h)
				return
			}
			// Neighbour: the next start is not refused by a latch; it reaches
			// the event seam and the launcher.
			_, err = h.Start(context.Background(), startReq("effect-later", cuid), e.gate)
			if m0.MustExit(err) || errors.Is(err, m0hook.ErrMustExit) || e.events.count() != 2 {
				t.Fatalf("neighbour start: %v events=%d", err, e.events.count())
			}
		})
		t.Run("end-"+c.name, func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			h := e.build()
			_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			must(t, err)
			g.set(refuseState(runrecord.Ending, c.cause))
			receipt, err := h.Stop(context.Background(), stopReq(effectA, cuid))
			if !receipt.Absent || string(receipt.Evidence) != "synthetic fenced absence" {
				t.Fatalf("stop receipt changed: %+v", receipt)
			}
			if err == nil || m0.MustExit(err) != c.fatal || errors.Is(err, m0hook.ErrMustExit) != c.fatal {
				t.Fatalf("stop: %v", err)
			}
			if h.ExitRequired() != c.fatal || m0.MustExit(h.ExitCause()) != c.fatal {
				t.Fatalf("latch: required=%v cause=%v", h.ExitRequired(), h.ExitCause())
			}
			a, _ := h.Last(effectA)
			if !a.Ended || a.End == nil || a.End.MustExit != c.fatal || !codes.Is(a.EndErr, codes.EndUnknown) || !errors.Is(a.EndErr, errStoreRefused) {
				t.Fatalf("attempt %+v", a)
			}
			// No end was sent without a durable intent; the inner stop ran
			// and the uid sweep still killed the child.
			if _, stops := e.inner.counts(); stops != 1 || len(e.km.Ends()) != 0 || e.record(a.Key).State == runrecord.Ended {
				t.Fatalf("stops=%d ends=%d record %+v", stops, len(e.km.Ends()), e.record(a.Key))
			}
			if _, err := os.Stat(filepath.Join(e.proc.Root, "777")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("child survived the end path")
			}
			g.set(nil)
			if c.fatal {
				refusedLater(t, e, h)
			}
		})
		t.Run("restart-"+c.name, func(t *testing.T) {
			e := newEnv(t)
			dead := &incarnation.Incarnation{PID: 4242, UID: luid, StartTicks: 1, BootID: "boot-1"}
			plantBound(t, e, "old", 30002, 300, dead)
			e.reopen()
			g := gated(e)
			g.set(refuseState(runrecord.Ending, c.cause))
			h, err := m0hook.New(context.Background(), e.hookDeps())
			if h == nil || err == nil || m0.MustExit(err) != c.fatal || errors.Is(err, m0hook.ErrMustExit) != c.fatal || h.ExitRequired() != c.fatal {
				t.Fatalf("new: hooked=%v err=%v", h != nil, err)
			}
			reports := h.Reports()
			if len(reports) < 1 || reports[0].Key != "old" || !codes.Is(reports[0].Err, codes.EndUnknown) || len(e.km.Ends()) != 0 {
				t.Fatalf("reports %+v ends=%d", reports, len(e.km.Ends()))
			}
			g.set(nil)
			if c.fatal {
				refusedLater(t, e, h)
				return
			}
			// An ordinary recovery failure is visible too and prevents admission.
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			if !errors.Is(err, m0hook.ErrNotRestarted) || receipt.Committed || len(e.km.Binds()) != 0 {
				t.Fatalf("failed restart admitted start: %+v err=%v binds=%d", receipt, err, len(e.km.Binds()))
			}
		})
	}
}

// Claim: after an acknowledged or unknown first End, repeated Stop preserves the first outcome without another End or sweep; first Stop is the control.
func TestRunEndedOnce(t *testing.T) {
	for _, c := range []struct {
		name    string
		endMode string
		acked   bool
	}{{"acknowledged", "", true}, {"unacknowledged", "close", false}} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.km.EndMode = c.endMode
			h := e.build()
			_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			must(t, err)
			_, err = h.Stop(context.Background(), stopReq(effectA, cuid))
			if (err == nil) != c.acked {
				t.Fatalf("first end error %v acknowledged=%v", err, c.acked)
			}
			first, _ := h.Last(effectA)
			if !first.Ended || first.End == nil || first.End.Acknowledged != c.acked || (first.EndErr == nil) != c.acked || len(e.km.Ends()) != 1 {
				t.Fatalf("first stop: attempt %+v ends=%d", first, len(e.km.Ends()))
			}
			kills := len(e.killer.UIDs())
			for i := 2; i <= 3; i++ {
				r, err := h.Stop(context.Background(), stopReq(effectA, cuid))
				if (err == nil) != c.acked || codes.Is(err, codes.EndUnknown) == c.acked || !r.Absent || string(r.Evidence) != "synthetic fenced absence" {
					t.Fatalf("Stop %d retained refusal: %+v %v", i, r, err)
				}
				second, _ := h.Last(effectA)
				if second.End != first.End || errors.Is(second.EndErr, launcher.ErrEndCalled) || (second.EndErr == nil) != c.acked || second.Code != first.Code {
					t.Fatalf("repeated stop ended the run again: first %+v second %+v", first, second)
				}
				if _, stops := e.inner.counts(); stops != i || len(e.km.Ends()) != 1 || len(e.killer.UIDs()) != kills {
					t.Fatalf("repeated stop: stops=%d ends=%d kills %d->%d", stops, len(e.km.Ends()), kills, len(e.killer.UIDs()))
				}
			}
		})
	}
}
