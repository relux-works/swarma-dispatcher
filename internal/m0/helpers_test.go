package m0

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantCode(t *testing.T, err error, code Code) {
	t.Helper()
	if CodeOf(err) != code {
		t.Fatalf("code: got %v (%q), want %q", err, CodeOf(err), code)
	}
}

func fixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "fixtures", dir, name))
	must(t, err)
	return b
}

func parseDoc(t *testing.T, b []byte) *value {
	t.Helper()
	v, _, err := canonicalize(b, policyLimits)
	must(t, err)
	return v
}

// setPath replaces (or adds) the member at a dotted path in an object tree.
func setPath(t *testing.T, v *value, path string, nv *value) {
	t.Helper()
	parts := strings.Split(path, ".")
	cur := v
	for _, p := range parts[:len(parts)-1] {
		cur = cur.member(p)
		if cur == nil {
			t.Fatalf("path %s missing", path)
		}
	}
	last := parts[len(parts)-1]
	for i := range cur.members {
		if cur.members[i].key == last {
			cur.members[i].val = nv
			return
		}
	}
	cur.members = append(cur.members, member{last, nv})
	cur.sortMembers()
}

type testKey struct {
	pub      ed25519.PublicKey
	priv     ed25519.PrivateKey
	spelling string
}

func newKey(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	return testKey{pub: pub, priv: priv, spelling: keyPrefix + base64.RawURLEncoding.EncodeToString(pub)}
}

// signRequest signs the unsigned request tree over the exact preimage and
// returns the signed wire bytes.
func signRequest(t *testing.T, unsigned *value, k testKey) []byte {
	t.Helper()
	r, err := decodeUnsigned(unsigned)
	must(t, err)
	sig := ed25519.Sign(k.priv, r.Preimage)
	signed := &value{kind: kindObject, members: append(append([]member(nil), unsigned.members...), member{"auth", object(
		member{"key", text(k.spelling)},
		member{"sig", text(base64.RawURLEncoding.EncodeToString(sig))},
	)})}
	signed.sortMembers()
	return signed.encode(nil)
}

func specimenInput(t *testing.T) Input {
	t.Helper()
	v := parseDoc(t, fixture(t, "m0-executor-entry", "entry.specimen.json")).member("input")
	get := func(k string) string {
		s, ok := v.member(k).text()
		if !ok {
			t.Fatalf("specimen input member %s", k)
		}
		return s
	}
	return Input{Account: get("account"), Profile: get("profile"), Model: get("model"), ReasoningEffort: get("reasoning_effort"), Prompt: get("prompt")}
}

// memLedger is an in-memory ledger for runtime tests. It is not the journal;
// the Linux tests cover the real store.
type memLedger struct {
	mu           sync.Mutex
	frames       [][]byte
	records      map[string][]byte
	appendCalls  int
	failAppendAt int
	failErr      error
	ended        bool
}

func newMemLedger() *memLedger { return &memLedger{records: map[string][]byte{}} }

func (m *memLedger) Append(b []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendCalls++
	if m.ended {
		return 0, &journal.Error{Class: journal.InvalidState, Op: "session ended"}
	}
	if m.failAppendAt != 0 && m.appendCalls == m.failAppendAt {
		m.ended = true
		return 0, m.failErr
	}
	m.frames = append(m.frames, append([]byte(nil), b...))
	return uint64(len(m.frames)), nil
}

func (m *memLedger) ReplaceRecord(name string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ended {
		return &journal.Error{Class: journal.InvalidState, Op: "session ended"}
	}
	m.records[name] = append([]byte(nil), b...)
	return nil
}

func (m *memLedger) State() journal.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return journal.State{EOF: 0, Next: uint64(len(m.frames)) + 1, Ready: !m.ended, Closed: m.ended}
}

func (m *memLedger) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ended = true
	return nil
}

func (m *memLedger) count(ft frameType) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, b := range m.frames {
		f, err := decodeFrame(b)
		if err == nil && f.Type == ft {
			n++
		}
	}
	return n
}

// syncFailure is the journal's poisoned sync-failure shape.
func syncFailure() error {
	return &journal.Error{Class: journal.StorageWriteFailure, Op: "fsync", Cause: journal.ErrSyncFailure, Poisoned: true}
}

type fakePolicy struct {
	mu   sync.Mutex
	inst *Installed
	err  error
}

