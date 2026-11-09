//go:build linux && (amd64 || arm64)

package journal

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"
)

func guardContenderResult(t *testing.T, c Config) (Class, bool) {
	t.Helper()
	e := newFixtureEngine(t, nativeCalls)
	e.calls.statfs = func(_ int, st *syscall.Statfs_t) error { st.Type = 0xEF53; return nil }
	s, err := e.enter(c, nil, consumerFuncs{}, false)
	if s != nil {
		_ = s.Close()
	}
	return ClassOf(err), s != nil
}

// Claim (Recovery doc: a temporary capability; doc.go:36 Session methods
// serialize local I/O): a Recovery retained past the callback and used while
// the published Session is still live refuses invalid-state with no seam call,
// leaves the Session ready (Append still succeeds) and keeps ownership. The
// adjacent retained-Recovery test also covers use after Close.
func TestRecoveryExpiredLive(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.CreateInput("arg", []byte("abc")))
	must(t, s.Close())
	var retained *Recovery
	live, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error { retained = r; return nil }}, false)
	must(t, err)
	defer live.Close()
	events := 0
	e.monitor.hook = func(seamEvent) error { events++; return nil }
	var msgs []string
	e.monitor.report = func(f string, a ...any) { msgs = append(msgs, fmt.Sprintf(f, a...)) }
	rerr := retained.ReplaceRecord("late", []byte("x"))
	_, ierr := retained.ReadInput("arg", sha256.Sum256([]byte("abc")))
	e.monitor.hook = nil
	e.monitor.report = t.Errorf
	st := live.State()
	cl, acq := guardContenderResult(t, c)
	_, aerr := live.Append([]byte("z"))
	if ClassOf(rerr) != InvalidState || ClassOf(ierr) != InvalidState || events != 0 || len(msgs) != 0 || !st.Ready || st.Closed || aerr != nil || acq || cl != OwnershipContended {
		t.Fatalf("EXPIRED-LIVE: replace=%v read=%v seam-events=%d monitor=%v state=%+v append=%v contender=%s acquired=%v", rerr, ierr, events, msgs, st, aerr, cl, acq)
	}
}

// Claim (positive controls for fixture monitor wiring): (a) inside a real consumer callback, in both phases, an unlock of the
// session's own lock through the session's seam table reports exactly one
// OWNERSHIP-MONITOR event, and the same call outside a callback reports none;
// (b) on a published Session every seam kind (open, stat, statfs, read, write,
// sync, rename, close, flock, and a descriptor inherited from a relative open)
// reports exactly one SERIAL-MONITOR event without the mutex and none with it.
// Inner seams are no-ops while violations are provoked: no lock is released.
func testFixtureMonitorWiring(t *testing.T) {
	for _, phase := range []string{"Consume", "Finish"} {
		t.Run("ownership/"+phase, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			must(t, s.Close())
			fl := e.calls.flock
			e.calls.flock = func(fd, how int) error {
				if how&syscall.LOCK_UN != 0 {
					return nil
				}
				return fl(fd, how)
			}
			var msgs []string
			e.monitor.report = func(f string, a ...any) { msgs = append(msgs, fmt.Sprintf(f, a...)) }
			unlock := func(r *Recovery) error { return r.s.e.calls.flock(r.s.lock, syscall.LOCK_UN) }
			cons := consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error { return unlock(r) }}
			if phase == "Finish" {
				cons = consumerFuncs{finish: unlock}
			}
			got, err := e.enter(c, nil, cons, false)
			must(t, err)
			inside := append([]string{}, msgs...)
			got.mu.Lock()
			_ = got.e.calls.flock(got.lock, syscall.LOCK_UN) // outside any callback: not an ownership event
			got.mu.Unlock()
			e.monitor.report = t.Errorf
			e.calls.flock = fl
			must(t, got.Close())
			if len(inside) != 1 || !strings.Contains(inside[0], "OWNERSHIP-MONITOR:") || len(msgs) != 1 || e.monitor.ownershipHits != 1 {
				t.Fatalf("MONITOR-WIRING: ownership %s: inside=%v all=%v hits=%d", phase, inside, msgs, e.monitor.ownershipHits)
			}
		})
	}
	t.Run("serial", func(t *testing.T) {
		c, e := fixture(t, true)
		s := start(t, c, e)
		_ = c
		orig := e.calls
		var msgs []string
		e.monitor.report = func(f string, a ...any) { msgs = append(msgs, fmt.Sprintf(f, a...)) }
		inherited := -1
		s.mu.Lock()
		fd, err := s.e.calls.open(s.records, ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
		s.mu.Unlock()
		must(t, err)
		inherited = fd
		e.calls = calls{
			open:   func(int, string, int, uint32) (int, error) { return -1, syscall.ENOENT },
			stat:   func(int, *syscall.Stat_t) error { return nil },
			statfs: func(int, *syscall.Statfs_t) error { return nil },
			flock:  func(int, int) error { return nil },
			read:   func(int, []byte, int64) (int, error) { return 0, nil },
			write:  func(_ int, p []byte, _ int64) (int, error) { return len(p), nil },
			sync:   func(int) error { return nil },
			rename: func(int, string, int, string) error { return nil },
			close:  func(int) error { return nil },
			random: orig.random,
		}
		var st syscall.Stat_t
		var fs syscall.Statfs_t
		buf := make([]byte, 1)
		kinds := []struct {
			kind string
			fd   int
			call func()
		}{
			{"open", s.records, func() { _, _ = s.e.calls.open(s.records, "x", syscall.O_RDONLY, 0) }},
			{"stat", s.journal, func() { _ = s.e.calls.stat(s.journal, &st) }},
			{"statfs", s.root, func() { _ = s.e.calls.statfs(s.root, &fs) }},
			{"read", s.journal, func() { _, _ = s.e.calls.read(s.journal, buf, 0) }},
			{"write", s.journal, func() { _, _ = s.e.calls.write(s.journal, buf, 0) }},
			{"sync", s.journal, func() { _ = s.e.calls.sync(s.journal) }},
			{"rename", s.records, func() { _ = s.e.calls.rename(s.records, "a", s.records, "b") }},
			{"close", s.journal, func() { _ = s.e.calls.close(s.journal) }},
			{"flock", s.lock, func() { _ = s.e.calls.flock(s.lock, syscall.LOCK_EX|syscall.LOCK_NB) }},
			{"inherited-stat", inherited, func() { _ = s.e.calls.stat(inherited, &st) }},
		}
		bad := []string{}
		for _, k := range kinds {
			n := len(msgs)
			k.call()
			unlocked := msgs[n:]
			e.monitor.mu.Lock()
			e.monitor.published[k.fd] = s // the close adapter forgets the descriptor
			e.monitor.mu.Unlock()
			s.mu.Lock()
			n2 := len(msgs)
			k.call()
			locked := msgs[n2:]
			s.mu.Unlock()
			e.monitor.mu.Lock()
			e.monitor.published[k.fd] = s
			e.monitor.mu.Unlock()
			if len(unlocked) != 1 || !strings.Contains(unlocked[0], "SERIAL-MONITOR:") || len(locked) != 0 {
				bad = append(bad, fmt.Sprintf("%s unlocked=%v locked=%v", k.kind, unlocked, locked))
			}
		}
		e.calls = orig
		e.monitor.report = t.Errorf
		s.mu.Lock()
		must(t, s.e.calls.close(inherited))
		s.mu.Unlock()
		if len(bad) != 0 {
			t.Fatalf("MONITOR-WIRING: serial: %v", bad)
		}
	})
}

