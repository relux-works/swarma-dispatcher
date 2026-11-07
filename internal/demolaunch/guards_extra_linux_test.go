//go:build linux

package demolaunch

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Run the seal in a separate test process: ExtraFiles owns fds 3..302 there,
// never replacing a descriptor the parent's Go runtime might own. The child
// writes a numeric verdict; only the parent asserts the offending descriptor.
func TestSealAllDescriptors(t *testing.T) {
	if os.Getenv("DEMO_SEAL_HELPER") == "1" {
		report := func(fd int) {
			must(t, os.WriteFile(os.Getenv("DEMO_SEAL_RESULT"), []byte(strconv.Itoa(fd)), 0600))
		}
		flags := func(fd int) uint64 {
			b, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
			must(t, err)
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "flags:") {
					v, e := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "flags:")), 8, 64)
					must(t, e)
					return v
				}
			}
			t.Fatal("fdinfo missing flags")
			return 0
		}
		for _, fd := range []int{3, 302} {
			if flags(fd)&syscall.O_CLOEXEC != 0 {
				t.Fatalf("inheritance control fd=%d already sealed", fd)
			}
		}
		must(t, sealInheritedDescriptors())
		for fd := 3; fd <= 302; fd++ {
			if flags(fd)&syscall.O_CLOEXEC == 0 {
				report(fd)
				return
			}
		}
		directory, err := os.Open("/proc/self/fd")
		must(t, err)
		defer directory.Close()
		names, err := directory.Readdirnames(-1)
		must(t, err)
		for _, name := range names {
			fd, err := strconv.Atoi(name)
			must(t, err)
			if fd > 2 && flags(fd)&syscall.O_CLOEXEC == 0 {
				report(fd)
				return
			}
		}
		report(0)
		return
	}
	exe, err := os.Executable()
	must(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, exe, "-test.run=^TestSealAllDescriptors$")
	resultPath := filepath.Join(t.TempDir(), "seal-result")
	c.Env = append(os.Environ(), "DEMO_SEAL_HELPER=1", "DEMO_SEAL_RESULT="+resultPath)
	r, w, err := os.Pipe()
	must(t, err)
	defer r.Close()
	defer w.Close()
	for range 300 {
		c.ExtraFiles = append(c.ExtraFiles, r)
	}
	output, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("descriptor seal child failed: %v\nchild output:\n%s", err, output)
	}
	verdict, readErr := os.ReadFile(resultPath)
	fd, parseErr := strconv.Atoi(string(verdict))
	if readErr != nil || parseErr != nil || fd < 0 || fd == 1 || fd == 2 {
		t.Fatalf("descriptor seal verdict invalid: read=%v parse=%v value=%q\nchild output:\n%s", readErr, parseErr, verdict, output)
	}
	if fd != 0 {
		t.Fatalf("unsealed inherited descriptor %d\nchild output:\n%s", fd, output)
	}
}

// Both open and enumeration errors refuse. The real proc directory is the
// positive control; this changes this process's inheritance flags.
func TestSealEnumerationFailure(t *testing.T) {
	root := fixtureDir(t)
	regular := filepath.Join(root, "file")
	must(t, os.WriteFile(regular, nil, 0600))
	for _, path := range []string{filepath.Join(root, "missing"), regular} {
		if err := sealDescriptorsIn(path); err == nil {
			t.Fatalf("descriptor enumeration failure accepted: %s", filepath.Base(path))
		}
	}
	must(t, sealDescriptorsIn("/proc/self/fd"))
}

// A descendant holds stdout after the helper parent exits with a valid result.
// The holder exits when the fixture release arrives, with a bounded fallback.
func TestPipeHolder(t *testing.T) {
	root := os.Getenv("DEMO_PIPE_HOLDER")
	if root == "" {
		return
	}
	fakeWait(root, "release-stop")
	os.Exit(0)
}
func TestStopWaitsForPipes(t *testing.T) {
	h := fixture(t, "descendant-pipe")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	h.e.stopWait = 5 * time.Second
	r := h.e.handle(stopBytes(10))
	if r.State != "unresolved" || r.Code != "stop_timeout" {
		t.Fatalf("helper pipe holder became absence: %+v", r)
	}
	assertState(t, h.e.handle(stopBytes(10)), "refused", "stop_in_flight")
	must(t, os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600))
	waitChild(t, h.e.stops["demo"].child, time.Now().Add(30*time.Second))
	control := fixture(t, "good")
	assertState(t, control.e.handle(launchBytes("demo")), "receipt-seen", "")
	assertState(t, control.e.handle(stopBytes(10)), "absent", "")
}