func (f *fakePolicy) Current() (*Installed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	cp := *f.inst
	return &cp, nil
}

func (f *fakePolicy) edit(fn func(*Installed)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *f.inst
	fn(&cp)
	f.inst = &cp
}

type fakeClock struct {
	mu        sync.Mutex
	now       int64
	confident bool
	err       error
}

func (c *fakeClock) Now() (Reading, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return Reading{}, c.err
	}
	return Reading{Unix: c.now, Confident: c.confident}, nil
}

func (c *fakeClock) set(now int64) { c.mu.Lock(); c.now = now; c.mu.Unlock() }

type fakeGenerations struct {
	mu      sync.Mutex
	current map[uint32]Generation
	err     error
}

func (g *fakeGenerations) Current(uid uint32) (Generation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return Generation{}, g.err
	}
	return g.current[uid], nil
}

type fakeComposer struct {
	input         Input
	catalog       string
	qualification string
}

func (c fakeComposer) Compose(r *Request, exec Execution, timing Timing) (*Entry, error) {
	return &Entry{Request: r, Execution: exec, Catalog: c.catalog, Qualification: c.qualification, Timing: timing, Input: c.input}, nil
}

type fakeLauncher struct {
	mu         sync.Mutex
	clock      *fakeClock
	receiver   string
	drop       bool
	execDelay  int64
	factsEdit  func(*ReceiverFacts)
	onStart    func()
	startErr   error
	stopAbsent bool
	stopErr    error
	observe    Observation
	starts     int
	stops      int
	observes   int
	commitErr  error
	lastStop   StopRequest
}

func (l *fakeLauncher) Start(_ context.Context, req StartRequest, gate CommitGate) (StartReceipt, error) {
	l.mu.Lock()
	l.starts++
	onStart, startErr, drop, edit, delay := l.onStart, l.startErr, l.drop, l.factsEdit, l.execDelay
	l.mu.Unlock()
	if onStart != nil {
		onStart()
	}
	if startErr != nil {
		return StartReceipt{}, startErr
	}
	facts := ReceiverFacts{DropVerified: drop, Receiver: l.receiver, WorkerUID: req.Worker.UID, Generation: req.Worker.Generation, ControllerEpoch: req.ControllerEpoch, ExecutionID: req.ExecutionID, EffectID: req.EffectID}
	if edit != nil {
		edit(&facts)
	}
	if err := gate.Commit(facts); err != nil {
		l.mu.Lock()
		l.commitErr = err
		l.mu.Unlock()
		return StartReceipt{Committed: false}, nil
	}
	now, _ := l.clock.Now()
	return StartReceipt{Committed: true, CommitUnix: now.Unix, ExecUnix: now.Unix + delay, Receipt: []byte("receipt")}, nil
}

func (l *fakeLauncher) Stop(_ context.Context, req StopRequest) (StopReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stops++
	l.lastStop = req
	if l.stopErr != nil {
		return StopReceipt{}, l.stopErr
	}
	if l.stopAbsent {
		return StopReceipt{Absent: true, Evidence: []byte("fenced account-wide absence")}, nil
	}
	return StopReceipt{}, nil
}

func (l *fakeLauncher) Observe(context.Context, EffectQuery) (Observation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.observes++
	if l.observe == "" {
		return ObservationUnknown, nil
	}
	return l.observe, nil
}

func (l *fakeLauncher) count() (starts, stops int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts, l.stops
}

// testEnv is one pinned, synthetic installation: the fixture policy and plans
// re-keyed to a test key, worker_plan replaced by the specimen input digest.
type testEnv struct {
	key          testKey
	policy       *Policy
	source       *fakePolicy
	clock        *fakeClock
	gens         *fakeGenerations
	launcher     *fakeLauncher
	composer     fakeComposer
	store        *memLedger
	rt           *Runtime
	dispatchDoc  *value
	cancelDoc    *value
	dispatchWire []byte
	cancelWire   []byte
	peer         uint32
}

func rekey(t *testing.T, v *value, prefix string, k testKey, workerPlan string) {
	t.Helper()
	setPath(t, v, prefix+"association.key", text(k.spelling))
	setPath(t, v, prefix+"plan.worker_plan", text(workerPlan))
}

