//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func sweepSetup(t *testing.T) (Config, *fixtureEngine, *Session) {
	t.Helper()
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.CreateInput("arg", []byte("abc")))
	must(t, s.ReplaceRecord("record", []byte("old")))
	return c, e, s
}
func sweepContender(t *testing.T, c Config, want Class) {
	t.Helper()
	e := newFixtureEngine(t, nativeCalls)
	e.calls.statfs = func(_ int, st *syscall.Statfs_t) error { st.Type = 0xEF53; return nil }
	s, err := e.enter(c, nil, consumerFuncs{}, false) // every fixture flock has a bounded adapter
	if s != nil {
		_ = s.Close()
	}
	if ClassOf(err) != want {
		t.Errorf("SWEEP-OWNERSHIP: contender=%s want=%s acquired=%v", ClassOf(err), want, s != nil)
	}
}
func seamFailureClass(v seamEvent) Class {
	switch v.kind {
	case "write", "sync", "rename", "close":
		return StorageWriteFailure
	case "open":
		if v.flags&syscall.O_CREAT != 0 {
			return StorageWriteFailure
		}
	}
	return StorageReadFailure
}

// Claims (spec 217-218,228-232): every seam in a successful local recovery
// operation is failed once in Consume and once in Finish. Logical refusals add
// paths absent from the success trace. All runs check an in-callback contender,
// terminal class, unchanged journal and a fresh owner's successful recovery.
func TestRecoveryFailureSweep(t *testing.T) {
	for _, phase := range []string{"Consume", "Finish"} {
		for _, op := range []string{"ReplaceRecord", "ReadInput"} {
			run := func(t *testing.T, k int, logical string) []seamEvent {
				c, e, s := sweepSetup(t)
				must(t, s.Close())
				history := disk(t, c)
				active := false
				trace := []seamEvent{}
				var injected seamEvent
				e.monitor.hook = func(v seamEvent) error {
					if active {
						trace = append(trace, v)
						if len(trace) == k {
							injected = v
							return syscall.EIO
						}
					}
					return nil
				}
				stat := e.calls.stat
				e.calls.stat = func(fd int, st *syscall.Stat_t) error {
					err := stat(fd, st)
					if active && err == nil && st.Mode&syscall.S_IFMT == syscall.S_IFREG {
						if logical == "protection" {
							st.Mode = st.Mode&^0777 | 0640
						}
						if logical == "size" && op == "ReadInput" {
							st.Size = MaxPayload + 1
						}
					}
					return err
				}
				want := Class("")
				var operation error
				called := 0
				callback := func(_ uint64, _ []byte, r *Recovery) error {
					called++
					name := "record"
					if op == "ReadInput" {
						name = "arg"
					}
					data := []byte("new")
					sum := sha256.Sum256([]byte("abc"))
					invoke := func() error {
						if op == "ReplaceRecord" {
							return r.ReplaceRecord(name, data)
						}
						b, err := r.ReadInput(name, sum)
						if err == nil {
							must(t, r.Budget.Release(uint64(len(b))))
						}
						return err
					}
					switch logical {
					case "name":
						name = "../journal"
						want = InvalidState
					case "protection":
						want = InvalidState
					case "size":
						if op == "ReadInput" {
							want = Corruption
						} else {
							data = make([]byte, MaxPayload+1)
							want = InvalidState
						}
					case "digest":
						sum = [32]byte{}
						want = Corruption
					case "exact-budget":
						must(t, r.Budget.Charge(r.Budget.limit))
						want = ConsumerFailure
					case "failed-budget":
						wantClass(t, r.Budget.Charge(^uint64(0)), ConsumerFailure)
						want = ConsumerFailure
					case "sticky":
						name = "../journal"
						active = true
						wantClass(t, invoke(), InvalidState)
						active = false
						name = "arg"
						if op == "ReplaceRecord" {
							name = "record"
						}
						want = InvalidState
					}
					active = true
					operation = invoke()
					active = false
					if k > 0 {
						want = seamFailureClass(injected)
					}
					sweepContender(t, c, OwnershipContended)
					return nil // deliberately ignore failure; recovery must keep it
				}
				cons := consumerFuncs{consume: callback}
				if phase == "Finish" {
					cons = consumerFuncs{finish: func(r *Recovery) error { return callback(1, nil, r) }}
				}
				got, err := e.enter(c, nil, cons, false)
				e.monitor.hook = nil
				if got != nil {
					must(t, got.Close())
				}
				if called != 1 || (k > 0 && len(trace) < k) || ClassOf(operation) != want || ClassOf(err) != want || ((got != nil) != (want == "")) {
					t.Fatalf("RECOVERY-SWEEP: phase=%s op=%s k=%d logical=%s callbacks=%d trace=%d op-error=%v entry=%v want=%s", phase, op, k, logical, called, len(trace), operation, err, want)
				}
				if !bytes.Equal(history, disk(t, c)) {
					t.Fatal("RECOVERY-SWEEP: history changed")
				}
				sweepContender(t, c, "") // independent engine models a new owning process after disposition
				return trace
			}
			var baseline []seamEvent
			t.Run(phase+"/"+op+"/control", func(t *testing.T) {
				baseline = run(t, 0, "")
				if len(baseline) == 0 {
					t.Fatal("RECOVERY-SWEEP: empty success trace")
				}
			})
			for i, v := range baseline {
				t.Run(fmt.Sprintf("%s/%s/%d-%s", phase, op, i+1, v.kind), func(t *testing.T) { run(t, i+1, "") })
			}
			cases := []string{"name", "protection", "size", "failed-budget", "sticky"}
			if op == "ReadInput" {
				cases = append(cases, "digest", "exact-budget")
			}
			for _, logical := range cases {
				t.Run(phase+"/"+op+"/"+logical, func(t *testing.T) { run(t, 0, logical) })
			}
		}
	}
}

