//go:build linux

package demolaunch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
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
		t.Fatalf("stage=endpoint-directory-pin: %v", err)
	}
	// Endpoint mechanism uses 0711; this changes only this fresh fixture leaf.
	if err = os.Chmod(h.root, 0711); err != nil {
		t.Fatalf("stage=endpoint-directory-mode: %v", err)
	}
	d.close()
	d, err = pinDirectory(h.root, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("stage=endpoint-directory-repin: %v", err)
	}
	// Create the caller before the socket-only umask: 0700 &^ 0177 is 0600.
	pinPath := filepath.Join(h.root, "caller")
	if err = os.Mkdir(pinPath, 0700); err != nil {
		t.Fatalf("stage=caller-mkdir: %v", err)
	}
	pin, err := pinDirectory(pinPath, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("stage=caller-directory-pin: %v", err)
	}
	id := pin.ids[len(pin.ids)-1]
	pin.close()
	if id.mode&07777 != 0700 {
		t.Fatalf("stage=caller-mode: got %04o, want 0700", id.mode&07777)
	}
	cfg := commonConfig{ServiceUID: uint32(os.Geteuid()), ServiceGID: uint32(os.Getegid()), ConnectGID: uint32(os.Getegid())}
	c := serverConfig{commonConfig: cfg, Caller: principal{Dev: id.dev, Ino: id.ino, UID: id.uid, Dir: pinPath}}
	pin, err = principalPin(c.Caller)
	if err != nil {
		t.Fatalf("stage=caller-principal-pin: %v", err)
	}
	pin.close()
	old := syscall.Umask(0177)
	defer syscall.Umask(old)
	endpoint, err := createEndpoint(d, cfg)
	if err != nil {
		t.Fatalf("stage=endpoint-create: %v", err)
	}
	defer endpoint.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := &shutdown{cancel: cancel}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, endpoint, c, stop, h.e) }()
	for i := 0; i < 9; i++ {
		f, err := connectSocket(endpoint.dir, time.Now())
		if err != nil {
			t.Fatalf("i=%d stage=connect: %v", i, err)
		}
		defer wakeClose(f)
		w, err := newSocketIO(f, time.Now())
		if err != nil {
			t.Fatalf("i=%d stage=wire-setup: %v", i, err)
		}
		err = w.writeFrame(`{"protocol":"swarma-demo-dispatcher/1"}`)
		if i == 8 && err != nil {
			continue
		}
		if err != nil {
			t.Fatalf("i=%d stage=hello-write: %v", i, err)
		}
		reply, err := w.readFrame()
		if i == 8 {
			if err == nil {
				t.Fatalf("i=%d stage=hello-read: ninth connection admitted, reply=%q", i, reply)
			}
		} else if err != nil || string(reply) != `{"protocol":"swarma-demo-dispatcher/1"}` {
			t.Fatalf("i=%d stage=hello-read: reply=%q err=%v", i, reply, err)
		}
	}
	stop.request(nil)
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("stage=server-shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stage=server-shutdown: timeout")
	}
}

// A caller directory created under the socket umask is really 0600 and must
// remain refused, not repaired. The adjacent accept test controls valid 0700.
func TestCallerPinRejectsSocketUmask(t *testing.T) {
	root := fixtureDir(t)
	old := syscall.Umask(0177)
	defer syscall.Umask(old)
	name := filepath.Join(root, "masked-caller")
	if err := os.Mkdir(name, 0700); err != nil {
		t.Fatalf("stage=masked-caller-mkdir: %v", err)
	}
	d, err := pinDirectory(name, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("stage=masked-caller-directory-pin: %v", err)
	}
	id := d.ids[len(d.ids)-1]
	d.close()
	if id.mode&07777 != 0600 {
		t.Fatalf("stage=umask-control: got %04o, want 0600", id.mode&07777)
	}
	p := principal{Dev: id.dev, Ino: id.ino, UID: id.uid, Dir: name}
	if pin, err := principalPin(p); pin != nil || !errors.Is(err, ErrIdentity) {
		if pin != nil {
			pin.close()
		}
		t.Fatalf("stage=masked-caller-refusal: want identity_mismatch, got %v", err)
	}
	info, err := os.Lstat(name)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("stage=masked-caller-preservation: info=%v err=%v", info, err)
	}
}

