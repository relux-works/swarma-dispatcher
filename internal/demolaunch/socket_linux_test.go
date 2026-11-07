//go:build linux

package demolaunch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func socketPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fds, e := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if e != nil {
		t.Fatal(e)
	}
	a, b := os.NewFile(uintptr(fds[0]), "fixture"), os.NewFile(uintptr(fds[1]), "fixture")
	t.Cleanup(func() { wakeClose(a); wakeClose(b) })
	for _, f := range []*os.File{a, b} {
		if e = tuneSocket(f); e != nil {
			t.Fatal(e)
		}
	}
	return a, b
}
func testWire(t *testing.T, f *os.File) *socketIO {
	t.Helper()
	w, e := newSocketIO(f, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	return w
}

// Kernel peer and pin verification precedes the first read. MSG_PEEK observes
// the exact queued bytes after rejection; a valid peer consumes the same probe.
func TestPeerGateBeforeRead(t *testing.T) {
	h := fixture(t, "good")
	for _, wrong := range []bool{false, true} {
		a, b := socketPair(t)
		w := testWire(t, a)
		if err := w.writeFrame(`{"protocol":"swarma-demo-dispatcher/1"}`); err != nil {
			t.Fatal(err)
		}
		uid := uint32(os.Geteuid())
		if wrong {
			uid++
		}
		called := false
		err := authenticate(b, uid, func() error { called = true; return nil })
		if wrong {
			if !errors.Is(err, ErrIdentity) || called {
				t.Fatal("wrong peer passed", err, called)
			}
			var probe [4]byte
			err = control(b, func(fd int) error {
				n, _, e := syscall.Recvfrom(fd, probe[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
				if n != 4 {
					t.Errorf("prefix consumed: %d", n)
				}
				return e
			})
			if err != nil || probe != [4]byte{0, 0, 0, 39} {
				t.Fatal("peer bytes changed", probe, err)
			}
		} else {
			if err != nil || !called {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- serveOperation(b, time.Now(), func() error { return nil }, h.e) }()
			if _, err = w.readFrame(); err != nil {
				t.Fatal(err)
			}
			if err = w.writeFrame(`{"op":"demo.status","account":"demo"}`); err != nil {
				t.Fatal(err)
			}
			reply, err := w.readFrame()
			if err != nil || !bytes.Contains(reply, []byte(`"state":"facts"`)) {
				t.Fatal(string(reply), err)
			}
			select {
			case err = <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("server blocked")
			}
		}
	}
	a, _ := socketPair(t)
	if err := authenticate(a, uint32(os.Geteuid()), func() error { return ErrIdentity }); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

// The actual accept path permits only the pinned current-UID fixture and
// refuses the ninth connection. It does not qualify distinct deployed UIDs.
func TestAcceptSlotsAndDemoIdentity(t *testing.T) {
	h := fixture(t, "good")
	d, err := pinDirectory(h.root, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	// Endpoint mechanism uses 0711; this changes only this fresh fixture leaf.
	if err = os.Chmod(h.root, 0711); err != nil {
		t.Fatal(err)
	}
	d.close()
	d, err = pinDirectory(h.root, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0177)
	defer syscall.Umask(old)
	cfg := commonConfig{ServiceUID: uint32(os.Geteuid()), ServiceGID: uint32(os.Getegid()), ConnectGID: uint32(os.Getegid())}
	endpoint, err := createEndpoint(d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.close()
	pinPath := filepath.Join(h.root, "caller")
	if err = os.Mkdir(pinPath, 0700); err != nil {
		t.Fatal(err)
	}
	pin, err := pinDirectory(pinPath, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	id := pin.ids[len(pin.ids)-1]
	pin.close()
	c := serverConfig{commonConfig: cfg, Caller: principal{Dev: id.dev, Ino: id.ino, UID: id.uid, Dir: pinPath}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := &shutdown{cancel: cancel}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, endpoint, c, stop, h.e) }()
	for i := 0; i < 9; i++ {
		f, err := connectSocket(endpoint.dir, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		defer wakeClose(f)
		w := testWire(t, f)
		err = w.writeFrame(`{"protocol":"swarma-demo-dispatcher/1"}`)
		if i == 8 && err != nil {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		reply, err := w.readFrame()
		if i == 8 {
			if err == nil {
				t.Fatal("ninth admitted", string(reply))
			}
		} else if err != nil || string(reply) != `{"protocol":"swarma-demo-dispatcher/1"}` {
			t.Fatal(string(reply), err)
		}
	}
	stop.request(nil)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown blocked")
	}
}

// Wrong protocol identity cannot negotiate with this demo; capacity remains an
// unknown operation. The valid status exchange is the adjacent positive control.
func TestDemoWireAndClient(t *testing.T) {
	h := fixture(t, "good")
	a, b := socketPair(t)
	done := make(chan error, 1)
	go func() { done <- serveOperation(b, time.Now(), func() error { return nil }, h.e); wakeClose(b) }()
	reply, err := clientExchange(a, time.Now(), []byte(`{"op":"demo.status","account":"demo"}`))
	if err != nil || !bytes.Contains([]byte(reply), []byte(`"state":"facts"`)) {
		t.Fatal(reply, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	a, b = socketPair(t)
	done = make(chan error, 1)
	go func() { done <- serveOperation(b, time.Now(), func() error { return nil }, h.e); wakeClose(b) }()
	w := testWire(t, a)
	if err = w.writeFrame(`{"request_id":"r","versions":[1]}`); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = w.readFrame(); err == nil {
		t.Fatal("production hello accepted")
	}
	assertState(t, h.e.handle([]byte(`{"op":"capacity","account":"demo"}`)), "refused", "operation_unknown")
	if h.calls.Load() != 0 {
		t.Fatal("wire caused launch")
	}
}

// The demo copy retains ancillary rejection and finite partial-frame deadlines;
// a literal ordinary frame is the positive control for the same receive path.
func TestAncillaryAndDeadline(t *testing.T) {
	a, b := socketPair(t)
	w := testWire(t, b)
	if _, err := a.Write([]byte{0, 0, 0, 1, 'x'}); err != nil {
		t.Fatal(err)
	}
	if got, err := w.readFrame(); err != nil || string(got) != "x" {
		t.Fatal(string(got), err)
	}
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if err = control(a, func(fd int) error {
		return syscall.Sendmsg(fd, []byte{0, 0, 0, 1}, syscall.UnixRights(int(file.Fd())), nil, syscall.MSG_DONTWAIT|syscall.MSG_NOSIGNAL)
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := w.readFrame(); got != nil || !errors.Is(err, ErrAncillary) {
		t.Fatal(got, err)
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil || len(after) != len(before) {
		t.Fatal("received right leaked", err)
	}
	a, b = socketPair(t)
	w = testWire(t, b)
	if _, err = a.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	w.start = time.Now().Add(-sessionBudget + 20*time.Millisecond)
	if got, err := w.readFrame(); got != nil || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("partial frame deadline", got, err)
	}
}
