//go:build linux

package demolaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A timed-out unknown-intent guess retains both child and pipe custody. A
// changed guess is admitted only after both finish; these are fixture children.
func TestUnknownGuessCustody(t *testing.T) {
	for _, mode := range []string{"slow", "descendant-pipe"} {
		t.Run(mode, func(t *testing.T) {
			h := fixture(t, "good")
			assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
			waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
			e := rebuild(t, h)
			h.mode = mode
			e.stopWait = 2 * time.Second
			before := h.calls.Load()
			assertState(t, e.handle(stopBytes(10)), "unresolved", "stop_timeout")
			ch := e.stops["demo"].child
			if ch == nil {
				t.Fatal("guess child not tracked")
			}
			if mode == "descendant-pipe" {
				select {
				case <-ch.done:
				case <-time.After(5 * time.Second):
					t.Fatal("helper parent did not exit")
				}
				select {
				case <-ch.outputDone:
					t.Fatal("descendant did not hold pipe")
				default:
				}
			} else {
				select {
				case <-ch.done:
					t.Fatal("helper was not live")
				default:
				}
			}
			changed := []byte(`{"op":"demo.stop","request_id":"another","account":"demo","grace_seconds":9}`)
			r := e.handle(changed)
			if r.State != "refused" || r.Code != "stop_in_flight" || h.calls.Load() != before+1 {
				t.Fatalf("unknown guess overlapped custody: %+v calls=%d", r, h.calls.Load()-before)
			}
			must(t, os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600))
			waitChild(t, ch, time.Now().Add(30*time.Second))
			h.mode = "good"
			r = e.handle(changed)
			if r.State != "absent" || r.Timing != "unknown" || !r.Closed || h.calls.Load() != before+2 {
				t.Fatalf("released guess control refused: %+v", r)
			}
		})
	}
}

// The real reader sees exact metadata; launch uses it and must translate every
// superset mode into receipt_mismatch, never receipt-seen. Owner is fixture UID.
func TestReceiptExactModes(t *testing.T) {
	for _, mode := range []os.FileMode{0644, 0664, 0666, 0646, 0744, 0644 | os.ModeSetgid} {
		h := fixture(t, "good")
		h.e.readReceipt = func(a account, id string) ([]byte, error) {
			name := filepath.Join(h.root, a.Generation, id+".json")
			if err := os.Chmod(name, mode); err != nil {
				return nil, err
			}
			return h.e.receiptBytes(a, id)
		}
		r := h.e.handle(launchBytes("demo"))
		if mode == 0644 {
			assertState(t, r, "receipt-seen", "")
		} else if r.State != "unknown" || r.Code != "receipt_mismatch" || !r.Closed {
			t.Errorf("superset receipt mode accepted: mode=%v reply=%+v", mode, r)
		}
	}
}

// Holding the state mutex after child tracking models a slow concurrent fsync:
// a verdict after the original deadline must latch even though absence is valid.
func TestLateVerdictLatch(t *testing.T) {
	h := fixture(t, "hold-stop")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	h.e.stopWait = 3 * time.Second
	done := make(chan response, 1)
	go func() { done <- h.e.handle(stopBytes(10)) }()
	until := time.Now().Add(10 * time.Second)
	locked := false
	for time.Now().Before(until) {
		h.e.mu.Lock()
		s := h.e.stops["demo"]
		if s != nil && s.child != nil {
			locked = true
			mustRelease := func() {
				defer h.e.mu.Unlock()
				must(t, os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600))
				waitChild(t, s.child, time.Now().Add(10*time.Second))
				time.Sleep(time.Until(s.deadline.Add(300 * time.Millisecond)))
			}
			mustRelease()
			break
		}
		h.e.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if !locked {
		t.Fatal("late verdict child not tracked")
	}
	var r response
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("late verdict did not return")
	}
	if r.State != "absent" || r.Timing != "missed" {
		t.Fatalf("late absence control failed: %+v", r)
	}
	if !r.Closed {
		t.Error("late absence left admission open")
	}
	before := h.calls.Load()
	next := h.e.handle(launchBytes("second"))
	if next.State != "refused" || next.Code != "admission_closed" || h.calls.Load() != before {
		t.Errorf("late absence admitted next launch: %+v", next)
	}
	if _, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"], h.e.accounts["second"]}, serverConfig{}); err != nil {
		t.Errorf("late stopped fact refused restart: %v", err)
	}
}

// Both sides use the same pin function; the path seam changes only its location.
// A replacement inode with correct UID/GID/mode must fail, as must a wrong device.
func TestEndpointPinIdentity(t *testing.T) {
	root := fixtureDir(t)
	name := filepath.Join(root, "endpoint")
	must(t, os.Mkdir(name, 0711))
	must(t, os.Chmod(name, 0711))
	f, err := os.Open(name)
	must(t, err)
	defer f.Close()
	id, err := identity(f)
	must(t, err)
	c := commonConfig{ServiceUID: uint32(os.Geteuid()), ServiceGID: uint32(os.Getegid()), EndpointDev: id.dev, EndpointIno: id.ino}
	for _, side := range []string{"daemon", "client"} {
		d, err := endpointPinIn(c, name)
		must(t, err)
		d.close()
		bad := c
		bad.EndpointDev++
		d, err = endpointPinIn(bad, name)
		if d != nil {
			d.close()
		}
		if !errors.Is(err, ErrIdentity) || d != nil {
			t.Fatalf("endpoint pin accepted wrong device: %s %v", side, err)
		}
	}
	must(t, os.Rename(name, name+"-old"))
	must(t, os.Mkdir(name, 0711))
	must(t, os.Chmod(name, 0711))
	for _, side := range []string{"daemon", "client"} {
		d, err := endpointPinIn(c, name)
		if d != nil {
			d.close()
		}
		if !errors.Is(err, ErrIdentity) || d != nil {
			t.Fatalf("endpoint pin accepted replacement: %s %v", side, err)
		}
	}
}