func sweepMethod(s *Session, method, suffix string) error {
	switch method {
	case "Append":
		_, err := s.Append([]byte("next"))
		return err
	case "ReplaceRecord":
		return s.ReplaceRecord("record", []byte(suffix))
	case "CreateInput":
		return s.CreateInput("input-"+suffix, []byte("arg"))
	case "ReadInput":
		_, err := s.ReadInput("arg", sha256.Sum256([]byte("abc")))
		return err
	case "Close":
		return s.Close()
	case "State":
		_ = s.State()
		return nil
	}
	panic(method)
}

// Wait for the actual goroutine to reach Mutex.Lock, rather than assuming a
// sleep queued it. A method returning early is separately observable. Runtime
// stack spelling is checked explicitly and fails boundedly if it changes.
func sweepLaunch(fn func() error) (<-chan uint64, <-chan error) {
	ids := make(chan uint64, 1)
	done := make(chan error, 1)
	go func() {
		var b [80]byte
		n := runtime.Stack(b[:], false)
		var id uint64
		_, _ = fmt.Sscanf(string(b[:n]), "goroutine %d ", &id)
		ids <- id
		done <- fn()
	}()
	return ids, done
}
func sweepWaiting(t *testing.T, id uint64, done <-chan error) (bool, error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	buf := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return false, err
		default:
		}
		n := runtime.Stack(buf, true)
		prefix := fmt.Sprintf("goroutine %d [", id)
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.HasPrefix(stack, prefix) && strings.Contains(stack, "Mutex") && strings.Contains(stack, "Lock") {
				return true, nil
			}
		}
		runtime.Gosched()
	}
	t.Fatal("SWEEP-QUEUE: goroutine neither blocked on mutex nor returned within two seconds")
	return false, nil
}