// Wrong protocol identity cannot negotiate with this demo; capacity remains an
// unknown operation. The valid status exchange is the adjacent positive control.
func TestDemoWireAndClient(t *testing.T) {
	h := fixture(t, "good")
	a, b := socketPair(t)
	done := make(chan error, 1)
	go func(f *os.File, result chan<- error) {
		result <- serveOperation(f, time.Now(), func() error { return nil }, h.e)
		wakeClose(f)
	}(b, done)
	reply, err := clientExchange(a, time.Now(), []byte(`{"op":"demo.status","account":"demo"}`))
	if err != nil || !bytes.Contains([]byte(reply), []byte(`"state":"facts"`)) {
		t.Fatal(reply, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	a, b = socketPair(t)
	done = make(chan error, 1)
	go func(f *os.File, result chan<- error) {
		result <- serveOperation(f, time.Now(), func() error { return nil }, h.e)
		wakeClose(f)
	}(b, done)
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

// This helper starts the real accept loop with only peer lookup and occupied
// slots injectable. Pin resolution, rechecks, protocol and cleanup remain real.
func serveFixture(t *testing.T, h *harness, variant string, supplied ...*slots) (*endpoint, func(), string) {
	t.Helper()
	pinPath := filepath.Join(h.root, "caller")
	must(t, os.Mkdir(pinPath, 0700))
	p, err := pinDirectory(pinPath, uint32(os.Geteuid()))
	must(t, err)
	id := p.ids[len(p.ids)-1]
	p.close()
	cfg := commonConfig{ServiceUID: uint32(os.Geteuid()), ServiceGID: uint32(os.Getegid()), ConnectGID: uint32(os.Getegid())}
	c := serverConfig{commonConfig: cfg, Caller: principal{Dev: id.dev, Ino: id.ino, UID: id.uid, Dir: pinPath}}
	switch variant {
	case "mode":
		must(t, os.Chmod(pinPath, 0750))
	case "replaced":
		must(t, os.Rename(pinPath, pinPath+"-old"))
		must(t, os.Mkdir(pinPath, 0700))
	case "device":
		c.Caller.Dev++
	case "inode":
		c.Caller.Ino++
	}
	must(t, os.Chmod(h.root, 0711))
	d, err := pinDirectory(h.root, uint32(os.Geteuid()))
	must(t, err)
	old := syscall.Umask(0177)
	endpoint, err := createEndpoint(d, cfg)
	syscall.Umask(old)
	must(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	stop := &shutdown{cancel: cancel}
	done := make(chan error, 1)
	peer := peerUID
	if variant == "peer" {
		peer = func(f *os.File) (uint32, error) { uid, err := peerUID(f); return uid + 1, err }
	}
	resources := newSlots()
	if len(supplied) > 0 {
		resources = supplied[0]
	}
	if variant == "global-full" {
		resources.total = 64
	}
	if variant == "global-room" {
		resources.total = 63
	}
	go func() { done <- serveWith(ctx, endpoint, c, stop, h.e, peer, resources) }()
	finish := func() {
		stop.request(nil)
		select {
		case err := <-done:
			must(t, err)
		case <-time.After(30 * time.Second):
			t.Error("accept loop did not stop")
		}
		must(t, endpoint.close())
	}
	t.Cleanup(finish)
	return endpoint, func() { must(t, os.Chmod(pinPath, 0750)) }, pinPath
}

// Real accept controls kill bypasses of peer enrollment, principal mode,
// device/inode, replacement, operation recheck and global admission. A positive
// exchange accompanies every refusal family; no distinct deployed UID is claimed.
func TestServeEnrollmentGuards(t *testing.T) {
	for _, variant := range []string{"valid", "peer", "mode", "replaced", "device", "inode", "between", "global-room", "global-full"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			ep, change, _ := serveFixture(t, h, variant)
			f, err := connectSocket(ep.dir, time.Now())
			must(t, err)
			defer wakeClose(f)
			w := testWire(t, f)
			err = w.writeFrame(`{"protocol":"swarma-demo-dispatcher/1"}`)
			admitted := variant == "valid" || variant == "between" || variant == "global-room"
			if !admitted {
				if err == nil {
					_, err = w.readFrame()
				}
				if err == nil {
					t.Fatal("stage=hello forbidden admission", variant)
				}
				if h.calls.Load() != 0 {
					t.Fatal("child on rejection")
				}
				absentFile(t, h, "attempt-"+gen)
				return
			}
			must(t, err)
			b, err := w.readFrame()
			must(t, err)
			if string(b) != `{"protocol":"swarma-demo-dispatcher/1"}` {
				t.Fatal("hello", string(b))
			}
			if variant == "between" {
				change()
			}
			err = w.writeFrame(`{"op":"demo.launch","request_id":"r","account":"demo","plan":"AAE="}`)
			w.responseWait = true
			if err == nil {
				b, err = w.readFrame()
			}
			if variant == "between" {
				if err == nil {
					t.Fatal("changed pin admitted", string(b))
				}
				absentFile(t, h, "attempt-"+gen)
				if h.calls.Load() != 0 {
					t.Fatal("changed pin launched")
				}
			} else {
				must(t, err)
				if !bytes.Contains(b, []byte(`"state":"receipt-seen"`)) {
					t.Fatal(string(b))
				}
				if h.calls.Load() != 1 {
					t.Fatal("valid control refused")
				}
			}
		})
	}
}

// Detected write failure after a real launcher call closes admission. The
// receipt reader handshake closes the peer before the result write, with no
// timing race or client retry. Kills omission of the lost-reply latch.
func TestLostLaunchReplyCloses(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(jsonNumber(boolInt(lost)), func(t *testing.T) {
			h := fixture(t, "good")
			a, b := socketPair(t)
			real := h.e.readReceipt
			h.e.readReceipt = func(ac account, id string) ([]byte, error) {
				data, err := real(ac, id)
				if err == nil && lost {
					wakeClose(a)
				}
				return data, err
			}
			done := make(chan error, 1)
			go func() { done <- serveOperation(b, time.Now(), func() error { return nil }, h.e) }()
			w := testWire(t, a)
			must(t, w.writeFrame(`{"protocol":"swarma-demo-dispatcher/1"}`))
			_, err := w.readFrame()
			must(t, err)
			must(t, w.writeFrame(string(launchBytes("demo"))))
			w.responseWait = true
			if !lost {
				reply, err := w.readFrame()
				must(t, err)
				if !bytes.Contains(reply, []byte(`"state":"receipt-seen"`)) {
					t.Fatal(string(reply))
				}
			}
			select {
			case err := <-done:
				if lost && err == nil {
					t.Fatal("write unexpectedly succeeded")
				}
				if !lost {
					must(t, err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("reply deadline")
			}
			if lost {
				fileBytes(t, h, "closed")
				assertState(t, h.e.handle(launchBytes("second")), "refused", "admission_closed")
			} else {
				absentFile(t, h, "closed")
			}
			if h.calls.Load() != 1 {
				t.Fatal("runner count")
			}
		})
	}
}

// Mutate only after accept has registered its pinned worker, or after a real
// launch. Kills removing either session pin recheck; releasing each UID/global
// slot is observed before the next valid control rather than assumed by timing.
func TestServePinRechecksAndSlotRelease(t *testing.T) {
	for _, stage := range []string{"valid", "before-hello", "before-result"} {
		t.Run(stage, func(t *testing.T) {
			h := fixture(t, "good")
			slots := newSlots()
			ep, change, _ := serveFixture(t, h, "valid", slots)
			if stage == "before-result" {
				real := h.e.readReceipt
				h.e.readReceipt = func(a account, id string) ([]byte, error) {
					b, e := real(a, id)
					if e == nil {
						change()
					}
					return b, e
				}
			}
			f, err := connectSocket(ep.dir, time.Now())
			must(t, err)
			defer wakeClose(f)
			w := testWire(t, f)
			until := time.Now().Add(30 * time.Second)
			for {
				slots.mu.Lock()
				n := len(slots.active)
				slots.mu.Unlock()
				if n == 1 {
					break
				}
				if time.Now().After(until) {
					t.Fatal("stage=worker-registration")
				}
				time.Sleep(time.Millisecond)
			}
			if stage == "before-hello" {
				change()
			}
			err = w.writeFrame(`{"protocol":"swarma-demo-dispatcher/1"}`)
			var b []byte
			if err == nil {
				b, err = w.readFrame()
			}
			if stage == "before-hello" {
				if err == nil {
					t.Fatal("changed pin greeted", string(b))
				}
				absentFile(t, h, "attempt-"+gen)
			} else {
				must(t, err)
				must(t, w.writeFrame(string(launchBytes("demo"))))
				w.responseWait = true
				b, err = w.readFrame()
				if stage == "before-result" {
					if err == nil {
						t.Fatal("changed pin received result", string(b))
					}
					fileBytes(t, h, "closed")
				} else {
					must(t, err)
					if !bytes.Contains(b, []byte(`"state":"receipt-seen"`)) {
						t.Fatal(string(b))
					}
					absentFile(t, h, "closed")
				}
			}
			wakeClose(f)
			until = time.Now().Add(30 * time.Second)
			for {
				slots.mu.Lock()
				total, uid, active := slots.total, slots.byUID[uint32(os.Geteuid())], len(slots.active)
				slots.mu.Unlock()
				if total == 0 && uid == 0 && active == 0 {
					break
				}
				if time.Now().After(until) {
					t.Fatalf("slots leaked total=%d uid=%d active=%d", total, uid, active)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

// The real accept loop never enters frame intake for a wrong peer, even with a
// queued hello. The admitted control enters exactly hello + operation intake.
// The observer counts reads but supplies no bytes and changes no verdict.
func TestServePeerBeforeFrameIntake(t *testing.T) {
	for _, variant := range []string{"valid", "peer"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			resources := newSlots()
			var reads atomic.Int32
			resources.readObserved = func() { reads.Add(1) }
			ep, _, _ := serveFixture(t, h, variant, resources)
			f, err := connectSocket(ep.dir, time.Now())
			must(t, err)
			defer wakeClose(f)
			reply, err := clientExchange(f, time.Now(), []byte(`{"op":"demo.status","account":"demo"}`))
			if variant == "valid" {
				must(t, err)
				if reads.Load() != 2 || !bytes.Contains([]byte(reply), []byte(`"state":"facts"`)) {
					t.Fatal(reads.Load(), reply)
				}
			} else {
				if err == nil || reads.Load() != 0 {
					t.Fatal("peer consumed input", reads.Load(), reply, err)
				}
			}
			if h.calls.Load() != 0 {
				t.Fatal("intake caused effect")
			}
			absentFile(t, h, "attempt-"+gen)
		})
	}
}
