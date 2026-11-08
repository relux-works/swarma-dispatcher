//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var nativeFlockCheck struct {
	once sync.Once
	err  error
}

// Check the composed production adapter before fixtures can reach synchronous
// contention paths. A blocking plant is unblocked by releasing our own holder;
// the cached failure then refuses every fixture instead of hanging older tests.
func verifyNativeFlock(t *testing.T) {
	t.Helper()
	nativeFlockCheck.once.Do(func() {
		path := filepath.Join(t.TempDir(), "flock-control")
		a, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC, 0600)
		if err != nil {
			nativeFlockCheck.err = err
			return
		}
		defer syscall.Close(a)
		b, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
		if err != nil {
			nativeFlockCheck.err = err
			return
		}
		defer syscall.Close(b)
		if err = syscall.Flock(a, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			nativeFlockCheck.err = err
			return
		}
		done := make(chan error, 1)
		go func() { done <- nativeCalls.flock(b, syscall.LOCK_EX|syscall.LOCK_NB) }()
		select {
		case err = <-done:
			if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
				nativeFlockCheck.err = fmt.Errorf("contention returned %v", err)
			}
		case <-time.After(3 * time.Second):
			nativeFlockCheck.err = fmt.Errorf("production adapter blocked on contention")
			_ = syscall.Flock(a, syscall.LOCK_UN)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				nativeFlockCheck.err = fmt.Errorf("adapter did not quiesce after holder release")
			}
		}
		_ = syscall.Flock(a, syscall.LOCK_UN)
		_ = syscall.Flock(b, syscall.LOCK_UN)
		if nativeFlockCheck.err == nil {
			nativeFlockCheck.err = nativeCalls.flock(b, syscall.LOCK_EX|syscall.LOCK_NB)
		}
	})
	if nativeFlockCheck.err != nil {
		t.Fatalf("FLOCK-ADAPTER: %v", nativeFlockCheck.err)
	}
}

// All channel waits used by the concurrency witnesses have their own diagnostic.
func guardReceive[T any](t *testing.T, ch <-chan T, assertion string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: bounded wait expired", assertion)
		var zero T
		return zero
	}
}

// Claims (spec 214,274-276): an already failed consumer budget wins before name
// validation or any input open/stat. Ordinary reads/refusals with a live budget
// form the paired controls; ignoring the operation error cannot publish recovery.
func TestGuardFailedBudgetReadInput(t *testing.T) {
	for _, name := range []string{"arg", "missing", "../journal"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failed=%v", name, failed), func(t *testing.T) {
				c, e := fixture(t, true)
				s := start(t, c, e)
				must(t, s.CreateInput("arg", []byte("abc")))
				must(t, s.Close())
				before := disk(t, c)
				active := false
				opens, stats := 0, 0
				op, st := e.calls.open, e.calls.stat
				e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
					if active {
						opens++
					}
					return op(d, n, f, m)
				}
				e.calls.stat = func(fd int, x *syscall.Stat_t) error {
					if active {
						stats++
					}
					return st(fd, x)
				}
				var opErr error
				var b []byte
				got, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error {
					if failed {
						wantClass(t, r.Budget.Charge(^uint64(0)), ConsumerFailure)
					}
					active = true
					b, opErr = r.ReadInput(name, sha256.Sum256([]byte("abc")))
					active = false
					if opErr == nil {
						must(t, r.Budget.Release(uint64(len(b))))
					}
					return nil
				}}, false)
				if got != nil {
					_ = got.Close()
				}
				if failed {
					if ClassOf(opErr) != ConsumerFailure || ClassOf(err) != ConsumerFailure || got != nil || b != nil || opens != 0 || stats != 0 {
						t.Fatalf("BUDGET-FAILED-READINPUT: op=%v entry=%v opens=%d stats=%d", opErr, err, opens, stats)
					}
				} else if name == "arg" {
					if err != nil || opErr != nil || string(b) != "abc" || got == nil || opens != 1 || stats != 2 {
						t.Fatalf("BUDGET-FAILED control: %v %v opens=%d stats=%d", opErr, err, opens, stats)
					}
				} else {
					want := MissingState
					if name == "../journal" {
						want = InvalidState
					}
					wantClass(t, opErr, want)
					wantClass(t, err, want)
				}
				if !bytes.Equal(before, disk(t, c)) {
					t.Fatal("BUDGET-FAILED-READINPUT: journal changed")
				}
			})
		}
	}
}

