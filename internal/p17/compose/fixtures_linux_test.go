//go:build linux && (amd64 || arm64)

package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/hygiene"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func doc(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../../fixtures", name))
	must(t, err)
	return b
}
func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	return b
}

const dispatchID = "00000000000000000000000000000001"
const cancelID = "00000000000000000000000000000002"

func wireRefusal(t *testing.T, body string, err error, code string) {
	t.Helper()
	must(t, err)
	var envelope struct {
		OK    *bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	must(t, json.Unmarshal([]byte(body), &envelope))
	if envelope.OK == nil || *envelope.OK || len(envelope.Result) != 0 || envelope.Error.Code != code {
		t.Fatalf("expected refusal envelope %q, got %s", code, body)
	}
}

// fixtureAncestorsSafe reports whether dir and every ancestor is a real
// directory owned by root or this UID and not group- or other-writable, which
// is what protectedDirectory requires of an installation path.
func fixtureAncestorsSafe(dir string) bool {
	uid := uint32(os.Geteuid())
	for p := dir; ; p = filepath.Dir(p) {
		var st syscall.Stat_t
		if syscall.Lstat(p, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFDIR || (st.Uid != 0 && st.Uid != uid) || st.Mode&0022 != 0 {
			return false
		}
		if p == "/" {
			return true
		}
	}
}

// fixtureBase picks the temporary directory, then the CI runner's temporary
// directory, then the checkout-local ignored .temp directory, taking the first
// whose ancestors are protected (a world-writable /tmp is not). The runner
// directory comes before the checkout because fixture socket paths must stay
// within the Unix socket path limit, and a deep checkout path can exceed it.
// Nothing is repaired; with none usable the test fails, so no installation
// control is silently skipped.
func fixtureBase(t *testing.T) string {
	t.Helper()
	if tmp, err := filepath.EvalSymlinks(os.TempDir()); err == nil && fixtureAncestorsSafe(tmp) {
		return tmp
	}
	if runner := os.Getenv("RUNNER_TEMP"); runner != "" {
		if dir, err := filepath.EvalSymlinks(runner); err == nil && filepath.IsAbs(dir) && fixtureAncestorsSafe(dir) {
			return dir
		}
	}
	local, err := filepath.Abs(filepath.Join("..", "..", "..", ".temp"))
	if err == nil && fixtureAncestorsSafe(filepath.Dir(local)) {
		if err := os.Mkdir(local, 0700); err == nil || os.IsExist(err) {
			if fixtureAncestorsSafe(local) {
				return local
			}
		}
	}
	t.Fatal("no fixture directory whose ancestors are all owned by root or this UID and not group/other-writable")
	return ""
}

func privateDir(t *testing.T, parent, pattern string) string {
	t.Helper()
	if parent == "" {
		parent = fixtureBase(t)
	}
	d, err := os.MkdirTemp(parent, pattern)
	must(t, err)
	must(t, os.Chmod(d, 0700))
	return d
}

type fixtureGate struct {
	classes []string
	err     error
}

func (g *fixtureGate) Verify(class string) error { g.classes = append(g.classes, class); return g.err }

type fixtureStops struct {
	verifications, requests int
	err                     error
}

func (s *fixtureStops) VerifyPinned() error { s.verifications++; return s.err }
func (s *fixtureStops) StopRequest(m0.StartRequest) (m0.StopRequest, error) {
	s.requests++
	return m0.StopRequest{EffectID: "fixture-stop", ExecutionID: "fixture-execution", Worker: m0.Worker{UID: 30002, Generation: "cccccccccccccccccccccccccccccccc"}, Event: 1700000000, Detection: 1700000000, Obligations: []m0.StopObligation{{Source: "expiry", Event: 1700000000, Detection: 1700000000}}, Ceiling: 1700000120}, nil
}

type fixturePolicy struct{ installed *m0.Installed }

func (p fixturePolicy) Current() (*m0.Installed, error) { return p.installed, nil }

type fixtureClock struct{ now int64 }

func (c fixtureClock) Now() (m0.Reading, error) { return m0.Reading{Unix: c.now, Confident: true}, nil }

type fixtureGenerations map[uint32]string

func (g fixtureGenerations) Current(uid uint32) (m0.Generation, error) {
	return m0.Generation{Generation: g[uid], Active: g[uid] != ""}, nil
}

type fixtureEntry struct {
	p     *m0.Policy
	input m0.Input
}

func (e fixtureEntry) Compose(r *m0.Request, x m0.Execution, t m0.Timing) (*m0.Entry, error) {
	return &m0.Entry{Request: r, Execution: x, Timing: t, Catalog: e.p.Catalog, Qualification: e.p.Qualification, Input: e.input}, nil
}

type fixtureRunner struct{}

func (fixtureRunner) Argv(*m0.Entry, int64) ([]string, error) {
	return []string{"/usr/bin/fixture-worker"}, nil
}

type fixtureInner struct {
	starts, stops int
	startErr      error
	atStart       func()
}

func (i *fixtureInner) Start(context.Context, m0.StartRequest, m0.CommitGate) (m0.StartReceipt, error) {
	i.starts++
	if i.atStart != nil {
		i.atStart()
	}
	return m0.StartReceipt{}, i.startErr
}
func (i *fixtureInner) Stop(context.Context, m0.StopRequest) (m0.StopReceipt, error) {
	i.stops++
	return m0.StopReceipt{Absent: true, Evidence: []byte("fixture fenced absence")}, nil
}
func (*fixtureInner) Observe(context.Context, m0.EffectQuery) (m0.Observation, error) {
	return m0.ObservationUnknown, nil
}

type fixtureEvents struct {
	calls   int
	err     error
	request m0.StartRequest
}

func (e *fixtureEvents) Event(r m0.StartRequest) (runorigin.EventRef, error) {
	e.calls++
	e.request = r
	return runorigin.EventRef{RoomID: "!room:example", EventID: "$retained-event"}, e.err
}

type fixtureInterface struct{ launch hygiene.Launch }

func (i fixtureInterface) Launch(m0.StartRequest) (hygiene.Launch, error) { return i.launch, nil }

type fixtureChild struct{ value incarnation.Incarnation }

func (c fixtureChild) Child(m0.StartRequest, m0.StartReceipt) (incarnation.Incarnation, error) {
	return c.value, nil
}

type fixture struct {
	t                    *testing.T
	root, state, m0state string
	config               []byte
	cfg                  *m0.DaemonConfig
	seams                m0.Seams
	deps                 Deps
	gate                 *fixtureGate
	stops                *fixtureStops
	events               *fixtureEvents
	inner                *fixtureInner
	source               *fakes.SourceDialer
	keymakerDials        atomic.Int32
	keymakerAccepted     chan struct{}
	frame, cancel        []byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	previous := syscall.Umask(0077)
	t.Cleanup(func() { syscall.Umask(previous) })
	f := &fixture{t: t, keymakerAccepted: make(chan struct{}, 16)}
	f.root = privateDir(t, "", "composition-")
	t.Cleanup(func() { must(t, os.RemoveAll(f.root)) })
	for _, p := range []string{"p17", "p17/records", "p17/inputs", "m0", "m0/records", "m0/inputs", "km", "source", "caller", "proc"} {
		must(t, os.Mkdir(filepath.Join(f.root, p), 0700))
	}
	f.state = filepath.Join(f.root, "p17")
	f.m0state = filepath.Join(f.root, "m0")
	uid := uint32(os.Getuid())
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	key := "ed25519:" + base64.RawURLEncoding.EncodeToString(pub)
	var specimen struct {
		Input map[string]string `json:"input"`
	}
	must(t, json.Unmarshal(doc(t, "m0-executor-entry/entry.specimen.json"), &specimen))
	in := specimen.Input
	input := m0.Input{Account: in["account"], Profile: in["profile"], Model: in["model"], ReasoningEffort: in["reasoning_effort"], Prompt: in["prompt"]}
	_, inputDigest, err := input.Canonical()
	must(t, err)
	var policy map[string]any
	must(t, json.Unmarshal(doc(t, "m0-launch/policy.valid.json"), &policy))
	policy["association"].(map[string]any)["key"] = key
	policy["plan"].(map[string]any)["worker_plan"] = inputDigest
	p, err := m0.DecodePolicy(encode(t, policy))
	must(t, err)
	makePlan := func(name string) (*m0.SigningPlan, []byte) {
		var request map[string]any
		must(t, json.Unmarshal(doc(t, "m0-launch/"+name), &request))
		delete(request, "auth")
		request["association"].(map[string]any)["key"] = key
		request["plan"].(map[string]any)["worker_plan"] = inputDigest
		parsed, err := m0.DecodeSigningPlan(encode(t, map[string]any{"kind": m0.SigningPlanKind, "profile": m0.SigningProfile, "request": request, "v": 1}))
		must(t, err)
		request["auth"] = map[string]any{"key": key, "sig": base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, parsed.Request.Preimage))}
		frame := encode(t, request)
		_, err = m0.DecodeRequest(frame)
		must(t, err)
		return parsed, frame
	}
	dispatch, frame := makePlan("dispatch.valid.json")
	cancel, cancelFrame := makePlan("cancel.valid.json")
	f.frame, f.cancel = frame, cancelFrame
	f.inner = &fixtureInner{}
	f.seams = m0.Seams{Policy: fixturePolicy{&m0.Installed{Policy: p, Catalog: m0.Catalog{Dispatch: dispatch, Cancel: cancel}, InstallRevision: 1, Qualified: m0.Qualified{HandoffMs: 1000, ResidualMs: 1000, SlackMs: 1000, IndependentStop: true}}}, Launcher: f.inner, Clock: fixtureClock{now: p.NotBefore + 10}, Generation: fixtureGenerations{p.Association.UID: p.Association.Generation, p.Worker.UID: p.Worker.Generation}, Runner: fixtureRunner{}, Entry: fixtureEntry{p: p, input: input}}
	// The pre-existing M0 installation is independent of the two new P17 facts.
	mask := uint32(0077)
	rt, err := m0.Initialize(journal.Config{Directory: f.m0state, Records: "records", Inputs: "inputs", DeclaredUmask: &mask, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 4 << 20}, f.seams)
	must(t, err)
	must(t, rt.Close())
	control, stopSource, err := fakes.ControlDir(filepath.Join(f.root, "source"), nil)
	must(t, err)
	t.Cleanup(stopSource)
	p17 := map[string]any{"supported_local_storage": true, "recovery_attested": true, "bind_timeout_ms": 1000, "keymaker_gid": os.Getgid(), "keymaker_mode": 384, "keymaker_socket_dir": filepath.Join(f.root, "km"), "keymaker_socket_name": "request.sock", "keymaker_uid": uid, "source_control_dir": control, "source_generation": "11111111111111111111111111111111", "source_socket_name": "origin.sock", "source_uid": uid, "state_dir": f.state, "station": "1"}
	f.config = encode(t, map[string]any{"callers": []any{map[string]any{"dev": 1, "dir": filepath.Join(f.root, "caller"), "ino": 2, "uid": 30001}}, "enabled": true, "entry": "established", "kind": "swarma-dispatcher m0-config/3", "p17": p17, "recovery_attested": true, "state_dir": f.m0state, "supported_local_storage": true, "v": 3})
	f.cfg, err = m0.ParseConfig(f.config)
	must(t, err)
	proc, err := fakes.NewProcTree(filepath.Join(f.root, "proc"), "fixture-boot")
	must(t, err)
	must(t, proc.Add(os.Getpid(), [4]uint32{uid, uid, uid, uid}, 1))
	a, b, err := fakes.SocketPair()
	must(t, err)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	fa, err := fakes.RawFD(a)
	must(t, err)
	fb, err := fakes.RawFD(b)
	must(t, err)
	f.gate = &fixtureGate{}
	f.stops = &fixtureStops{}
	f.events = &fixtureEvents{}
	f.source = &fakes.SourceDialer{Script: &fakes.SourceScript{}, Peer: source.Credential{UID: uid, PID: int32(os.Getpid())}}
	f.deps = DefaultDeps()
	f.deps.Static = f.gate
	f.deps.Stops = f.stops
	f.deps.Events = f.events
	f.deps.ChildInterface = fixtureInterface{hygiene.Launch{Argv: []string{"/usr/bin/fixture-worker"}, Env: []string{"PATH=/usr/bin", "HOME=/home/worker"}, ProtectedPaths: []string{f.state, filepath.Join(f.cfg.P17.SourceControlDir, "origin.sock")}, ProtectedFDs: []int{fa, fb}, InheritedFDs: []int{0, 1, 2}}}
	f.deps.ChildIncarnations = fixtureChild{incarnation.Incarnation{UID: 30002, PID: 777, StartTicks: 42, BootID: "fixture-boot"}}
	f.deps.Generations = source.StaticGenerations{uid: "11111111111111111111111111111111"}
	f.deps.Incarnations = proc.Incarnations()
	f.deps.Killer = &fakes.Killer{Tree: proc}
	f.deps.Export = sweep.StaticWorkProductExport{"fixture": sweep.NoWorkingCopy}
	f.deps.Release = sweep.StaticOperatorRelease{"fixture": 30002}
	f.deps.sourceWire = fakes.SourceWire{}
	f.deps.sourceDialer = f.source
	f.deps.procRoot = proc.Root
	f.deps.mountInfo = proc.MountInfoPath()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(f.root, "km/request.sock"), Net: "unix"})
	must(t, err)
	must(t, os.Chmod(filepath.Join(f.root, "km/request.sock"), os.FileMode(f.cfg.P17.KeymakerMode)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			f.keymakerDials.Add(1)
			f.keymakerAccepted <- struct{}{}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return f
}
func (f *fixture) read(string) ([]byte, error) { return append([]byte(nil), f.config...), nil }
func (f *fixture) attach() (*Session, error) {
	return Attach(context.Background(), "/configuration/m0.json", f.seams, f.deps, f.read)
}
func (f *fixture) open() *Session {
	f.t.Helper()
	s, err := f.attach()
	must(f.t, err)
	f.t.Cleanup(func() { must(f.t, s.Close()) })
	return s
}
func (f *fixture) assertNoContacts(s *Session) {
	f.t.Helper()
	dials, _ := f.source.Dials()
	if s.quarantine.Held(30002) {
		f.t.Fatal("generation pre-check leaked its lease")
	}
	if dials != 0 || f.keymakerDials.Load() != 0 || f.inner.starts != 0 || len(s.store.Keys()) != 0 {
		f.t.Fatal("generation boundary contacted a service, started a child, or wrote a record")
	}
}
func (f *fixture) record() *runrecord.Record {
	return &runrecord.Record{Version: 1, Kind: "p17-run-record/1", Key: "fixture-record", LaunchNonce: "fixture-record", State: runrecord.Binding, LauncherUID: uint32(os.Getuid()), LauncherGeneration: "11111111111111111111111111111111", StationRegistration: "1", InputPrincipal: `{"uid":30002,"generation":"cccccccccccccccccccccccccccccccc"}`, Origin: json.RawMessage(`{}`), SourceIncarnation: "fixture-source", PreparedID: "fixture-prepare", BindRequestID: "fixture-bind", ChildUID: 30002, Launcher: &incarnation.Incarnation{UID: uint32(os.Getuid()), PID: os.Getpid(), StartTicks: 1, BootID: "fixture-boot"}}
}

var errFixture = errors.New("fixture ordinary launcher refusal")
