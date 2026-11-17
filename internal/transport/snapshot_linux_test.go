//go:build linux

package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/compose/digest"
)

// These role translations are fixtures only: the real parser, immutable token,
// publication/serve/close lifecycle and caller selection are exercised, not a
// claim of root-owned configuration or distinct real peer qualification.
type snapshotFixture struct {
	path                 string
	a, b                 []byte
	endpoint             *endpoint
	callerUID            uint32
	reads, endpointCalls int
	loadCalls            serverLoadCalls
}

func snapshotControl(t *testing.T) *snapshotFixture {
	t.Helper()
	parent := protectedFixture(t, 0700)
	caller := protectedFixture(t, 0700)
	e, _ := fixtureEndpoint(t)
	f := &snapshotFixture{path: filepath.Join(parent.path, "server.json"), endpoint: e, callerUID: 1002}
	if f.callerUID == uint32(os.Geteuid()) {
		f.callerUID = 1003
	}
	id := caller.ids[len(caller.ids)-1]
	ep := e.dir.ids[len(e.dir.ids)-1]
	c := serverConfig{commonConfig: commonConfig{Version: 1, ConnectGID: uint32(os.Getegid()) + 1, ServiceGID: uint32(os.Getegid()), ServiceUID: uint32(os.Geteuid()), EndpointDev: ep.dev, EndpointIno: ep.ino}, Callers: []principal{{Dev: id.dev, Ino: id.ino, Dir: caller.path, UID: f.callerUID}}}
	canonical := func(c serverConfig) []byte {
		b, err := json.Marshal(map[string]any{"callers": c.Callers, "connect_gid": c.ConnectGID, "endpoint_dev": c.EndpointDev, "endpoint_ino": c.EndpointIno, "service_gid": c.ServiceGID, "service_uid": c.ServiceUID, "v": c.Version})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	f.a = canonical(c)
	c.Callers[0].UID++
	f.b = canonical(c)
	if _, err := parseServer(f.a); err != nil {
		t.Fatal(err)
	}
	if _, err := parseServer(f.b); err != nil {
		t.Fatal(err)
	}
	writeSnapshotFile(t, f.path, f.a)
	f.loadCalls = serverLoadCalls{
		read: func(path string) ([]byte, error) {
			f.reads++
			fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			if err != nil {
				return nil, err
			}
			file := os.NewFile(uintptr(fd), "server-fixture")
			defer file.Close()
			st, err := file.Stat()
			if err != nil {
				return nil, err
			}
			sys, ok := st.Sys().(*syscall.Stat_t)
			if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != 0440 || sys.Uid != uint32(os.Geteuid()) || sys.Gid != uint32(os.Getegid()) || sys.Nlink != 1 || st.Size() > serverConfigLimit {
				return nil, fmt.Errorf("fixture protected metadata: mode=%o uid=%d gid=%d links=%d size=%d: %w", st.Mode(), sys.Uid, sys.Gid, sys.Nlink, st.Size(), ErrConfig)
			}
			body, err := io.ReadAll(io.LimitReader(file, serverConfigLimit+1))
			if err != nil {
				return nil, err
			}
			named, err := os.Lstat(path)
			if err != nil || !os.SameFile(st, named) || parent.recheck() != nil {
				return nil, ErrIdentity
			}
			return body, nil
		},
		runtime: func(common commonConfig, uid uint32, server bool) error {
			if !server || uid != uint32(os.Geteuid()) || common.ServiceUID != uid {
				return ErrConfig
			}
			return nil
		},
		pin: func(p principal) (*directory, error) { p.UID = uint32(os.Geteuid()); return principalPin(p) },
	}
	return f
}
func writeSnapshotFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0440); err != nil {
		t.Fatal(err)
	}
}
func (f *snapshotFixture) load(t *testing.T) *ServerSnapshot {
	t.Helper()
	s, err := loadServer(f.path, f.loadCalls)
	if err != nil {
		t.Fatalf("fixture LoadServer: %v", err)
	}
	return s
}
func (f *snapshotFixture) calls(started chan *shutdown) daemonCalls {
	return daemonCalls{
		pin: func(commonConfig) (*directory, error) { f.endpointCalls++; return f.endpoint.dir, nil },
		publish: func(_ *directory, _ commonConfig, _ publicationCalls, stop *shutdown) (*endpoint, error) {
			f.endpointCalls++
			started <- stop
			return f.endpoint, nil
		},
		admission: admissionCalls{peer: func(*os.File) (uint32, error) { return f.callerUID, nil }, pin: f.loadCalls.pin},
	}
}
func snapshotExchange(t *testing.T, path string) (string, error) {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return "", err
	}
	defer conn.Close()
	file, err := conn.File()
	if err != nil {
		return "", err
	}
	defer file.Close()
	return capacityExchange(file, time.Now(), "00000000000000000000000000000001")
}
func snapshotStarted(t *testing.T, started <-chan *shutdown, done <-chan error) *shutdown {
	t.Helper()
	select {
	case stop := <-started:
		return stop
	case err := <-done:
		t.Fatalf("snapshot failed before publication: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot never published")
	}
	return nil
}
func snapshotWait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot daemon did not exit")
		return ErrShutdown
	}
}