// Claim (doc.go:36; spec :228-232): with an Append held in its journal write, a
// method queued on the session mutex AHEAD of a later Close completes before
// the Close: it succeeds and every one of its seam calls happens before the
// lock descriptor closes. The committed TestSessionQueuedBehindClose only
// covers the opposite order. Limit: catches a lock gap only if the gap lets
// the woken Close run (a scheduler yield inside the gap); a gap with no yield
// and no seam is not observable here.
func TestSessionQueuedAheadOfClose(t *testing.T) {
	for _, method := range []string{"Append", "ReplaceRecord", "CreateInput", "ReadInput"} {
		t.Run(method, func(t *testing.T) {
			_, e, s := sweepSetup(t)
			entered, release := make(chan struct{}), make(chan struct{})
			relOnce := false
			rel := func() {
				if !relOnce {
					relOnce = true
					close(release)
				}
			}
			defer rel()
			lock := s.lock
			var closed, held bool
			after := 0
			var hmu = make(chan struct{}, 1)
			hmu <- struct{}{}
			e.monitor.hook = func(v seamEvent) error {
				<-hmu
				if closed {
					after++
				}
				first := v.kind == "write" && !held
				if first {
					held = true
				}
				hmu <- struct{}{}
				if first {
					close(entered)
					select {
					case <-release:
					case <-time.After(5 * time.Second):
						t.Error("QUEUED-AHEAD: held writer expired")
					}
				}
				return nil
			}
			cl := e.calls.close
			e.calls.close = func(fd int) error {
				err := cl(fd)
				if fd == lock {
					<-hmu
					closed = true
					hmu <- struct{}{}
				}
				return err
			}
			first := make(chan error, 1)
			go func() { first <- sweepMethod(s, "Append", "holder") }()
			guardReceive(t, entered, "QUEUED-AHEAD holder")
			mids, mdone := sweepLaunch(func() error { return sweepMethod(s, method, "waiter") })
			if blocked, _ := sweepWaiting(t, guardReceive(t, mids, "QUEUED-AHEAD method id"), mdone); !blocked {
				t.Fatal("QUEUED-AHEAD setup: method did not queue")
			}
			cids, cdone := sweepLaunch(s.Close)
			if blocked, _ := sweepWaiting(t, guardReceive(t, cids, "QUEUED-AHEAD close id"), cdone); !blocked {
				t.Fatal("QUEUED-AHEAD setup: Close did not queue")
			}
			rel()
			must(t, guardReceive(t, first, "QUEUED-AHEAD holder result"))
			merr := guardReceive(t, mdone, "QUEUED-AHEAD method result")
			cerr := guardReceive(t, cdone, "QUEUED-AHEAD close result")
			e.monitor.hook = nil
			<-hmu
			n := after
			hmu <- struct{}{}
			if merr != nil || cerr != nil || n != 0 {
				t.Fatalf("QUEUED-AHEAD: %s queued ahead of Close: method=%v close=%v seam calls after the lock closed=%d", method, merr, cerr, n)
			}
		})
	}
}

