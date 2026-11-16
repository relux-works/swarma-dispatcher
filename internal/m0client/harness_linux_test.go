//go:build linux

package m0client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// ancestorsSafe reports whether dir and every ancestor is a real directory
// owned by root or this UID and not writable by group or others, which is
// what the endpoint pin requires of a fixture path.
func ancestorsSafe(dir string) bool {
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

// fixtureBase picks the temporary directory, or the checkout-local ignored
// .temp directory when the temporary directory has unsafe ancestors. Nothing
// is repaired; with neither usable the test skips with the reason.
func fixtureBase(t *testing.T) string {
	t.Helper()
	if tmp, err := filepath.EvalSymlinks(os.TempDir()); err == nil && ancestorsSafe(tmp) {
		return tmp
	}
	local, err := filepath.Abs(filepath.Join("..", "..", ".temp"))
	if err == nil && ancestorsSafe(filepath.Dir(local)) {
		if err := os.Mkdir(local, 0700); err == nil || os.IsExist(err) {
			if ancestorsSafe(local) {
				return local
			}
		}
	}
	t.Skip("no fixture directory whose ancestors are all owned by root or this UID and not group/other-writable")
	return ""
}

func randomID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	_, err := rand.Read(b[:])
	must(t, err)
	return hex.EncodeToString(b[:])
}

// env is one synthetic caller installation: a throwaway seed, the fixture
// policy re-keyed to it, a configuration, a state directory and an endpoint
// directory. Every captured output and the state directory are scanned for
// the seed when the test ends.
type env struct {
	t                        *testing.T
	root                     string
	seed                     []byte
	key                      string
	policy                   *m0.Policy
	input                    m0.Input
	doc                      map[string]any
	cfgPath                  string
	keyPath                  string
	stateDir                 string
	epDir                    string
	callerDir                string
	dispatchPath, cancelPath string
	host                     host
	dispatchID, cancelID     string
	mu                       sync.Mutex
	outputs                  [][]byte
}