// SIGCHLD is ignored only inside an isolated re-exec. Kernel auto-reaping makes
// Wait4 return ECHILD; a valid result without an observed exit is not absence.
// The deployed daemon does not set SIGCHLD to ignored.
func TestWaitErrorRefusesAbsence(t *testing.T) {
	if os.Getenv("DEMO_WAIT_WITNESS") == "" {
		exe, err := os.Executable()
		must(t, err)
		for _, ignore := range []string{"no", "yes"} {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			c := exec.CommandContext(ctx, exe, "-test.run=^TestWaitErrorRefusesAbsence$", "-test.v")
			c.Env = append(os.Environ(), "DEMO_WAIT_WITNESS="+ignore, "GORACE=atexit_sleep_ms=0")
			out, err := c.CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("wait error witness failed: %v\n%s", err, out)
			}
		}
		return
	}
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
	if os.Getenv("DEMO_WAIT_WITNESS") == "yes" {
		signal.Ignore(syscall.SIGCHLD)
		r := h.e.handle(stopBytes(10))
		if r.State != "unresolved" || r.Code != "helper_exit" {
			t.Fatalf("wait error accepted as zero exit: %+v", r)
		}
	} else {
		assertState(t, h.e.handle(stopBytes(10)), "absent", "")
	}
}

// Delaying the fixture runner until the original write deadline has expired
// forces helper stdin failure after Start. The neighboring normal call succeeds.
func TestHelperDeliveryDeadline(t *testing.T) {
	for _, late := range []bool{false, true} {
		h := fixture(t, "good")
		assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
		if late {
			inner := h.e.run
			h.e.stopWait = time.Second
			h.e.run = func(path string) *exec.Cmd {
				c := inner(path)
				if path == managerPath {
					time.Sleep(1500 * time.Millisecond)
				}
				return c
			}
		}
		r := h.e.handle(stopBytes(10))
		if !late {
			assertState(t, r, "absent", "")
		} else if r.State != "unresolved" || r.Code != "helper_io" || !r.Closed {
			t.Fatalf("helper delivery failure not reported: %+v", r)
		}
	}
}

// A failed first latch retains its original reason across later faults and a
// healed request; the persisted reason is evidence, not an admission decision.
func TestLatchRetryReason(t *testing.T) {
	h := fixture(t, "good")
	h.e.store.createFault = func(string) error { return syscall.EIO }
	if err := h.e.closeAdmission("original_failure"); err == nil {
		t.Fatal("latch fault absent")
	}
	_ = h.e.closeAdmission("later_failure")
	h.e.store.createFault = nil
	r := h.e.handle([]byte(`{"op":"demo.status","account":"demo"}`))
	var record map[string]string
	must(t, typed(fileBytes(t, h, "closed"), &record))
	if record["reason"] != "original_failure" || !r.Closed || r.Code != "" {
		t.Fatalf("latch retry lost original reason: %v %+v", record, r)
	}
}

// Client composition reaches the shared endpoint predicate after the real caller
// pin; a correct live endpoint exchanges facts, a replacement inode is refused.
func TestEndpointPinClient(t *testing.T) {
	for _, replace := range []bool{false, true} {
		h := fixture(t, "good")
		ep, _, pinPath := serveFixture(t, h, "valid")
		d, err := pinDirectory(pinPath, uint32(os.Geteuid()))
		must(t, err)
		id := d.ids[len(d.ids)-1]
		d.close()
		end := ep.dir.ids[len(ep.dir.ids)-1]
		c := clientConfig{commonConfig: commonConfig{ServiceUID: uint32(os.Geteuid()), ServiceGID: uint32(os.Getegid()), ConnectGID: uint32(os.Getegid()), EndpointDev: end.dev, EndpointIno: end.ino}, Caller: principal{Dev: id.dev, Ino: id.ino, UID: id.uid, Dir: pinPath}}
		name := ep.dir.path
		if replace {
			name = filepath.Join(h.root, "replacement")
			must(t, os.Mkdir(name, 0711))
			must(t, os.Chmod(name, 0711))
		}
		reply, err := clientWith("fixture", "status", []byte(`{"op":"demo.status","account":"demo"}`), func(string) (clientConfig, error) { return c, nil }, func(commonConfig, uint32, bool) error { return nil }, func(c commonConfig) (*directory, error) { return endpointPinIn(c, name) })
		if replace {
			if !errors.Is(err, ErrIdentity) || reply != "" {
				t.Fatalf("endpoint pin accepted replacement: client err=%v reply=%q", err, reply)
			}
		} else if err != nil || !strings.Contains(reply, `"state":"facts"`) {
			t.Fatalf("endpoint client control: %v %q", err, reply)
		}
	}
}