// Claims (doc 36; spec 228-232): count every successful method seam, then hold
// each one before its syscall while every other Session method queues. Includes
// sync, rename and teardown. The paired fault sweep exercises error teardown.
func TestSessionSeamSweep(t *testing.T) {
	methods := []string{"Append", "ReplaceRecord", "CreateInput", "ReadInput", "Close"}
	for _, method := range methods {
		var baseline []seamEvent
		t.Run(method+"/control", func(t *testing.T) {
			_, e, s := sweepSetup(t)
			e.monitor.hook = func(v seamEvent) error { baseline = append(baseline, v); return nil }
			must(t, sweepMethod(s, method, "holder"))
			e.monitor.hook = nil
			if len(baseline) == 0 {
				t.Fatal("SESSION-SWEEP: empty success trace")
			}
		})
		for i, v := range baseline {
			t.Run(fmt.Sprintf("%s/fail/%d-%s", method, i+1, v.kind), func(t *testing.T) {
				_, e, s := sweepSetup(t)
				n := 0
				e.monitor.hook = func(v seamEvent) error {
					n++
					if n == i+1 {
						return syscall.EIO
					}
					return nil
				}
				err := sweepMethod(s, method, "holder")
				e.monitor.hook = nil
				if ClassOf(err) != seamFailureClass(v) || !s.State().Closed {
					t.Fatalf("SESSION-SWEEP: failure k=%d class=%s closed=%v", i+1, ClassOf(err), s.State().Closed)
				}
			})
			for _, other := range append(append([]string{}, methods...), "State") {
				t.Run(fmt.Sprintf("%s/hold/%d-%s/%s", method, i+1, v.kind, other), func(t *testing.T) {
					c, e, s := sweepSetup(t)
					entered, release := make(chan struct{}), make(chan struct{})
					var once sync.Once
					rel := func() { once.Do(func() { close(release) }) }
					defer rel()
					var held, touched atomic.Bool
					var count atomic.Int32
					e.monitor.hook = func(v seamEvent) error {
						if held.Load() {
							touched.Store(true)
						}
						if count.Add(1) == int32(i+1) {
							held.Store(true)
							close(entered)
							select {
							case <-release:
							case <-time.After(5 * time.Second):
								t.Error("SESSION-SWEEP: hold expired")
							}
							held.Store(false)
						}
						return nil
					}
					first := make(chan error, 1)
					go func() { first <- sweepMethod(s, method, "holder") }()
					guardReceive(t, entered, "SESSION-SWEEP reached seam")
					ids, done := sweepLaunch(func() error { return sweepMethod(s, other, "waiter") })
					blocked, earlyErr := sweepWaiting(t, guardReceive(t, ids, "SESSION-SWEEP id"), done)
					sweepContender(t, c, OwnershipContended)
					rel()
					firstErr := guardReceive(t, first, "SESSION-SWEEP holder")
					secondErr := earlyErr
					if blocked {
						secondErr = guardReceive(t, done, "SESSION-SWEEP waiter")
					}
					e.monitor.hook = nil
					if !blocked || touched.Load() {
						t.Errorf("SESSION-SWEEP: %s ran during %s seam %d-%s", other, method, i+1, v.kind)
					}
					if firstErr != nil {
						t.Fatalf("SESSION-SWEEP control: holder=%v", firstErr)
					}
					want := Class("")
					if method == "Close" && other != "Close" && other != "State" {
						want = InvalidState
					}
					if ClassOf(secondErr) != want {
						t.Fatalf("SESSION-SWEEP control: waiter=%v want=%s", secondErr, want)
					}
				})
			}
		}
	}
}

// Claim (spec 217-218): the first error beats a later failed budget in either
// Recovery method and either callback phase, including the sync-failure marker.
func TestRecoveryStickyBudgetOrder(t *testing.T) {
	for _, phase := range []string{"Consume", "Finish"} {
		for _, op := range []string{"ReadInput", "ReplaceRecord"} {
			for _, cause := range []string{"none", "missing", "invalid", "sync"} {
				t.Run(phase+"/"+op+"/"+cause, func(t *testing.T) {
					c, e, s := sweepSetup(t)
					must(t, s.Close())
					var first, second error
					want := ConsumerFailure
					if cause == "missing" {
						want = MissingState
					}
					if cause == "invalid" {
						want = InvalidState
					}
					if cause == "sync" {
						want = StorageWriteFailure
					}
					cb := func(_ uint64, _ []byte, r *Recovery) error {
						switch cause {
						case "missing":
							_, first = r.ReadInput("missing", [32]byte{})
						case "invalid":
							first = r.ReplaceRecord("../bad", nil)
						case "sync":
							e.monitor.hook = func(v seamEvent) error {
								if v.kind == "sync" {
									return syscall.EIO
								}
								return nil
							}
							first = r.ReplaceRecord("record", []byte("new"))
							e.monitor.hook = nil
						}
						if cause != "none" {
							wantClass(t, first, want)
						}
						wantClass(t, r.Budget.Charge(^uint64(0)), ConsumerFailure)
						if op == "ReadInput" {
							_, second = r.ReadInput("arg", sha256.Sum256([]byte("abc")))
						} else {
							second = r.ReplaceRecord("record", []byte("next"))
						}
						return nil
					}
					cons := consumerFuncs{consume: cb}
					if phase == "Finish" {
						cons = consumerFuncs{finish: func(r *Recovery) error { return cb(1, nil, r) }}
					}
					got, err := e.enter(c, nil, cons, false)
					if got != nil {
						_ = got.Close()
					}
					if got != nil || ClassOf(second) != want || ClassOf(err) != want || (cause != "none" && (!errors.Is(second, first) || !errors.Is(err, first))) || (cause == "sync" && (!errors.Is(second, ErrSyncFailure) || !errors.Is(err, ErrSyncFailure))) {
						t.Fatalf("STICKY-BUDGET: first=%v second=%v entry=%v want=%s", first, second, err, want)
					}
				})
			}
		}
	}
}