func readFixture(t *testing.T, dir, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "fixtures", dir, name))
	must(t, err)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out map[string]any
	must(t, d.Decode(&out))
	return out
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the transport contract admits no root service UID; these fixtures use the current UID as the service UID")
	}
	base := fixtureBase(t)
	root, err := os.MkdirTemp(base, "caller")
	must(t, err)
	e := &env{t: t, root: root, seed: make([]byte, ed25519.SeedSize)}
	t.Cleanup(func() {
		e.scan()
		_ = os.Chmod(e.stateDir, 0700)
		_ = os.RemoveAll(root)
	})
	must(t, os.Chmod(root, 0700))
	_, err = rand.Read(e.seed)
	must(t, err)
	e.key, err = m0.KeySpelling(ed25519.NewKeyFromSeed(e.seed).Public().(ed25519.PublicKey))
	must(t, err)

	specimen := readFixture(t, "m0-executor-entry", "entry.specimen.json")["input"].(map[string]any)
	e.input = m0.Input{Account: specimen["account"].(string), Profile: specimen["profile"].(string), Model: specimen["model"].(string), ReasoningEffort: specimen["reasoning_effort"].(string), Prompt: specimen["prompt"].(string)}
	_, workerPlan, err := e.input.Canonical()
	must(t, err)
	policy := readFixture(t, "m0-launch", "policy.valid.json")
	policy["association"].(map[string]any)["key"] = e.key
	policy["plan"].(map[string]any)["worker_plan"] = workerPlan
	body, err := json.Marshal(policy)
	must(t, err)
	e.policy, err = m0.DecodePolicy(body)
	must(t, err)

	e.cfgPath = filepath.Join(root, "caller.json")
	e.stateDir = filepath.Join(root, "state")
	e.epDir = filepath.Join(root, "ep")
	keys := filepath.Join(root, "keys")
	e.keyPath = filepath.Join(keys, "seed")
	must(t, os.Mkdir(e.stateDir, 0700))
	must(t, os.Chmod(e.stateDir, 0700))
	must(t, os.Mkdir(e.epDir, 0711))
	must(t, os.Chmod(e.epDir, 0711))
	must(t, os.Mkdir(keys, 0700))
	must(t, os.WriteFile(e.keyPath, e.seed, 0600))
	must(t, os.Chmod(e.keyPath, 0600))
	e.callerDir = filepath.Join(root, "caller")
	must(t, os.Mkdir(e.callerDir, 0700))
	e.dispatchPath, e.cancelPath = filepath.Join(root, "dispatch.json"), filepath.Join(root, "cancel.json")
	var ep, caller syscall.Stat_t
	must(t, syscall.Stat(e.epDir, &ep))
	must(t, syscall.Stat(e.callerDir, &caller))
	serviceGID := uint32(4294967294)
	for groupMember(serviceGID) {
		serviceGID--
	}
	e.doc = map[string]any{
		"v": 1, "caller": map[string]any{"uid": e.policy.Association.UID, "dir": e.callerDir, "dev": uint64(caller.Dev), "ino": caller.Ino},
		"endpoint_dev": uint64(ep.Dev), "endpoint_ino": ep.Ino,
		"service_uid": os.Geteuid(), "service_gid": serviceGID, "connect_gid": os.Getegid(),
		"stage_timeout_ms": 2000, "session_timeout_ms": 5000,
		"key_file": e.keyPath, "state_dir": e.stateDir, "dispatch_plan": e.dispatchPath, "cancel_plan": e.cancelPath,
	}
	// Model only ownership/credentials an unprivileged fixture cannot provision.
	// Opens, inode numbers, permissions, sockets and service peers remain real.
	// Production uses the zero host, with no overrides.
	e.host = host{endpoint: e.epDir, uid: func() int { return int(e.policy.Association.UID) }, euid: func() int { return int(e.policy.Association.UID) }}
	e.host.stat = func(fd int) (syscall.Stat_t, error) {
		st, err := fstat(fd)
		if err != nil {
			return st, err
		}
		name, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		if err != nil {
			return st, err
		}
		switch {
		case name == e.keyPath || name == filepath.Dir(e.keyPath) || name == e.stateDir || strings.HasPrefix(name, e.stateDir+"/") || name == e.callerDir:
			st.Uid = e.policy.Association.UID
		case name == e.epDir:
			st.Gid = serviceGID
		case strings.HasPrefix(name, e.epDir+"/"):
		default:
			st.Uid, st.Gid = 0, 0
		}
		return st, nil
	}
	e.installPlans(randomID(t), randomID(t))
	e.writeConfig()
	return e
}

// requestMembers spells the per-installation members of R from the decoded
// policy, independently of the tool's builder.
func (e *env) requestMembers() map[string]any {
	p := e.policy
	return map[string]any{
		"receiver": p.Receiver,
		"policy":   map[string]any{"id": p.ID, "revision": p.Revision},
		"association": map[string]any{
			"id": p.Association.ID, "revision": p.Association.Revision, "uid": p.Association.UID,
			"generation": p.Association.Generation, "key": p.Association.Key,
			"registration_number": p.Association.RegistrationNumber, "master_epoch": p.Association.MasterEpoch,
		},
		"worker": map[string]any{"uid": p.Worker.UID, "generation": p.Worker.Generation},
		"plan":   map[string]any{"worker_plan": p.Plan.WorkerPlan, "profile": p.Plan.Profile, "harness": p.Plan.Harness, "exec_profile": p.Plan.ExecProfile},
	}
}

func (e *env) writeConfig() {
	e.t.Helper()
	b, err := json.Marshal(e.doc)
	must(e.t, err)
	_ = os.Remove(e.cfgPath)
	must(e.t, os.WriteFile(e.cfgPath, b, 0444))
	must(e.t, os.Chmod(e.cfgPath, 0444))
}

func (e *env) config() *Config {
	e.t.Helper()
	c, err := e.host.loadConfig(e.cfgPath)
	must(e.t, err)
	return c
}

func (e *env) keep(b ...[]byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, x := range b {
		e.outputs = append(e.outputs, append([]byte(nil), x...))
	}
}

// run exercises real command orchestration with a modeled installation.
func (e *env) run(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runWith(args, &stdout, &stderr, e.host.loadConfig)
	e.keep(stdout.Bytes(), stderr.Bytes())
	return code, stdout.String(), stderr.String()
}