func buildInstalled(t *testing.T, k testKey, workerPlan string) *Installed {
	t.Helper()
	pv := parseDoc(t, fixture(t, "m0-launch", "policy.valid.json"))
	rekey(t, pv, "", k, workerPlan)
	policy, err := DecodePolicy(pv.encode(nil))
	must(t, err)
	spv := parseDoc(t, fixture(t, "m0-launch", "signing-plan.valid.json"))
	rekey(t, spv, "request.", k, workerPlan)
	dispatchPlan, err := DecodeSigningPlan(spv.encode(nil))
	must(t, err)
	cv := parseDoc(t, fixture(t, "m0-launch", "cancel.valid.json")).without("auth")
	rekey(t, cv, "", k, workerPlan)
	cpv := object(member{"v", integer(1)}, member{"kind", text(SigningPlanKind)}, member{"profile", text(SigningProfile)}, member{"request", cv})
	cancelPlan, err := DecodeSigningPlan(cpv.encode(nil))
	must(t, err)
	return &Installed{Policy: policy, Catalog: Catalog{Dispatch: dispatchPlan, Cancel: cancelPlan}, Qualified: Qualified{HandoffMs: 5000, ResidualMs: 5000, SlackMs: 10000, IndependentStop: true}, InstallRevision: 1}
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	k := newKey(t)
	input := specimenInput(t)
	_, workerPlan, err := input.Canonical()
	must(t, err)
	inst := buildInstalled(t, k, workerPlan)
	dv := parseDoc(t, fixture(t, "m0-launch", "dispatch.valid.json")).without("auth")
	rekey(t, dv, "", k, workerPlan)
	cv := parseDoc(t, fixture(t, "m0-launch", "cancel.valid.json")).without("auth")
	rekey(t, cv, "", k, workerPlan)
	policy := inst.Policy
	e := &testEnv{
		key:         k,
		policy:      policy,
		source:      &fakePolicy{inst: inst},
		clock:       &fakeClock{now: policy.NotBefore + 10, confident: true},
		gens:        &fakeGenerations{current: map[uint32]Generation{policy.Association.UID: {policy.Association.Generation, true}, policy.Worker.UID: {policy.Worker.Generation, true}}},
		composer:    fakeComposer{input: input, catalog: policy.Catalog, qualification: policy.Qualification},
		store:       newMemLedger(),
		dispatchDoc: dv,
		cancelDoc:   cv,
		peer:        policy.Association.UID,
	}
	e.launcher = &fakeLauncher{clock: e.clock, receiver: policy.Receiver, drop: true, stopAbsent: true}
	e.dispatchWire = signRequest(t, dv, k)
	e.cancelWire = signRequest(t, cv, k)
	e.rt, err = newRuntime(e.store, e.seams())
	must(t, err)
	return e
}

func (e *testEnv) seams() Seams {
	return Seams{Policy: e.source, Launcher: e.launcher, Runner: CodexRunner{Executable: "/example/protected/swarma-credential-broker", Home: "/example/station"}, Clock: e.clock, Generation: e.gens, Entry: e.composer}
}

func (e *testEnv) dispatch(t *testing.T) (*Result, error) {
	t.Helper()
	return e.rt.Handle(context.Background(), e.peer, e.dispatchWire)
}

func (e *testEnv) cancel(t *testing.T) (*Result, error) {
	t.Helper()
	return e.rt.Handle(context.Background(), e.peer, e.cancelWire)
}

func (e *testEnv) record(t *testing.T, id string) dispatchRecord {
	t.Helper()
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	rec := e.rt.proj.Dispatches[id]
	if rec == nil {
		t.Fatalf("no record %s", id)
	}
	return *rec
}

const dispatchID = "00000000000000000000000000000001"
const cancelID = "00000000000000000000000000000002"

// reopen replays the ledger frames into a fresh runtime over a copy of the
// store, as established recovery would, and appends a new daemon epoch.
func (e *testEnv) reopen(t *testing.T) *Runtime {
	t.Helper()
	e.store.mu.Lock()
	frames := append([][]byte(nil), e.store.frames...)
	e.store.mu.Unlock()
	proj, err := replay(frames)
	must(t, err)
	store := newMemLedger()
	store.frames = frames
	e.store = store
	rt := &Runtime{seams: e.seams().complete(), store: store, proj: proj}
	rt.mu.Lock()
	err = rt.appendLocked(&frame{Type: frameOpen, Epoch: proj.Epoch + 1})
	rt.mu.Unlock()
	must(t, err)
	e.rt = rt
	return rt
}

var errFixture = errors.New("fixture failure")