// A helper's teardown request is retained even when the consumer ignores it.
// Both phases keep every descriptor, class and poison until callback return;
// panic unwinding also clears the mark before deferred entry teardown.
func TestCallbackFailureGuard(t *testing.T) {
	for _, phase := range []string{"Consume", "Finish"} {
		for _, kind := range []string{"control", "errno", "logical", "nil", "return-error", "repeated", "panic"} {
			t.Run(phase+"/"+kind, func(t *testing.T) {
				c, e, s := sweepSetup(t)
				must(t, s.Close())
				var failed *Session
				var cause error
				callback := func(r *Recovery) error {
					failed = r.s
					before := len(r.s.owned)
					lock := r.s.lock
					switch kind {
					case "errno":
						original := e.calls.stat
						e.calls.stat = func(int, *syscall.Stat_t) error { return syscall.EIO }
						_, cause = r.s.check(r.s.inputs, true)
						e.calls.stat = original
						cause = poison(cause)
					case "logical", "return-error", "repeated", "panic":
						_, cause = r.s.readInput("../journal", [32]byte{})
					}
					checkReturn := func(arg error) error {
						ret := r.s.fail(arg)
						wantClass, wantPoison := StorageWriteFailure, true
						if arg != nil {
							wantClass = ClassOf(arg)
							var je *Error
							wantPoison = errors.As(arg, &je) && je.Poisoned
						}
						var je *Error
						if ret == nil || ClassOf(ret) != wantClass || !errors.As(ret, &je) || je.Poisoned != wantPoison || (arg != nil && ret != arg) {
							t.Errorf("CALLBACK-RETURN: class, poison or identity changed: arg=%v ret=%v", arg, ret)
						}
						return ret
					}
					if kind == "nil" {
						cause = checkReturn(nil)
					} else if cause != nil {
						checkReturn(cause)
					}
					if kind == "repeated" {
						for i := 0; i < 8; i++ {
							checkReturn(failure(MissingState, "later", nil))
						}
						if r.s.callbackFailure != cause {
							t.Error("CALLBACK-RECORD: first cause replaced or grew")
						}
						checkReturn(poison(failure(StorageWriteFailure, "later sync", ErrSyncFailure)))
						checkReturn(poison(failure(StorageWriteFailure, "repeated sync", ErrSyncFailure)))
					}
					if len(r.s.owned) != before || r.s.lock != lock || r.s.state.Closed || r.s.terminal != nil {
						t.Errorf("CALLBACK-GUARD: %s teardown before callback return", phase)
					}
					sweepContender(t, c, OwnershipContended)
					if kind == "panic" {
						panic("guard sentinel")
					}
					if kind == "return-error" {
						return errors.New("consumer sentinel")
					}
					return nil
				}
				cons := consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error { return callback(r) }}
				if phase == "Finish" {
					cons = consumerFuncs{finish: callback}
				}
				var got *Session
				var err error
				var panicked any
				func() { defer func() { panicked = recover() }(); got, err = e.enter(c, nil, cons, false) }()
				if got != nil {
					must(t, got.Close())
				}
				if kind == "control" {
					if err != nil || got == nil {
						t.Fatalf("CALLBACK-RECORD: positive control: %v", err)
					}
				} else if kind == "panic" {
					if panicked != "guard sentinel" || !failed.state.Closed || failed.callbackRunning {
						t.Fatal("CALLBACK-GUARD: panic did not quiesce")
					}
				} else {
					if got != nil || ClassOf(err) != ClassOf(cause) || !failed.state.Closed {
						t.Errorf("CALLBACK-RECORD: ignored helper failure lost: entry=%v cause=%v", err, cause)
					}
					if kind == "repeated" && (!errors.Is(err, cause) || !errors.Is(err, ErrSyncFailure)) {
						t.Error("CALLBACK-RECORD: later sync sentinel lost")
					}
					var je *Error
					if !errors.As(err, &je) || je.Poisoned != (kind == "errno" || kind == "repeated" || kind == "nil") {
						t.Errorf("CALLBACK-RECORD: poison changed: %v", err)
					}
				}
				sweepContender(t, c, "")
			})
		}
	}
}