// Claims (doc 36; spec 228-232): every Session method waits behind a held
// Append. I/O seams also observe the mutex directly with TryLock, including a
// post-Append call, so a delayed method cannot hide its missing I/O lock.
// State has no seam: its return is observed during the held-write window.
func TestGuardMethodSerialization(t *testing.T) {
	for _, method := range []string{"Close", "ReplaceRecord", "CreateInput", "ReadInput", "State"} {
		t.Run(method, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			must(t, s.CreateInput("arg", []byte("abc")))
			var writing, unlocked, touched atomic.Bool
			check := func() {
				if writing.Load() {
					touched.Store(true)
				}
				if s.mu.TryLock() {
					unlocked.Store(true)
					s.mu.Unlock()
				}
			}
			op, rd, wr, st, rn, cl := e.calls.open, e.calls.read, e.calls.write, e.calls.stat, e.calls.rename, e.calls.close
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseWrite := func() { once.Do(func() { close(release) }) }
			defer releaseWrite()
			var first atomic.Bool
			e.calls.write = func(fd int, p []byte, o int64) (int, error) {
				if fd == s.journal && first.CompareAndSwap(false, true) {
					if s.mu.TryLock() {
						unlocked.Store(true)
						s.mu.Unlock()
					}
					writing.Store(true)
					close(entered)
					select {
					case <-release:
					case <-time.After(3 * time.Second):
						unlocked.Store(true)
					}
					writing.Store(false)
				} else {
					check()
				}
				return wr(fd, p, o)
			}
			e.calls.open = func(d int, n string, f int, m uint32) (int, error) { check(); return op(d, n, f, m) }
			e.calls.read = func(fd int, p []byte, o int64) (int, error) { check(); return rd(fd, p, o) }
			e.calls.stat = func(fd int, x *syscall.Stat_t) error { check(); return st(fd, x) }
			e.calls.rename = func(a int, b string, c int, d string) error { check(); return rn(a, b, c, d) }
			e.calls.close = func(fd int) error { check(); return cl(fd) }
			appendDone := make(chan error, 1)
			go func() { _, err := s.Append([]byte{'b'}); appendDone <- err }()
			guardReceive(t, entered, "METHOD-SERIAL setup")
			other := make(chan error, 1)
			started := make(chan struct{})
			invoke := func() error {
				switch method {
				case "Close":
					return s.Close()
				case "ReplaceRecord":
					return s.ReplaceRecord("r", []byte{'r'})
				case "CreateInput":
					return s.CreateInput("new", []byte{'i'})
				case "ReadInput":
					b, err := s.ReadInput("arg", sha256.Sum256([]byte("abc")))
					if err == nil && string(b) != "abc" {
						return fmt.Errorf("bad input")
					}
					return err
				default:
					if !s.State().Ready {
						return fmt.Errorf("not ready")
					}
					return nil
				}
			}
			go func() { close(started); other <- invoke() }()
			guardReceive(t, started, "METHOD-SERIAL start")
			early := false
			var otherErr error
			select {
			case otherErr = <-other:
				early = true
			case <-time.After(200 * time.Millisecond):
			}
			releaseWrite()
			appendErr := guardReceive(t, appendDone, "METHOD-SERIAL append")
			if !early {
				otherErr = guardReceive(t, other, "METHOD-SERIAL method")
			}
			// The operation completed successfully after the hold on the unmodified code.
			if early || unlocked.Load() || touched.Load() {
				t.Fatalf("METHOD-SERIAL: %s ran without serialization: early=%v unlocked=%v touched=%v", method, early, unlocked.Load(), touched.Load())
			}
			if appendErr != nil || otherErr != nil {
				t.Fatalf("METHOD-SERIAL control: append=%v method=%v", appendErr, otherErr)
			}
		})
	}
}

// Claim (106-110,228-232): Close cannot release ownership while an Append is
// in its write. After the writer and Close finish, a fresh owner can recover.
func TestGuardCloseDuringAppendOwnership(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseWrite := func() { once.Do(func() { close(release) }) }
	defer releaseWrite()
	wr := e.calls.write
	var first atomic.Bool
	e.calls.write = func(fd int, p []byte, o int64) (int, error) {
		if first.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-time.After(3 * time.Second):
				return 0, syscall.ETIMEDOUT
			}
		}
		return wr(fd, p, o)
	}
	type result struct {
		seq uint64
		err error
	}
	done := make(chan result, 1)
	go func() { seq, err := s.Append([]byte("in-flight")); done <- result{seq, err} }()
	guardReceive(t, entered, "CLOSE-DURING-APPEND setup")
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	early := false
	select {
	case <-closed:
		early = true
	case <-time.After(200 * time.Millisecond):
	}
	e2 := newFixtureEngine(t, nativeCalls)
	e2.calls.statfs = e.calls.statfs
	type entry struct {
		s   *Session
		err error
	}
	contender := make(chan entry, 1)
	go func() { x, err := e2.enter(c, nil, consumerFuncs{}, false); contender <- entry{x, err} }()
	r := guardReceive(t, contender, "CLOSE-DURING-APPEND contender")
	releaseWrite()
	a := guardReceive(t, done, "CLOSE-DURING-APPEND writer")
	if !early {
		guardReceive(t, closed, "CLOSE-DURING-APPEND close")
	}
	if r.s != nil {
		_ = r.s.Close()
	}
	if early || r.s != nil {
		t.Fatalf("CLOSE-DURING-APPEND: ownership released under writer: early=%v acquired=%v ack=%d err=%v", early, r.s != nil, a.seq, a.err)
	}
	if ClassOf(r.err) != OwnershipContended || a.seq != 2 || a.err != nil {
		t.Fatalf("CLOSE-DURING-APPEND control: contender=%v append=%+v", r.err, a)
	}
	next, err := e2.enter(c, nil, consumerFuncs{}, false)
	must(t, err)
	if next.State().Next != 3 {
		t.Fatal("CLOSE-DURING-APPEND: acknowledged frame absent")
	}
	must(t, next.Close())
}

