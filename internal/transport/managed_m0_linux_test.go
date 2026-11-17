//go:build linux

package transport_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

const requestID = "00000000000000000000000000000001"
const rootSkip = "real peer control requires root to set a second kernel UID with syscall.Credential"

type installedPolicy struct{ installed *m0.Installed }

func (s installedPolicy) Current() (*m0.Installed, error) { return s.installed, nil }

type fixedClock struct{ now int64 }

func (c fixedClock) Now() (m0.Reading, error) { return m0.Reading{Unix: c.now, Confident: true}, nil }

type currentGenerations struct {
	policy *m0.Policy
	stale  bool
	calls  atomic.Int32
}

func (g *currentGenerations) Current(uid uint32) (m0.Generation, error) {
	g.calls.Add(1)
	if uid == g.policy.Association.UID {
		gen := g.policy.Association.Generation
		if g.stale {
			gen = strings.Repeat("f", 32)
		}
		return m0.Generation{Generation: gen, Active: true}, nil
	}
	return m0.Generation{Generation: g.policy.Worker.Generation, Active: true}, nil
}

type entryComposer struct {
	input  m0.Input
	policy *m0.Policy
}

func (c entryComposer) Compose(r *m0.Request, e m0.Execution, timing m0.Timing) (*m0.Entry, error) {
	return &m0.Entry{Request: r, Execution: e, Timing: timing, Input: c.input, Catalog: c.policy.Catalog, Qualification: c.policy.Qualification}, nil
}

type boundedLauncher struct {
	policy *m0.Policy
	now    int64
	starts atomic.Int32
}

func (l *boundedLauncher) Start(_ context.Context, r m0.StartRequest, gate m0.CommitGate) (m0.StartReceipt, error) {
	l.starts.Add(1)
	err := gate.Commit(m0.ReceiverFacts{DropVerified: true, Receiver: l.policy.Receiver, WorkerUID: r.Worker.UID, Generation: r.Worker.Generation, ControllerEpoch: r.ControllerEpoch, ExecutionID: r.ExecutionID, EffectID: r.EffectID})
	if err != nil {
		return m0.StartReceipt{}, err
	}
	return m0.StartReceipt{Committed: true, CommitUnix: l.now, ExecUnix: l.now, Receipt: []byte("test receipt")}, nil
}
func (*boundedLauncher) Stop(context.Context, m0.StopRequest) (m0.StopReceipt, error) {
	return m0.StopReceipt{Absent: true, Evidence: []byte("test absence")}, nil
}
func (*boundedLauncher) Observe(context.Context, m0.EffectQuery) (m0.Observation, error) {
	return m0.ObservationUnknown, nil
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%v; cause: %v", err, errors.Unwrap(err))
	}
}
func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	check(t, err)
	return b
}
func document(t *testing.T, dir, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "fixtures", dir, name))
	check(t, err)
	var v map[string]any
	check(t, json.Unmarshal(b, &v))
	return v
}