// Claim (spec 228-232): a method already queued behind Close refuses without
// any I/O after Close. The held Append is the live-session success control.
func TestSessionQueuedBehindClose(t *testing.T) {
	for _, method := range []string{"Append", "ReplaceRecord", "CreateInput", "ReadInput"} {
		t.Run(method, func(t *testing.T) {
			_, e, s := sweepSetup(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			rel := func() { once.Do(func() { close(release) }) }
			defer rel()
			lock := s.lock
			var closed atomic.Bool
			var after atomic.Int32
			var hold atomic.Bool
			e.monitor.hook = func(v seamEvent) error {
				if closed.Load() {
					after.Add(1)
				}
				if v.kind == "write" && hold.CompareAndSwap(false, true) {
					close(entered)
					select {
					case <-release:
					case <-time.After(5 * time.Second):
						t.Error("QUEUED-CLOSE: held writer expired")
					}
				}
				return nil
			}
			cl := e.calls.close
			e.calls.close = func(fd int) error {
				err := cl(fd)
				if fd == lock {
					closed.Store(true)
				}
				return err
			}
			first := make(chan error, 1)
			go func() { first <- sweepMethod(s, "Append", "holder") }()
			guardReceive(t, entered, "QUEUED-CLOSE holder")
			ids, done := sweepLaunch(s.Close)
			if blocked, _ := sweepWaiting(t, guardReceive(t, ids, "QUEUED-CLOSE close id"), done); !blocked {
				t.Fatal("QUEUED-CLOSE: Close did not queue")
			}
			mids, mdone := sweepLaunch(func() error { return sweepMethod(s, method, "waiter") })
			if blocked, _ := sweepWaiting(t, guardReceive(t, mids, "QUEUED-CLOSE method id"), mdone); !blocked {
				t.Fatal("QUEUED-CLOSE: method did not queue")
			}
			rel()
			must(t, guardReceive(t, first, "QUEUED-CLOSE holder result"))
			must(t, guardReceive(t, done, "QUEUED-CLOSE close result"))
			err := guardReceive(t, mdone, "QUEUED-CLOSE method result")
			e.monitor.hook = nil
			if ClassOf(err) != InvalidState || after.Load() != 0 {
				t.Fatalf("QUEUED-CLOSE: %s err=%v I/O after Close=%d", method, err, after.Load())
			}
		})
	}
}

// The established entry may need to create its lock too (spec 215). Literal
// mutation errnos retain poison; the zero-errno neighbor recovers successfully.
func guardEstablishedLockCreateErrnos(t *testing.T) {
	for _, errno := range []syscall.Errno{0, syscall.EIO, syscall.EROFS, syscall.EDQUOT, syscall.ENOSPC} {
		t.Run(fmt.Sprint(errno), func(t *testing.T) {
			c, e, s := sweepSetup(t)
			must(t, s.Close())
			must(t, os.Remove(filepath.Join(c.Directory, "lock")))
			hits := 0
			op := e.calls.open
			e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
				if n == "lock" && f&syscall.O_CREAT != 0 {
					hits++
					if errno != 0 {
						return -1, errno
					}
				}
				return op(d, n, f, m)
			}
			got, err := e.enter(c, nil, consumerFuncs{}, false)
			if errno == 0 {
				must(t, err)
				must(t, got.Close())
			} else {
				cl, p := guardErr(err)
				if cl != StorageWriteFailure || !p || got != nil {
					t.Fatalf("ESTABLISHED-CREATE: class=%s poison=%v session=%v", cl, p, got != nil)
				}
			}
			if hits != 1 {
				t.Fatal("ESTABLISHED-CREATE: create not exercised")
			}
		})
	}
}