// Claim: a protected A-to-B replacement between the command's digest read and daemon consumption cannot change the listener's actual caller table; the startup digest binds A, and a later standalone read correctly sees B.
func TestSnapshotReplacementRace(t *testing.T) {
	f := snapshotControl(t)
	snapshot := f.load(t)
	writeSnapshotFile(t, f.path+".new", f.b)
	var stderr bytes.Buffer
	started := make(chan *shutdown, 1)
	done := make(chan error, 1)
	go func() {
		done <- digest.AnnounceAndServe(snapshot.Bytes(), nil, &stderr, func() error {
			// Both files are valid/protected; only this fixture seam replaces A.
			if err := os.Rename(f.path+".new", f.path); err != nil {
				return err
			}
			return daemonFromSnapshot(snapshot, nil, f.calls(started))
		})
	}()
	stop := snapshotStarted(t, started, done)
	response, err := snapshotExchange(t, filepath.Join(f.endpoint.dir.path, endpointName))
	if err != nil {
		stop.request(nil)
		_ = snapshotWait(t, done)
		t.Fatal(err)
	}
	if !strings.Contains(response, `"code":"capacity:hold"`) {
		t.Errorf("listener did not use A's caller table: %s", response)
	}
	stop.request(nil)
	if err := snapshotWait(t, done); err != nil {
		t.Fatal(err)
	}
	inner := sha256.Sum256(f.a)
	canonical, _ := json.Marshal(map[string]string{"server": hex.EncodeToString(inner[:])})
	outer := sha256.Sum256(canonical)
	if stderr.String() != "config-digest "+hex.EncodeToString(outer[:])+"\n" || f.reads != 1 {
		t.Fatal("startup line did not bind exactly the consumed read")
	}
	later := f.load(t)
	if !bytes.Equal(later.Bytes(), f.b) || digest.ConfigDigest(later.Bytes(), nil) == digest.ConfigDigest(f.a, nil) {
		t.Fatal("non-authoritative later read hid protected replacement")
	}
}

// Claim: nil/zero/consumed snapshots refuse ErrConfig before endpoint work beside a valid socket start, byte accessors are copies, and copying the opaque value cannot duplicate its consumption token.
func TestSnapshotConsumption(t *testing.T) {
	f := snapshotControl(t)
	snapshot := f.load(t)
	duplicate := *snapshot
	got := snapshot.Bytes()
	got[0] = 'x'
	if !bytes.Equal(snapshot.Bytes(), f.a) {
		t.Fatal("Bytes aliases parsed bytes")
	}
	var nilSnapshot *ServerSnapshot
	if nilSnapshot.Bytes() != nil {
		t.Fatal("nil Bytes")
	}
	for _, bad := range []*ServerSnapshot{nil, {}} {
		if err := daemonFromSnapshot(bad, nil, f.calls(make(chan *shutdown, 1))); !errors.Is(err, ErrConfig) || f.endpointCalls != 0 {
			t.Fatal("invalid snapshot reached endpoint work")
		}
		if err := DaemonFromSnapshot(bad, nil); !errors.Is(err, ErrConfig) {
			t.Fatal("public invalid snapshot did not refuse ErrConfig")
		}
	}
	started := make(chan *shutdown, 1)
	done := make(chan error, 1)
	go func() { done <- daemonFromSnapshot(snapshot, nil, f.calls(started)) }()
	stop := snapshotStarted(t, started, done)
	_, err := snapshotExchange(t, filepath.Join(f.endpoint.dir.path, endpointName))
	stop.request(nil)
	result := snapshotWait(t, done)
	if err != nil || result != nil {
		t.Fatalf("valid start: %v %v", err, result)
	}
	count := f.endpointCalls
	for _, bad := range []*ServerSnapshot{snapshot, &duplicate} {
		if err := daemonFromSnapshot(bad, nil, f.calls(make(chan *shutdown, 1))); !errors.Is(err, ErrConfig) || f.endpointCalls != count {
			t.Fatal("consumed snapshot reached endpoint work")
		}
		if err := DaemonFromSnapshot(bad, nil); !errors.Is(err, ErrConfig) {
			t.Fatal("public consumed snapshot did not refuse")
		}
	}
	if !bytes.Equal(snapshot.Bytes(), f.a) {
		t.Fatal("consumption changed snapshot bytes")
	}
}

type snapshotExtension struct {
	fatal bool
	calls atomic.Int32
}

func (*snapshotExtension) ManagedCallers() []Principal { return nil }
func (e *snapshotExtension) Serve(_ uint32, id string, _ []byte) (string, error) {
	e.calls.Add(1)
	if e.fatal {
		return "", errors.Join(ErrFatal, syscall.EIO)
	}
	return `{"error":{"code":"observation_unknown"},"ok":false,"request_id":"` + id + `","v":1}`, nil
}
func snapshotExtensionExchange(t *testing.T, path string) (string, error) {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return "", err
	}
	defer conn.Close()
	file, err := conn.File()
	if err != nil {
		return "", err
	}
	defer file.Close()
	wire, err := newSocketIO(file, time.Now())
	if err != nil {
		return "", err
	}
	if err := wire.writeFrame(hello("00000000000000000000000000000001")); err != nil {
		return "", err
	}
	if _, err := wire.readFrame(); err != nil {
		return "", err
	}
	if err := wire.writeFrame(`{"kind":"swarma-dispatcher m0-request/1","op":"dispatch","request_id":"00000000000000000000000000000001"}`); err != nil {
		return "", err
	}
	body, err := wire.readFrame()
	return string(body), err
}