// Cancellation is observed while the helper is alive, before its valid result.
func TestStopShutdown(t *testing.T) {
	h := fixture(t, "hold-stop")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	done := make(chan response, 1)
	go func() { done <- h.e.handle(stopBytes(10)) }()
	until := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(h.root, "exec.stop.input")); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("helper did not enter wait")
		}
		time.Sleep(time.Millisecond)
	}
	h.cancel()
	select {
	case r := <-done:
		if r.State != "unresolved" || r.Code != "shutdown" || !r.Closed {
			t.Fatalf("stop ignored shutdown: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop ignored shutdown deadline")
	}
}

// Maximum launch stdin exceeds a pipe buffer; closing the unread pipe produces
// delivery failure after Start, while the child remains alive until cleanup.
func TestLaunchDeliveryFailure(t *testing.T) {
	h := fixture(t, "close-input")
	b := []byte(`{"op":"demo.launch","request_id":"r","account":"demo","plan":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, 49152)) + `"}`)
	r := h.e.handle(b)
	if r.State != "unknown" || r.Code != "launcher_io" || !r.Closed {
		t.Fatalf("unread launcher stdin ignored: %+v", r)
	}
	if h.calls.Load() != 1 {
		t.Fatal("delivery witness did not start child")
	}
	control := fixture(t, "good")
	assertState(t, control.e.handle(b), "receipt-seen", "")
}

// The composed client receives actual SO_PEERCRED and must send no bytes to a
// different expected UID; a local peer with the expected UID completes status.
func TestClientKernelPeerGate(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(jsonNumber(boolInt(wrong)), func(t *testing.T) {
			h := fixture(t, "good")
			pinPath := filepath.Join(h.root, "caller")
			must(t, os.Mkdir(pinPath, 0700))
			d, err := pinDirectory(pinPath, uint32(os.Geteuid()))
			must(t, err)
			id := d.ids[len(d.ids)-1]
			d.close()
			c := clientConfig{commonConfig: commonConfig{ServiceUID: uint32(os.Geteuid()), ServiceGID: uint32(os.Getegid()), ConnectGID: uint32(os.Getegid())}, Caller: principal{Dev: id.dev, Ino: id.ino, UID: id.uid, Dir: pinPath}}
			must(t, os.Chmod(h.root, 0711))
			d, err = pinDirectory(h.root, uint32(os.Geteuid()))
			must(t, err)
			old := syscall.Umask(0177)
			ep, err := createEndpoint(d, c.commonConfig)
			syscall.Umask(old)
			must(t, err)
			defer ep.close()
			received := make(chan int, 1)
			go func() {
				f, start, err := acceptOne(ep.listener)
				if err != nil {
					received <- -1
					return
				}
				defer wakeClose(f)
				if wrong {
					f.SetReadDeadline(time.Now().Add(5 * time.Second))
					var b [1]byte
					n, _ := f.Read(b[:])
					received <- n
				} else {
					err = serveOperation(f, start, func() error { return nil }, h.e)
					if err != nil {
						received <- -1
					} else {
						received <- 0
					}
				}
			}()
			observer := func(uid uint32) uint32 {
				if wrong {
					return uid + 1
				}
				return uid
			}
			reply, err := clientWith("fixture", "status", []byte(`{"op":"demo.status","account":"demo"}`), func(string) (clientConfig, error) { return c, nil }, func(commonConfig, uint32, bool) error { return nil }, func(commonConfig) (*directory, error) { return pinDirectory(h.root, uint32(os.Geteuid())) }, observer)
			if wrong {
				if !errors.Is(err, ErrIdentity) || reply != "" {
					t.Errorf("client kernel peer refusal: %q %v", reply, err)
				}
			} else {
				must(t, err)
			}
			select {
			case n := <-received:
				if n != 0 {
					t.Fatalf("client sent bytes to wrong peer or server failed: %d", n)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("client peer witness stalled")
			}
		})
	}
}
