//go:build linux

package demolaunch

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests use real captures and a fixture store, but no child or privileged
// runner. A synthetic completed child makes status inspect the capture normally.
func latchCapture(t *testing.T) (*harness, *capture) {
	t.Helper()
	h := fixture(t, "good")
	c, err := h.e.logCapture(gen, "stdout")
	must(t, err)
	t.Cleanup(func() { c.file.Close() })
	c.writeFile = func([]byte) (int, error) { return 0, syscall.EIO }
	done := make(chan struct{})
	close(done)
	h.e.launchChildren["demo"] = &child{done: done, outputDone: done, stdout: c, stderr: &capture{}}
	return h, c
}

func awaitLatchEvent(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

// Force status to own engine.mu while .lost creation owns capture.mu. TryLock
// observes ownership, not a scheduling sleep. The persisted-loss control must
// complete with admission open; the failed create must latch after status exits.
func TestLossLatchLockOrder(t *testing.T) {
	for _, persisted := range []bool{true, false} {
		t.Run(map[bool]string{true: "persisted", false: "unpersisted"}[persisted], func(t *testing.T) {
			h, c := latchCapture(t)
			entered, release, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
			realLoss := c.onLoss
			c.onLoss = func() error {
				close(entered)
				<-release
				if persisted {
					return realLoss()
				}
				return syscall.ENOSPC
			}
			go func() { c.write([]byte("loss")); close(written) }()
			awaitLatchEvent(t, entered, "loss create not reached")
			statusDone := make(chan struct{})
			go func() { h.e.status("demo"); close(statusDone) }()
			until := time.Now().Add(5 * time.Second)
			for h.e.mu.TryLock() {
				h.e.mu.Unlock()
				if time.Now().After(until) {
					close(release)
					t.Fatal("status did not acquire engine lock")
				}
				runtime.Gosched()
			}
			close(release)
			awaitLatchEvent(t, statusDone, "loss latch deadlocked against concurrent status")
			awaitLatchEvent(t, written, "loss latch callback did not finish")
			got := h.e.status("demo")
			want := "stdout:loss_unpersisted"
			if persisted {
				want = "stdout:output_lost"
			}
			if got.State != "facts" || got.Code != "" || got.Closed == persisted || len(got.OutputLoss) != 1 || got.OutputLoss[0] != want || h.calls.Load() != 0 {
				t.Fatalf("loss handoff result: %+v calls=%d", got, h.calls.Load())
			}
		})
	}
}

// A held engine mutex must prevent the production latch callback from changing
// fields. Wait for its actual mutex-blocked stack or completion, rather than
// assuming a goroutine ran after a sleep. Completion synchronizes the negative
// case too, so the assertion detects a missing lock without -race. This checks
// one forced interleaving; the stack observation is specific to Go's mutex.
func TestLossLatchEngineLock(t *testing.T) {
	h, c := latchCapture(t)
	c.onLoss = func() error { return syscall.ENOSPC }
	callbackDone := make(chan struct{})
	realCallback := c.onLossUnpersisted
	c.onLossUnpersisted = func() { realCallback(); close(callbackDone) }
	h.e.mu.Lock()
	locked := true
	defer func() {
		if locked {
			h.e.mu.Unlock()
		}
	}()
	written := make(chan struct{})
	go func() { c.write([]byte("loss")); close(written) }()
	until := time.Now().Add(5 * time.Second)
	completed := false
	for {
		select {
		case <-callbackDone:
			completed = true
		default:
		}
		if completed {
			break
		}
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		blocked := false
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, ".logCapture.func") && strings.Contains(stack, "[sync.Mutex.Lock]") {
				blocked = true
			}
		}
		if blocked {
			break
		}
		if time.Now().After(until) {
			t.Fatal("latch callback neither blocked nor completed")
		}
		runtime.Gosched()
	}
	if completed || h.e.closed || h.e.latchWritten || h.e.latchReason != "" {
		t.Fatal("loss latch changed fields while engine lock held")
	}
	h.e.mu.Unlock()
	locked = false
	awaitLatchEvent(t, written, "loss latch did not resume after engine unlock")
	if got := h.e.status("demo"); !got.Closed || got.Code != "" || h.calls.Load() != 0 {
		t.Fatalf("loss latch unlock control: %+v", got)
	}
}

// Read closed before any request can retry persistence. A successfully recorded
// .lost is the nearby control and must not write closed or close admission.
func TestLossLatchImmediateDurable(t *testing.T) {
	for _, persisted := range []bool{true, false} {
		t.Run(map[bool]string{true: "persisted", false: "unpersisted"}[persisted], func(t *testing.T) {
			h, c := latchCapture(t)
			if !persisted {
				c.onLoss = func() error { return syscall.ENOSPC }
			}
			c.write([]byte("loss"))
			b, err := h.e.store.read("closed")
			if persisted {
				if !os.IsNotExist(err) || h.e.closed {
					t.Fatal("persisted loss unexpectedly latched")
				}
			} else if err != nil || !strings.Contains(string(b), `"reason":"loss_unpersisted"`) {
				t.Fatalf("durable loss latch not attempted at loss time: %q %v", b, err)
			}
			if h.calls.Load() != 0 {
				t.Fatal("capture invoked launcher")
			}
		})
	}
}

// Probe inside closeAdmission's exclusive create, after closed/reason writes
// and before latchWritten. With no other engine user, a failed TryLock proves
// the callback still owns the lock at persistence, not just at callback entry.
// This forced interleaving complements the existing entry/order witnesses; it
// does not instrument every instruction. Intact .lost is the no-latch control.
func TestLossLatchWriteLockScope(t *testing.T) {
	for _, persisted := range []bool{true, false} {
		t.Run(map[bool]string{true: "persisted", false: "unpersisted"}[persisted], func(t *testing.T) {
			h, c := latchCapture(t)
			if !persisted {
				c.onLoss = func() error { return syscall.ENOSPC }
			}
			calls, unlocked := 0, false
			fields := false
			h.e.store.createFault = func(name string) error {
				if name != "closed" {
					return nil
				}
				calls++
				if h.e.mu.TryLock() {
					unlocked = true
					h.e.mu.Unlock()
				}
				fields = h.e.closed && h.e.latchReason == "loss_unpersisted" && !h.e.latchWritten
				return nil
			}
			c.write([]byte("loss"))
			h.e.store.createFault = nil
			if persisted {
				if calls != 0 || h.e.closed {
					t.Fatal("persisted loss scope control latched")
				}
			} else if calls != 1 || unlocked || !fields || !h.e.latchWritten {
				t.Fatalf("loss latch write outside engine lock: calls=%d unlocked=%v fields=%v written=%v", calls, unlocked, fields, h.e.latchWritten)
			}
			if h.calls.Load() != 0 {
				t.Fatal("latch scope invoked launcher")
			}
		})
	}
}