// attachedM0 uses a real journal and test-only, explicitly pinned dependency seams.
// The generated private key stays only in memory and is never returned or logged.
func attachedM0(t *testing.T, real, stale bool) (*m0.Extension, []byte, *currentGenerations, *boundedLauncher) {
	t.Helper()
	pin := transport.ManagedPrincipalFixture(t, 30001, real)
	specimen := document(t, "m0-executor-entry", "entry.specimen.json")["input"].(map[string]any)
	input := m0.Input{Account: specimen["account"].(string), Profile: specimen["profile"].(string), Model: specimen["model"].(string), ReasoningEffort: specimen["reasoning_effort"].(string), Prompt: specimen["prompt"].(string)}
	_, planDigest, err := input.Canonical()
	check(t, err)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	check(t, err)
	defer clear(private)
	key := "ed25519:" + base64.RawURLEncoding.EncodeToString(pub)
	rekey := func(v map[string]any) {
		v["association"].(map[string]any)["key"] = key
		v["plan"].(map[string]any)["worker_plan"] = planDigest
	}
	pv := document(t, "m0-launch", "policy.valid.json")
	rekey(pv)
	policy, err := m0.DecodePolicy(encode(t, pv))
	check(t, err)
	dispatch := document(t, "m0-launch", "dispatch.valid.json")
	delete(dispatch, "auth")
	rekey(dispatch)
	cancel := document(t, "m0-launch", "cancel.valid.json")
	delete(cancel, "auth")
	rekey(cancel)
	plan := func(v map[string]any) *m0.SigningPlan {
		sp, err := m0.DecodeSigningPlan(encode(t, map[string]any{"kind": m0.SigningPlanKind, "profile": m0.SigningProfile, "request": v, "v": 1}))
		check(t, err)
		return sp
	}
	inst := &m0.Installed{Policy: policy, Catalog: m0.Catalog{Dispatch: plan(dispatch), Cancel: plan(cancel)}, Qualified: m0.Qualified{HandoffMs: 5000, ResidualMs: 5000, SlackMs: 10000, IndependentStop: true}, InstallRevision: 1}
	preimage := append([]byte("swarma-dispatcher request/1\n"), encode(t, dispatch)...)
	sig := ed25519.Sign(private, preimage)
	dispatch["auth"] = map[string]any{"key": key, "sig": base64.RawURLEncoding.EncodeToString(sig)}
	body := encode(t, dispatch)
	dir := t.TempDir()
	check(t, os.Chmod(dir, 0700))
	for _, name := range []string{"records", "inputs"} {
		check(t, os.Mkdir(filepath.Join(dir, name), 0700))
	}
	var fs syscall.Statfs_t
	check(t, syscall.Statfs(dir, &fs))
	if fs.Type != 0xEF53 && fs.Type != 0x58465342 {
		t.Fatal("M0 socket control requires ext4 or XFS")
	}
	p17 := map[string]any{"bind_timeout_ms": 1000, "keymaker_gid": 30004, "keymaker_mode": 432, "keymaker_socket_dir": "/run/keymaker", "keymaker_socket_name": "request.sock", "keymaker_uid": 30003, "source_control_dir": "/run/source", "source_generation": "11111111111111111111111111111111", "source_socket_name": "origin.sock", "source_uid": 30002, "state_dir": filepath.Join(dir, "..", "p17"), "station": "1"}
	cfg := encode(t, map[string]any{"callers": []transport.Principal{pin}, "enabled": true, "entry": "initialize", "kind": m0.ConfigKind, "p17": p17, "recovery_attested": true, "state_dir": dir, "supported_local_storage": true, "v": 3})
	gens := &currentGenerations{policy: policy, stale: stale}
	now := policy.NotBefore + 10
	launcher := &boundedLauncher{policy: policy, now: now}
	seams := m0.Seams{Policy: installedPolicy{inst}, Clock: fixedClock{now}, Generation: gens, Launcher: launcher, Entry: entryComposer{input, policy}, Runner: m0.CodexRunner{Executable: "/example/protected/swarma-credential-broker", Home: "/example/station"}}
	previous := syscall.Umask(0177)
	defer syscall.Umask(previous)
	ext, err := m0.Attach("/example/m0.json", seams, func(string) ([]byte, error) { return cfg, nil })
	check(t, err)
	t.Cleanup(func() { check(t, ext.Close()) })
	return ext, body, gens, launcher
}

func sendFrame(c net.Conn, body []byte) error {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
	_, err := io.Copy(c, bytes.NewReader(append(prefix[:], body...)))
	return err
}
func readFrame(c net.Conn) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(c, prefix[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 || n > 508 {
		return nil, errors.New("invalid result length")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(c, b)
	return b, err
}
func exchange(path string, body []byte, admitted bool) ([]byte, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		return nil, err
	}
	if !admitted {
		// Send a valid hello so an accidentally admitted peer cannot look like
		// the base refusal merely by waiting for the server's read timeout.
		if err := sendFrame(c, []byte(`{"request_id":"`+requestID+`","versions":[1]}`)); err != nil && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
			return nil, err
		}
		var one [1]byte
		n, err := c.Read(one[:])
		if n == 0 && (errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET)) {
			return nil, nil
		}
		return nil, errors.New("unlisted managed peer did not receive the base refusal")
	}
	if err := sendFrame(c, []byte(`{"request_id":"`+requestID+`","versions":[1]}`)); err != nil {
		return nil, err
	}
	hello, err := readFrame(c)
	if err != nil {
		return nil, errors.New("hello failed: " + err.Error())
	}
	if string(hello) != `{"ok":true,"request_id":"`+requestID+`","result":{"version":1},"v":1}` {
		return nil, errors.New("unexpected hello")
	}
	if err := sendFrame(c, body); err != nil {
		return nil, err
	}
	return readFrame(c)
}

