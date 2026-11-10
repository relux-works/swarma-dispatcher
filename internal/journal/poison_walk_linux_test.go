//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"errors"
	"fmt"
	"syscall"
	"testing"
)

// Spec 215, 219-221: a protection refusal followed by a failed close must keep
// invalid-state first and poison the terminal error/state. Real descriptors
// back both paths; only stat data and the close errno are fault-injected.
func TestReplaceRecordDoubleFault(t *testing.T) {
	for _, phase := range []string{"Session", "Consume", "Finish"} {
		for _, fault := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fault-%v", phase, fault), func(t *testing.T) {
				c, e, s := sweepSetup(t)
				if phase != "Session" {
					must(t, s.Close())
				}
				history := disk(t, c)
				target, stats, closes := -1, 0, 0
				armed := false
				open, stat := e.calls.open, e.calls.stat
				e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
					fd, err := open(d, n, f, m)
					if armed && n == "record" && err == nil {
						target = fd
					}
					return fd, err
				}
				e.calls.stat = func(fd int, st *syscall.Stat_t) error {
					err := stat(fd, st)
					if armed && fd == target && err == nil {
						stats++
						if fault {
							st.Mode ^= 0040
						}
					}
					return err
				}
				// The outer adapter closes the real fd even when this hook returns EIO.
				e.monitor.hook = func(v seamEvent) error {
					if armed && v.kind == "close" && v.fd == target {
						closes++
						target = -1
						if fault {
							return syscall.EIO
						}
					}
					return nil
				}
				var err error
				if phase == "Session" {
					armed = true
					err = s.ReplaceRecord("record", []byte("new"))
					armed = false
				} else {
					callback := func(r *Recovery) error {
						s = r.s
						armed = true
						opErr := r.ReplaceRecord("record", []byte("new"))
						armed = false
						if fault && ClassOf(opErr) != InvalidState {
							t.Errorf("DOUBLE-FAULT: helper class changed: %v", opErr)
						}
						sweepContender(t, c, OwnershipContended)
						return nil
					}
					cons := consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error { return callback(r) }}
					if phase == "Finish" {
						cons = consumerFuncs{finish: callback}
					}
					var published *Session
					published, err = e.enter(c, nil, cons, false)
					if fault && published != nil {
						t.Error("DOUBLE-FAULT: failed recovery published")
					}
					if !fault {
						s = published
					}
				}
				e.monitor.hook = nil
				if stats != 1 || closes != 1 {
					t.Fatalf("DOUBLE-FAULT: expected one probe stat/close, got %d/%d", stats, closes)
				}
				if fault {
					var je *Error
					if ClassOf(err) != InvalidState || !errors.As(err, &je) || !je.Poisoned || !s.State().Poisoned || !s.State().Closed || s.terminal != err {
						t.Errorf("DOUBLE-FAULT: lost first class or close poison: %v state=%+v", err, s.State())
					}
				} else {
					if err != nil || s == nil || !s.State().Ready {
						t.Fatalf("DOUBLE-FAULT: valid replacement refused: %v", err)
					}
					must(t, s.Close())
				}
				if !bytes.Equal(history, disk(t, c)) {
					t.Error("DOUBLE-FAULT: journal changed")
				}
				sweepContender(t, c, "")
			})
		}
	}
}

// A shared-lock conversion during either callback is reported, without actually
// changing the fixture lock, in blocking and nonblocking forms. The same
// adapter call outside a callback is the paired silent control; no real
// blocking conversion is attempted by this fixture witness.
func TestOwnershipMonitorSharedConversion(t *testing.T) {
	for _, mode := range []struct {
		name      string
		flags     int
		assertion string
	}{
		{"nonblocking", syscall.LOCK_SH | syscall.LOCK_NB, "LOCK-SH-CONTROL:"},
		{"blocking", syscall.LOCK_SH, "LOCK-SH-BLOCKING:"},
	} {
		for _, phase := range []string{"Consume", "Finish"} {
			t.Run(mode.name+"/"+phase, func(t *testing.T) {
				c, e, s := sweepSetup(t)
				must(t, s.Close())
				flock := e.calls.flock
				e.calls.flock = func(fd, how int) error {
					if how&syscall.LOCK_SH != 0 {
						return nil
					}
					return flock(fd, how)
				}
				hits := 0
				e.monitor.report = func(string, ...any) { hits++ }
				callback := func(r *Recovery) error { return r.s.e.calls.flock(r.s.lock, mode.flags) }
				cons := consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error { return callback(r) }}
				if phase == "Finish" {
					cons = consumerFuncs{finish: callback}
				}
				got, err := e.enter(c, nil, cons, false)
				must(t, err)
				inside := hits
				got.mu.Lock()
				must(t, got.e.calls.flock(got.lock, mode.flags))
				got.mu.Unlock()
				e.monitor.report = t.Errorf
				e.calls.flock = flock
				must(t, got.Close())
				if inside != 1 || hits != 1 {
					t.Errorf("%s callback conversion missed or inactive callback reported: %d/%d", mode.assertion, inside, hits)
				}
			})
		}
	}
}