func (e *env) installPlans(dispatchID, cancelID string) {
	e.dispatchID, e.cancelID = dispatchID, cancelID
	for name, plan := range map[string]*m0.SigningPlan{e.dispatchPath: e.plan(m0.OpDispatch, dispatchID, ""), e.cancelPath: e.plan(m0.OpCancel, cancelID, dispatchID)} {
		_ = os.Remove(name)
		must(e.t, os.WriteFile(name, plan.Canonical, 0600))
	}
}

func (e *env) dispatch(id string) (int, string, string) {
	// IDs select operator-installed test plans, never a caller random source.
	if id != e.dispatchID {
		e.installPlans(id, e.cancelID)
	}
	return e.run("dispatch", "--config", e.cfgPath)
}
func (e *env) cancel(id, target string) (int, string, string) {
	if id != e.cancelID || target != e.dispatchID {
		e.installPlans(target, id)
	}
	return e.run("cancel", "--config", e.cfgPath)
}
func (e *env) resend(id string) (int, string, string) {
	return e.run("resend", "--config", e.cfgPath, "--request-id", id)
}

func (e *env) sign(c *Config, op m0.Op, id, target string) ([]byte, string, error) {
	return SignRequest(c, e.plan(op, id, target))
}

func (e *env) storedPath(id string) string { return filepath.Join(e.stateDir, id+".req") }

func (e *env) storedCount() int {
	e.t.Helper()
	entries, err := os.ReadDir(e.stateDir)
	must(e.t, err)
	return len(entries)
}

// seedForms are the spellings the secrecy scan looks for: the whole seed raw,
// in hex and in the four base64 alphabets, and each half raw and in hex.
func seedForms(seed []byte) map[string][]byte {
	forms := map[string][]byte{
		"raw":           seed,
		"hex":           []byte(hex.EncodeToString(seed)),
		"HEX":           []byte(strings.ToUpper(hex.EncodeToString(seed))),
		"base64":        []byte(base64.StdEncoding.EncodeToString(seed)),
		"base64 raw":    []byte(base64.RawStdEncoding.EncodeToString(seed)),
		"base64url":     []byte(base64.URLEncoding.EncodeToString(seed)),
		"base64url raw": []byte(base64.RawURLEncoding.EncodeToString(seed)),
	}
	for name, half := range map[string][]byte{"first half": seed[:len(seed)/2], "second half": seed[len(seed)/2:]} {
		forms[name+" raw"] = half
		forms[name+" hex"] = []byte(hex.EncodeToString(half))
	}
	return forms
}

// findSeed returns the names of every seed form present in any blob.
func findSeed(seed []byte, blobs ...[]byte) []string {
	var found []string
	for name, form := range seedForms(seed) {
		for _, b := range blobs {
			if bytes.Contains(b, form) {
				found = append(found, name)
				break
			}
		}
	}
	return found
}

// scan checks every captured stdout and stderr, the configuration and every
// file of the state directory for the seed.
func (e *env) scan() {
	e.mu.Lock()
	blobs := append([][]byte(nil), e.outputs...)
	e.mu.Unlock()
	for _, name := range []string{e.cfgPath, e.dispatchPath, e.cancelPath} {
		if b, err := os.ReadFile(name); err == nil {
			blobs = append(blobs, b)
		}
	}
	_ = os.Chmod(e.stateDir, 0700)
	entries, _ := os.ReadDir(e.stateDir)
	for _, entry := range entries {
		if b, err := os.ReadFile(filepath.Join(e.stateDir, entry.Name())); err == nil {
			blobs = append(blobs, b)
		}
	}
	if found := findSeed(e.seed, blobs...); len(found) != 0 {
		e.t.Errorf("seed material found in outputs or state: %v", found)
	}
}

const probeMarker = "PROBE-NOT-A-FRAME"

var helloPattern = regexp.MustCompile(`^\{"request_id":"([0-9a-f]{32})","versions":\[1\]\}$`)

// listener is a test stand-in for the daemon's connection loop on a real
// Unix socket. It records every byte each connection sends. serve, when set,
// receives the request frame exactly as the daemon's extension would.
type listener struct {
	t        *testing.T
	e        *env
	ln       *net.UnixListener
	path     string
	mode     string
	serve    func(id string, body []byte) (string, error)
	mu       sync.Mutex
	accepted int
	finished int
	probes   int
	streams  [][]byte
	done     chan struct{}
}