// Claim (215,219-221): literal mutation errnos poison at every create site.
// Success at each site and the nonregular-entry tests are neighboring controls.
func TestGuardCreateErrnoClasses(t *testing.T) {
	t.Run("established-lock", guardEstablishedLockCreateErrnos)
	for _, at := range []string{"lock", "journal", "temp", "input"} {
		for _, errno := range []syscall.Errno{0, syscall.EIO, syscall.EROFS, syscall.EDQUOT, syscall.ENOSPC} {
			t.Run(fmt.Sprintf("%s/%d", at, errno), func(t *testing.T) {
				c, e := fixture(t, true)
				var s *Session
				if at == "temp" || at == "input" {
					s = start(t, c, e)
				}
				before := []byte(nil)
				if s != nil {
					before = disk(t, c)
				}
				hits := 0
				op := e.calls.open
				e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
					hit := f&syscall.O_CREAT != 0 && ((at == "lock" && n == "lock") || (at == "journal" && n == "journal") || (at == "temp" && strings.HasPrefix(n, ".record-tmp-")) || (at == "input" && n == "in"))
					if hit {
						hits++
						if errno != 0 {
							return -1, errno
						}
					}
					return op(d, n, f, m)
				}
				var err error
				switch at {
				case "lock", "journal":
					s, err = e.enter(c, []byte{'a'}, nil, true)
				case "temp":
					err = s.ReplaceRecord("r", []byte{'r'})
				case "input":
					err = s.CreateInput("in", []byte{'i'})
				}
				if errno == 0 {
					must(t, err)
					must(t, s.Close())
					if hits != 1 {
						t.Fatal("CREATE-ERRNO control")
					}
					return
				}
				cl, poisoned := guardErr(err)
				if cl != StorageWriteFailure || !poisoned || hits != 1 {
					t.Fatalf("CREATE-ERRNO: %s errno=%v class=%s poisoned=%v calls=%d", at, errno, cl, poisoned, hits)
				}
				if at == "temp" || at == "input" {
					ended(t, s, true)
					if !bytes.Equal(before, disk(t, c)) {
						t.Fatal("CREATE-ERRNO: history changed")
					}
				} else if s != nil {
					_ = s.Close()
					t.Fatal("CREATE-ERRNO: failed entry published session")
				}
			})
		}
	}
}

// Claim (doc 50-52; spec 274-276): a refused 1 MiB input allocates less than
// 512 KiB. The successful neighbor must visibly allocate at least 1 MiB.
// Only ReadInput is measured, after replay storage and fixture allocation.
func TestGuardInputChargeBeforeAllocation(t *testing.T) {
	for _, enough := range []bool{true, false} {
		t.Run(fmt.Sprint(enough), func(t *testing.T) {
			c, e := fixture(t, true)
			data := make([]byte, MaxPayload)
			sum := sha256.Sum256(data)
			s := start(t, c, e)
			must(t, s.CreateInput("big", data))
			must(t, s.Close())
			if !enough {
				c.ConsumerBudget = 4096
			}
			var delta uint64
			var opErr error
			var n int
			got, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error {
				var a, b runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&a)
				p, re := r.ReadInput("big", sum)
				runtime.ReadMemStats(&b)
				delta = b.TotalAlloc - a.TotalAlloc
				opErr = re
				n = len(p)
				if re == nil {
					must(t, r.Budget.Release(uint64(len(p))))
				}
				return nil
			}}, false)
			if got != nil {
				_ = got.Close()
			}
			if enough {
				if err != nil || opErr != nil || got == nil || n != MaxPayload || delta < MaxPayload {
					t.Fatalf("CHARGE-BEFORE-ALLOC control: err=%v op=%v size=%d delta=%d", err, opErr, n, delta)
				}
			} else {
				if ClassOf(err) != ConsumerFailure || ClassOf(opErr) != ConsumerFailure || got != nil || n != 0 || delta >= 512<<10 {
					t.Fatalf("CHARGE-BEFORE-ALLOC: err=%v op=%v bytes=%d allocated=%d", err, opErr, n, delta)
				}
			}
		})
	}
}
