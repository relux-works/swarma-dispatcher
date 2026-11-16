//go:build linux

package transport

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

type managedExtension struct {
	callers  []principal
	mu       sync.Mutex
	peers    []uint32
	accepted chan struct{}
}

func (e *managedExtension) ManagedCallers() []principal { return e.callers }
func (e *managedExtension) Serve(peer uint32, id string, body []byte) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.peers = append(e.peers, peer)
	return `{"ok":true,"request_id":"` + id + `","v":1}`, nil
}

func managedFixture(t *testing.T, uid uint32, attach, listed bool) (*endpoint, *managedExtension, *directory) {
	t.Helper()
	e, c := fixtureEndpoint(t)
	d := protectedFixture(t, 0700)
	id := d.ids[len(d.ids)-1]
	p := principal{UID: uid, Dir: d.path, Dev: id.dev, Ino: id.ino}
	x := &managedExtension{accepted: make(chan struct{}, 1)}
	if listed {
		x.callers = []principal{p}
	} else if managed(uid) {
		other := p
		other.UID++
		x.callers = []principal{other}
	}
	cfg := serverConfig{commonConfig: c}
	if unmanaged(uid) {
		cfg.Callers = []principal{p}
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := &shutdown{cancel: cancel}
	done := make(chan error, 1)
	var ext Extension
	if attach {
		ext = x
	}
	// Only the observed UID/owner is substituted; all filesystem pins and socket I/O are real.
	calls := admissionCalls{
		peer: func(*os.File) (uint32, error) { return uid, nil },
		pin: func(p principal) (*directory, error) {
			p.UID = uint32(os.Geteuid())
			d, err := principalPin(p)
			if err == nil {
				select {
				case x.accepted <- struct{}{}:
				default:
				}
			}
			return d, err
		},
	}
	// Keep the list present in the no-extension control to prove attachment is
	// independently required even if a stale internal snapshot were supplied.
	pins, err := managedCallers(cfg, x, calls.pin)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-x.accepted:
	default:
	} // Discard the startup validation observation.
	go func() { done <- serveAdmitted(ctx, e, cfg, stop, ext, pins, calls) }()
	t.Cleanup(func() {
		stop.request(nil)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("serve did not stop")
		}
		if err := e.close(); err != nil {
			t.Error(err)
		}
	})
	return e, x, d
}

func managedConnect(t *testing.T, e *endpoint) (*os.File, *socketIO) {
	t.Helper()
	f, err := connectSocket(e.dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wakeClose(f) })
	return f, wireFor(t, f)
}

func requireClosed(t *testing.T, wire *socketIO) {
	t.Helper()
	b, err := wire.readFrame()
	if b != nil || !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("want base refusal (zero bytes and close), got %q %v", b, err)
	}
}

func sendRefusedHello(t *testing.T, wire *socketIO) {
	t.Helper()
	if err := wire.writeFrame(hello("a")); err != nil && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatal(err)
	}
}

func requireHello(t *testing.T, wire *socketIO) {
	t.Helper()
	if err := wire.writeFrame(hello("a")); err != nil {
		t.Fatal(err)
	}
	if b, err := wire.readFrame(); err != nil || string(b) != helloResult("a") {
		t.Fatalf("hello: %q %v", b, err)
	}
}

// Claim: only a listed managed peer with an attached extension gets hello and M0 service; other managed admissions retain the base zero-byte close and unmanaged capacity is unchanged.
func TestManagedCallerAdmission(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		uid                      uint32
		attach, listed, admitted bool
		op                       string
		result                   bool
	}{
		{"listed-m0", 30001, true, true, true, m0Frame, true},
		{"no-extension", 30001, false, true, false, m0Frame, false},
		{"unlisted", 30001, true, false, false, m0Frame, false},
		{"capacity", 30001, true, true, true, capacity("a"), false},
		{"other-operation", 30001, true, true, true, `{"op":"start","request_id":"a"}`, false},
		{"other-kind", 30001, true, true, true, `{"kind":"other","request_id":"a"}`, false},
		{"unmanaged-capacity", 1001, true, false, true, capacity("a"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, ext, _ := managedFixture(t, tc.uid, tc.attach, tc.listed)
			_, wire := managedConnect(t, e)
			if !tc.admitted {
				sendRefusedHello(t, wire)
				requireClosed(t, wire)
				return
			}
			requireHello(t, wire)
			if err := wire.writeFrame(tc.op); err != nil {
				t.Fatal(err)
			}
			if !tc.result {
				requireClosed(t, wire)
				return
			}
			b, err := wire.readFrame()
			want := `{"ok":true,"request_id":"a","v":1}`
			if unmanaged(tc.uid) {
				want = hold("a")
			}
			if err != nil || string(b) != want {
				t.Fatalf("result: %q %v", b, err)
			}
			ext.mu.Lock()
			defer ext.mu.Unlock()
			if !unmanaged(tc.uid) && (len(ext.peers) != 1 || ext.peers[0] != tc.uid) {
				t.Fatal("extension did not receive peer UID", ext.peers)
			}
			if unmanaged(tc.uid) && len(ext.peers) != 0 {
				t.Fatal("capacity routed to extension")
			}
		})
	}
}