const (
	modeAccept   = "accept"
	modeServe    = "serve"
	modeClose    = "close"
	modeTruncate = "truncate"
	modeWrongID  = "wrong-id"
	modeSilent   = "silent"
	modeRights   = "rights"
	modeBadHello = "bad-hello"
)

func refusalEnvelope(id string) string {
	return `{"error":{"code":"request_invalid","message":"unsupported request"},"ok":false,"request_id":"` + id + `","v":1}`
}

func acceptedEnvelope(id string) string {
	return `{"ok":true,"request_id":"` + id + `","result":{"effect_id":"fixture","state":"started"},"v":1}`
}

func (e *env) listen(mode string) *listener { return e.listenWith(mode, nil) }

func (e *env) listenWith(mode string, serve func(id string, body []byte) (string, error)) *listener {
	e.t.Helper()
	dir, err := os.Open(e.epDir)
	must(e.t, err)
	defer dir.Close()
	// Bind through the directory descriptor: fixture paths can exceed the
	// socket address length.
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: fmt.Sprintf("/proc/self/fd/%d/dispatcher.sock", dir.Fd())})
	must(e.t, err)
	ln.SetUnlinkOnClose(false)
	l := &listener{t: e.t, e: e, ln: ln, path: filepath.Join(e.epDir, "dispatcher.sock"), mode: mode, serve: serve, done: make(chan struct{})}
	must(e.t, os.Chmod(l.path, 0660))
	go func() {
		defer close(l.done)
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			l.mu.Lock()
			l.accepted++
			l.mu.Unlock()
			go l.handle(c)
		}
	}()
	e.t.Cleanup(l.close)
	return l
}

func (l *listener) close() {
	_ = l.ln.Close()
	<-l.done
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		idle := l.accepted == l.finished
		l.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(time.Millisecond)
	}
	_ = os.Remove(l.path)
}

type recorder struct {
	c   *net.UnixConn
	got []byte
}

func (r *recorder) readFrame() ([]byte, error) {
	var prefix [4]byte
	n, err := io.ReadFull(r.c, prefix[:])
	r.got = append(r.got, prefix[:n]...)
	if err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > 131072 {
		rest, _ := io.ReadAll(io.LimitReader(r.c, 4096))
		r.got = append(r.got, rest...)
		return nil, fmt.Errorf("frame length %d", size)
	}
	body := make([]byte, size)
	n, err = io.ReadFull(r.c, body)
	r.got = append(r.got, body[:n]...)
	return body, err
}

func frameOf(payload string) []byte {
	b := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(payload)))
	copy(b[4:], payload)
	return b
}

func (l *listener) handle(c *net.UnixConn) {
	r := &recorder{c: c}
	defer func() {
		_ = c.Close()
		l.mu.Lock()
		l.streams = append(l.streams, r.got)
		l.finished++
		l.mu.Unlock()
	}()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	// The kernel peer of every fixture connection is this process.
	if raw, err := c.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
			if err != nil || cred.Uid != uint32(os.Geteuid()) {
				l.t.Errorf("fixture peer credentials: %v %v", cred, err)
			}
		})
	}
	hello, err := r.readFrame()
	if err != nil {
		return
	}
	m := helloPattern.FindSubmatch(hello)
	if m == nil {
		return
	}
	id := string(m[1])
	if l.mode == modeBadHello {
		// A well-formed refusal envelope is still not the hello result.
		_, _ = c.Write(frameOf(refusalEnvelope(id)))
		rest, _ := io.ReadAll(io.LimitReader(c, 4096))
		r.got = append(r.got, rest...)
		return
	}
	if _, err := c.Write(frameOf(helloResult(id))); err != nil {
		return
	}
	body, err := r.readFrame()
	if err != nil {
		return
	}
	switch l.mode {
	case modeAccept:
		_, _ = c.Write(frameOf(acceptedEnvelope(id)))
	case modeServe:
		reply, err := l.serve(id, body)
		if err != nil {
			return
		}
		_, _ = c.Write(frameOf(reply))
	case modeClose:
	case modeTruncate:
		_, _ = c.Write(frameOf(acceptedEnvelope(id))[:20])
	case modeWrongID:
		_, _ = c.Write(frameOf(acceptedEnvelope(strings.Repeat("f", 32))))
	case modeSilent:
		rest, _ := io.ReadAll(io.LimitReader(c, 4096))
		r.got = append(r.got, rest...)
	case modeRights:
		null, err := os.Open(os.DevNull)
		if err != nil {
			return
		}
		defer null.Close()
		_, _, _ = c.WriteMsgUnix(frameOf(acceptedEnvelope(id)), syscall.UnixRights(int(null.Fd())), nil)
	}
}