// Claim: listed socket callers reach M0 generationCurrent, which refuses a stale generation; the current generation starts once, and unmanaged M0 requests still fail M0's association-UID check.
func TestManagedM0Generation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		peer  uint32
		stale bool
		want  string
	}{
		{"current", 30001, false, `"state":"started"`},
		{"stale", 30001, true, `"code":"m0_generation_mismatch"`},
		{"unmanaged", 1002, false, `kernel peer is not the association UID`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext, body, gens, launcher := attachedM0(t, false, tc.stale)
			path, _ := transport.ManagedSocketFixture(t, ext, tc.peer)
			reply, err := exchange(path, body, true)
			check(t, err)
			if !bytes.Contains(reply, []byte(tc.want)) {
				t.Fatalf("wrong M0 outcome: %s", reply)
			}
			if tc.name == "current" {
				if launcher.starts.Load() != 1 {
					t.Fatal("no start")
				}
			} else if launcher.starts.Load() != 0 {
				t.Fatal("refused caller started")
			}
			if tc.stale && gens.calls.Load() == 0 {
				t.Fatal("refused before M0 generationCurrent")
			}
		})
	}
}

// Claim: a second REAL kernel UID through the REAL socket is admitted only when listed and only to M0 frames; an unlisted managed kernel UID receives the base zero-byte close.
func TestManagedM0RealPeer(t *testing.T) {
	if mode := os.Getenv("DISPATCHER_PEER_CHILD"); mode != "" {
		body, err := io.ReadAll(io.LimitReader(os.Stdin, 131073))
		check(t, err)
		reply, err := exchange("/proc/self/fd/3/dispatcher.sock", body, mode != "unlisted")
		if mode == "reference" {
			if reply != nil || !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
				t.Fatal("managed peer received a reference-subset result")
			}
		} else {
			check(t, err)
			if mode == "listed" && !bytes.Contains(reply, []byte(`"state":"started"`)) {
				t.Fatal("managed peer did not receive an M0 result")
			}
		}
		return
	}
	if os.Geteuid() != 0 {
		if os.Getenv("DISPATCHER_REQUIRE_ROOT") == "1" {
			t.Fatal(rootSkip)
		}
		t.Skip(rootSkip)
	}
	ext, body, _, launcher := attachedM0(t, true, false)
	_, socketDir := transport.ManagedSocketFixture(t, ext, 0)
	executable, err := os.Executable()
	check(t, err)
	binaryFile, err := os.Open(executable)
	check(t, err)
	defer binaryFile.Close()
	for _, tc := range []struct {
		mode string
		uid  uint32
		body []byte
	}{
		{"listed", 30001, body},
		{"reference", 30001, []byte(`{"op":"capacity","request_id":"` + requestID + `"}`)},
		{"reference", 30001, []byte(`{"op":"start","request_id":"` + requestID + `"}`)},
		{"unlisted", 30003, body},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "/proc/self/fd/4", "-test.run=^TestManagedM0RealPeer$", "-test.v")
		cmd.ExtraFiles = []*os.File{socketDir, binaryFile}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: tc.uid, Gid: uint32(os.Getegid()), Groups: []uint32{}}}
		cmd.Env = []string{"DISPATCHER_PEER_CHILD=" + tc.mode, "GORACE=atexit_sleep_ms=0"}
		cmd.Stdin = bytes.NewReader(tc.body)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s child: %v\n%s", tc.mode, err, output)
		}
	}
	if launcher.starts.Load() != 1 {
		t.Fatal("expected exactly one M0 launch")
	}
}