// Claim: the daemon's real serve loop propagates extension fatal duty without a response or later request, while ordinary unknown outcomes beside it continue to serve.
func TestSnapshotFatalServeLoop(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		t.Run(fmt.Sprintf("fatal-%t", fatal), func(t *testing.T) {
			f := snapshotControl(t)
			s := f.load(t)
			ext := &snapshotExtension{fatal: fatal}
			started := make(chan *shutdown, 1)
			done := make(chan error, 1)
			go func() { done <- daemonFromSnapshot(s, ext, f.calls(started)) }()
			stop := snapshotStarted(t, started, done)
			path := filepath.Join(f.endpoint.dir.path, endpointName)
			body, err := snapshotExtensionExchange(t, path)
			if fatal {
				if body != "" || err == nil {
					stop.request(nil)
					_ = snapshotWait(t, done)
					t.Fatal("fatal request received an envelope")
				}
				result := snapshotWait(t, done)
				if !errors.Is(result, ErrFatal) || !errors.Is(result, syscall.EIO) {
					t.Fatalf("serve loop lost fatal cause: %v", result)
				}
				if _, err := snapshotExtensionExchange(t, path); err == nil || ext.calls.Load() != 1 {
					t.Fatal("fatal daemon served a later request")
				}
			} else {
				if err != nil || !strings.Contains(body, `"code":"observation_unknown"`) {
					stop.request(nil)
					_ = snapshotWait(t, done)
					t.Fatalf("ordinary unknown failed: %s %v", body, err)
				}
				body, err = snapshotExtensionExchange(t, path)
				stop.request(nil)
				result := snapshotWait(t, done)
				if err != nil || result != nil || ext.calls.Load() != 2 || !strings.Contains(body, `"code":"observation_unknown"`) {
					t.Fatalf("ordinary unknown stopped serving: %s %v %v", body, err, result)
				}
			}
		})
	}
}

// Claim: simultaneous opaque-value copies have one winner and one ErrConfig; the immutable snapshot remains readable.
func TestSnapshotConcurrentConsume(t *testing.T) {
	f := snapshotControl(t)
	s := f.load(t)
	copy := *s
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, value := range []*ServerSnapshot{s, &copy} {
		wg.Add(1)
		go func(s *ServerSnapshot) { defer wg.Done(); _, err := s.take(); results <- err }(value)
	}
	wg.Wait()
	close(results)
	wins, refusals := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConfig) {
			refusals++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || refusals != 1 || !bytes.Equal(s.Bytes(), f.a) {
		t.Fatal("snapshot consumption was not single-use")
	}
}

// Claim: LoadServer retains protected-read, parse, runtime, unmanaged-pin ordering and copies its read buffer, with each failed stage preventing later checks beside a valid load.
func TestSnapshotLoadOrder(t *testing.T) {
	f := snapshotControl(t)
	for _, fail := range []string{"", "read", "runtime", "pin"} {
		var order []string
		input := append([]byte(nil), f.a...)
		calls := serverLoadCalls{read: func(string) ([]byte, error) {
			order = append(order, "read")
			if fail == "read" {
				return nil, ErrConfig
			}
			return input, nil
		}, runtime: func(commonConfig, uint32, bool) error {
			order = append(order, "runtime")
			if fail == "runtime" {
				return ErrConfig
			}
			return nil
		}, pin: func(p principal) (*directory, error) {
			order = append(order, "pin")
			if fail == "pin" {
				return nil, ErrIdentity
			}
			return f.loadCalls.pin(p)
		}}
		s, err := loadServer(f.path, calls)
		if fail == "" {
			if err != nil || strings.Join(order, ",") != "read,runtime,pin" {
				t.Fatalf("valid load order: %v %v", order, err)
			}
			input[0] = 'x'
			if !bytes.Equal(s.Bytes(), f.a) {
				t.Fatal("load retained caller's mutable buffer")
			}
		} else {
			if s != nil || err == nil {
				t.Fatal("failed load opened")
			}
			expected := map[string]string{"read": "read", "runtime": "read,runtime", "pin": "read,runtime,pin"}[fail]
			if strings.Join(order, ",") != expected {
				t.Fatal("failure crossed a later check")
			}
		}
	}
	called := false
	_, err := loadServer(f.path, serverLoadCalls{read: func(string) ([]byte, error) { return []byte(`{}`), nil }, runtime: func(commonConfig, uint32, bool) error { called = true; return nil }})
	if !errors.Is(err, ErrConfig) || called {
		t.Fatal("parse refusal crossed runtime check")
	}
}