// drain returns the byte stream of every tool connection so far, in
// completion order. A marker connection made after the tool returned is
// accepted after every earlier connection, so once it has been recorded
// nothing from the tool can still be pending.
func (l *listener) drain() [][]byte {
	l.t.Helper()
	c, err := net.Dial("unix", l.path)
	must(l.t, err)
	_, err = c.Write([]byte(probeMarker))
	must(l.t, err)
	must(l.t, c.Close())
	l.mu.Lock()
	l.probes++
	l.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l.mu.Lock()
		var out [][]byte
		seen := 0
		for _, s := range l.streams {
			if string(s) == probeMarker {
				seen++
			} else {
				out = append(out, s)
			}
		}
		ready := seen == l.probes && l.accepted == l.finished
		l.mu.Unlock()
		if ready {
			l.e.keep(out...)
			return out
		}
		if time.Now().After(deadline) {
			l.t.Fatal("listener did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

// requestFrame returns the payload of the second frame of one recorded
// stream: the request as the listener received it.
func requestFrame(t *testing.T, stream []byte) []byte {
	t.Helper()
	for i := 0; i < 2; i++ {
		if len(stream) < 4 {
			t.Fatalf("stream has no frame %d", i+1)
		}
		size := int(binary.BigEndian.Uint32(stream))
		if len(stream) < 4+size {
			t.Fatalf("stream frame %d truncated", i+1)
		}
		if i == 1 {
			return stream[4 : 4+size]
		}
		stream = stream[4+size:]
	}
	return nil
}

type policySource struct {
	mu   sync.Mutex
	inst *m0.Installed
}

func (s *policySource) Current() (*m0.Installed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *s.inst
	return &cp, nil
}

func (s *policySource) edit(fn func(*m0.Installed)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *s.inst
	fn(&cp)
	s.inst = &cp
}

type fixedClock struct{ now int64 }

func (c fixedClock) Now() (m0.Reading, error) { return m0.Reading{Unix: c.now, Confident: true}, nil }

type generations map[uint32]m0.Generation

func (g generations) Current(uid uint32) (m0.Generation, error) { return g[uid], nil }

type composer struct {
	input         m0.Input
	catalog       string
	qualification string
}

func (c composer) Compose(r *m0.Request, exec m0.Execution, timing m0.Timing) (*m0.Entry, error) {
	return &m0.Entry{Request: r, Execution: exec, Catalog: c.catalog, Qualification: c.qualification, Timing: timing, Input: c.input}, nil
}

// launcher is the test launcher seam: it counts physical starts and commits
// through the daemon's gate with the facts a verified receiver would present.
type launcher struct {
	mu       sync.Mutex
	receiver string
	now      int64
	starts   int
	stops    int
}

func (l *launcher) Start(_ context.Context, req m0.StartRequest, gate m0.CommitGate) (m0.StartReceipt, error) {
	l.mu.Lock()
	l.starts++
	l.mu.Unlock()
	facts := m0.ReceiverFacts{DropVerified: true, Receiver: l.receiver, WorkerUID: req.Worker.UID, Generation: req.Worker.Generation, ControllerEpoch: req.ControllerEpoch, ExecutionID: req.ExecutionID, EffectID: req.EffectID}
	if err := gate.Commit(facts); err != nil {
		return m0.StartReceipt{}, nil
	}
	return m0.StartReceipt{Committed: true, CommitUnix: l.now, ExecUnix: l.now, Receipt: []byte("receipt")}, nil
}

func (l *launcher) Stop(context.Context, m0.StopRequest) (m0.StopReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stops++
	return m0.StopReceipt{Absent: true, Evidence: []byte("fenced account-wide absence")}, nil
}

func (l *launcher) Observe(context.Context, m0.EffectQuery) (m0.Observation, error) {
	return m0.ObservationUnknown, nil
}

func (l *launcher) count() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts, l.stops
}

// backend is the landed daemon code behind the listener: a real journal, the
// real runtime and extension, and test seams for every unpublished dependency.
type backend struct {
	rt       *m0.Runtime
	ext      *m0.Extension
	source   *policySource
	launcher *launcher
}

// plan spells one protected signing plan for an exact request ID, built from
// the policy and not from the tool's builder.
func (e *env) plan(op m0.Op, id, target string) *m0.SigningPlan {
	e.t.Helper()
	request := e.requestMembers()
	request["v"], request["kind"], request["request_id"], request["op"] = 1, m0.RequestKind, id, string(op)
	if target != "" {
		request["dispatch_request_id"] = target
	}
	b, err := json.Marshal(map[string]any{"v": 1, "kind": m0.SigningPlanKind, "profile": m0.SigningProfile, "request": request})
	must(e.t, err)
	sp, err := m0.DecodeSigningPlan(b)
	must(e.t, err)
	return sp
}

func (e *env) catalog(dispatchID, cancelID string) m0.Catalog {
	return m0.Catalog{Dispatch: e.plan(m0.OpDispatch, dispatchID, ""), Cancel: e.plan(m0.OpCancel, cancelID, dispatchID)}
}

// newBackend opens a fresh ledger whose protected catalog pins exactly the
// given dispatch and cancel request IDs.
func (e *env) newBackend(dispatchID, cancelID string) *backend {
	e.t.Helper()
	dir := filepath.Join(e.root, "ledger")
	must(e.t, os.Mkdir(dir, 0700))
	must(e.t, os.Chmod(dir, 0700))
	for _, n := range []string{"records", "inputs"} {
		must(e.t, os.Mkdir(filepath.Join(dir, n), 0700))
		must(e.t, os.Chmod(filepath.Join(dir, n), 0700))
	}
	var fs syscall.Statfs_t
	must(e.t, syscall.Statfs(dir, &fs))
	if fs.Type != 0xEF53 && fs.Type != 0x58465342 {
		e.t.Skipf("daemon ledger UNRUN: fixture filesystem type %#x is neither ext4 nor XFS", fs.Type)
	}
	current := syscall.Umask(0)
	syscall.Umask(current)
	mask := uint32(current)
	if mask&0600 != 0 {
		e.t.Skipf("process umask %#o masks owner read/write", mask)
	}
	p := e.policy
	now := p.NotBefore + 10
	b := &backend{
		source:   &policySource{inst: &m0.Installed{Policy: p, Catalog: e.catalog(dispatchID, cancelID), Qualified: m0.Qualified{HandoffMs: 5000, ResidualMs: 5000, SlackMs: 10000, IndependentStop: true}, InstallRevision: 1}},
		launcher: &launcher{receiver: p.Receiver, now: now},
	}
	seams := m0.Seams{
		Policy:     b.source,
		Launcher:   b.launcher,
		Runner:     m0.CodexRunner{Executable: "/example/protected/swarma-credential-broker", Home: "/example/station"},
		Clock:      fixedClock{now: now},
		Generation: generations{p.Association.UID: {Generation: p.Association.Generation, Active: true}, p.Worker.UID: {Generation: p.Worker.Generation, Active: true}},
		Entry:      composer{input: e.input, catalog: p.Catalog, qualification: p.Qualification},
	}
	rt, err := m0.Initialize(journal.Config{Directory: dir, Records: "records", Inputs: "inputs", DeclaredUmask: &mask, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 4 << 20}, seams)
	must(e.t, err)
	b.rt = rt
	b.ext = m0.NewExtension(rt)
	e.t.Cleanup(func() { _ = b.ext.Close() })
	return b
}

// serveBackend hands frames to the real extension using the modeled managed
// caller UID. This is not the transport package's serve loop; only the client
// service-peer check uses real SO_PEERCRED in this single-UID fixture.
func (e *env) serveBackend(b *backend) *listener {
	return e.listenWith(modeServe, func(id string, body []byte) (string, error) {
		return b.ext.Serve(e.policy.Association.UID, id, body)
	})
}