// Claim: managed pins are rechecked before hello and before the M0 result; an unchanged pin serves the adjacent control.
func TestManagedCallerPinRecheck(t *testing.T) {
	for _, stage := range []string{"unchanged", "before-hello", "before-result"} {
		t.Run(stage, func(t *testing.T) {
			e, ext, d := managedFixture(t, 30001, true, true)
			_, wire := managedConnect(t, e)
			if stage == "before-hello" {
				select {
				case <-ext.accepted:
				case <-time.After(time.Second):
					t.Fatal("accept pin not observed")
				}
				if err := os.Chmod(d.path, 0710); err != nil {
					t.Fatal(err)
				}
			} else {
				requireHello(t, wire)
			}
			if stage == "before-hello" {
				if err := wire.writeFrame(hello("a")); err != nil {
					t.Fatal(err)
				}
				requireClosed(t, wire)
				return
			}
			if stage == "before-result" {
				if err := os.Chmod(d.path, 0710); err != nil {
					t.Fatal(err)
				}
			}
			if err := wire.writeFrame(m0Frame); err != nil {
				t.Fatal(err)
			}
			if stage == "before-result" {
				requireClosed(t, wire)
				return
			}
			if _, err := wire.readFrame(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Claim: startup rejects invalid, overlapping, service-owned or unpinnable managed lists; a valid list pins successfully and its snapshot cannot be changed by the extension.
func TestManagedCallerStartup(t *testing.T) {
	d := protectedFixture(t, 0700)
	id := d.ids[len(d.ids)-1]
	valid := principal{UID: 30001, Dir: d.path, Dev: id.dev, Ino: id.ino}
	pin := func(p principal) (*directory, error) { p.UID = uint32(os.Geteuid()); return principalPin(p) }
	for _, tc := range []struct {
		name    string
		callers []principal
		service uint32
		server  []principal
		want    error
	}{
		{"valid", []principal{valid}, 1002, nil, nil},
		{"bad-pin", []principal{{UID: 30001, Dir: d.path, Dev: id.dev, Ino: id.ino + 1}}, 1002, nil, ErrIdentity},
		{"out-of-range", []principal{{UID: 29999, Dir: d.path, Dev: id.dev, Ino: id.ino}}, 1002, nil, ErrConfig},
		{"service", []principal{valid}, 30001, nil, ErrConfig},
		{"overlap", []principal{valid}, 1002, []principal{valid}, ErrConfig},
		{"duplicate", []principal{valid, valid}, 1002, nil, ErrConfig},
		{"oversized", []principal{valid, valid, valid, valid, valid}, 1002, nil, ErrConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := &managedExtension{callers: append([]principal(nil), tc.callers...)}
			pins, err := managedCallers(serverConfig{commonConfig: commonConfig{ServiceUID: tc.service}, Callers: tc.server}, ext, pin)
			if !errors.Is(err, tc.want) {
				t.Fatalf("startup: %v want %v", err, tc.want)
			}
			if tc.want == nil {
				ext.callers[0].Dir = "/other"
				if pins[0] != valid {
					t.Fatal("mutable startup snapshot")
				}
			}
		})
	}
}

// Claim: eight managed sessions receive hello, the ninth gets the base close, and completing sessions releases their per-UID slots.
func TestManagedCallerSlots(t *testing.T) {
	e, _, _ := managedFixture(t, 30001, true, true)
	var wires []*socketIO
	for range 8 {
		_, w := managedConnect(t, e)
		requireHello(t, w)
		wires = append(wires, w)
	}
	_, ninth := managedConnect(t, e)
	sendRefusedHello(t, ninth)
	requireClosed(t, ninth)
	for _, w := range wires {
		if err := w.writeFrame(m0Frame); err != nil {
			t.Fatal(err)
		}
		if _, err := w.readFrame(); err != nil {
			t.Fatal(err)
		}
		requireClosed(t, w)
	}
	_, fresh := managedConnect(t, e)
	requireHello(t, fresh)
}
