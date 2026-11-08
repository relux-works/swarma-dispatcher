//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// These fault-mechanism witnesses use real descriptors with the filesystem type
// seam. They establish local ordering/refusal, not storage qualification.

// Claim (spec :25-26, :144-146, :351): a frame that ends exactly at the 64 MiB
// file cap is an admissible append, and only a frame that would pass the cap
// refuses journal-full cleanly. Limit: synthetic tracked EOF through fstat
// substitution on a real (sparse) file; no 64 MiB of payload is written.
// Control plant: O-cap-ge.
func TestGuardExactCapAppend(t *testing.T) {
	for _, n := range []int{1, MaxPayload} {
		c, e := fixture(t, true)
		s := start(t, c, e)
		native := e.calls.stat
		e.calls.stat = func(fd int, st *syscall.Stat_t) error {
			err := native(fd, st)
			if fd == s.journal {
				st.Size = s.state.EOF
			}
			return err
		}
		s.state.EOF = FileCap - int64(56+n)
		seq, err := s.Append(make([]byte, n))
		if err != nil || seq != 2 || s.State().EOF != FileCap || s.State().Next != 3 {
			t.Fatalf("FINDING exact-cap append N=%d refused or miscounted: seq=%d state=%+v err=%v", n, seq, s.State(), err)
		}
		before := s.State()
		_, err = s.Append([]byte{1})
		wantClass(t, err, JournalFull)
		if s.State() != before {
			t.Fatal("journal-full changed state")
		}
	}
}

// Claim (spec :152-154): positive progress resets the zero-transfer EINTR
// counter, so 3 EINTRs, progress, 3 EINTRs, completion succeeds in exactly 8
// calls; the negative neighbour (4 consecutive EINTRs after progress) fails
// poisoned after exactly 5 calls. Control plant: R56c.
func TestGuardEintrResetAfterProgress(t *testing.T) {
	type r struct {
		n   int
		err error
	}
	E := r{0, syscall.EINTR}
	for _, tc := range []struct {
		script []r
		ok     bool
	}{
		{[]r{E, E, E, {1, nil}, E, E, E, {4, nil}}, true},
		{[]r{{1, nil}, E, E, E, E}, false},
	} {
		e := &engine{}
		s := &Session{e: e}
		i := 0
		e.calls.write = func(_ int, p []byte, o int64) (int, error) {
			if i >= len(tc.script) {
				t.Fatal("write retried beyond script")
			}
			x := tc.script[i]
			i++
			return x.n, x.err
		}
		err := s.fullWrite(0, []byte("abcde"), 0)
		if tc.ok && (err != nil || i != len(tc.script)) {
			t.Fatalf("FINDING: EINTR counter not reset after progress: err=%v calls=%d", err, i)
		}
		if !tc.ok && (ClassOf(err) != StorageWriteFailure || i != len(tc.script)) {
			t.Fatalf("four consecutive EINTRs: err=%v calls=%d", err, i)
		}
	}
}

// Claim (spec :105-108): a contender opens only the state directory and lock,
// never records, inputs or journal, and refuses ownership-contended on both
// entries. Control: the owner's own acquisition reached all five names.
// Control plant: O-flock-after-journal.
func TestGuardContenderTouchesOnlyLock(t *testing.T) {
	c, e := fixture(t, true)
	var owner []string
	o := e.calls.open
	e.calls.open = func(d int, n string, f int, m uint32) (int, error) { owner = append(owner, n); return o(d, n, f, m) }
	s := start(t, c, e)
	if len(owner) != 5 {
		t.Fatalf("owner acquisition opened %v", owner)
	}
	for _, init := range []bool{false, true} {
		e2 := &engine{calls: e.calls}
		var names []string
		e2.calls.open = func(d int, n string, f int, m uint32) (int, error) { names = append(names, n); return o(d, n, f, m) }
		var err error
		if init {
			_, err = e2.enter(c, []byte{'a'}, nil, true)
		} else {
			_, err = e2.enter(c, nil, consumerFuncs{}, false)
		}
		if ClassOf(err) != OwnershipContended || len(names) != 2 || names[1] != "lock" {
			t.Errorf("FINDING contender init=%v: class=%s opens=%v", init, ClassOf(err), names)
		}
	}
	must(t, s.Close())
}

// Claim (spec :67-70, :209): the filesystem type gate runs before any state
// file is created; an unsupported type refuses unsupported-storage on both
// entries and leaves no lock. Control: a supported type proceeds.
// Control plant: O-statfs-after-lock.
func TestGuardTypeGateBeforeLock(t *testing.T) {
	c, e := fixture(t, true)
	e.calls.statfs = func(_ int, s *syscall.Statfs_t) error { s.Type = 0x01021994; return nil } // tmpfs magic
	for _, init := range []bool{true, false} {
		var err error
		if init {
			_, err = e.enter(c, []byte{'a'}, nil, true)
		} else {
			_, err = e.enter(c, nil, consumerFuncs{}, false)
		}
		wantClass(t, err, UnsupportedStorage)
		if _, err := os.Lstat(filepath.Join(c.Directory, "lock")); !os.IsNotExist(err) {
			t.Fatalf("FINDING: unsupported-storage refusal created the lock (init=%v)", init)
		}
	}
	e.calls.statfs = func(_ int, s *syscall.Statfs_t) error { s.Type = 0xEF53; return nil }
	s := start(t, c, e)
	must(t, s.Close())
}

// Claim (spec :252-253, carried decision B): after one session reports a sync
// failure, a clean Close of another live session in the same process does not
// clear the latch; a later entry still refuses with MustExit. Control: before
// the failure a second session opens normally. Control plant: O-latch-reset-on-close.
func TestGuardLatchSurvivesOtherClose(t *testing.T) {
	c1, e := fixture(t, true)
	c2, _ := fixture(t, true)
	c3, _ := fixture(t, true)
	s1 := start(t, c1, e)
	s2 := start(t, c2, e)
	sy := e.calls.sync
	e.calls.sync = func(int) error { return syscall.EIO }
	if _, err := s1.Append([]byte{'b'}); !MustExit(err) {
		t.Fatal("injected sync failure not reported")
	}
	e.calls.sync = sy
	_ = s2.Close()
	if got, err := e.enter(c3, []byte{'a'}, nil, true); got != nil || !MustExit(err) {
		t.Fatalf("FINDING: process latch cleared by another session's Close: session=%v err=%v", got != nil, err)
	}
}

// Claim (spec :299, :313-316, :217-223): an input read failure during replay is
// sticky even when the consumer ignores it: recovery ends with that class and
// publishes no session. Control: the same consumer with the correct digest
// recovers. Control plant: O-recovery-readerr-ignored.
func TestGuardRecoveryReadErrorSticky(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.CreateInput("arg", []byte("abc")))
	must(t, s.Close())
	for _, good := range []bool{true, false} {
		d := [32]byte{}
		if good {
			d = sha256.Sum256([]byte("abc"))
		}
		got, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error {
			b, _ := r.ReadInput("arg", d) // error deliberately ignored
			if b != nil {
				_ = r.Budget.Release(uint64(len(b)))
			}
			return nil
		}}, false)
		if good {
			must(t, err)
			must(t, got.Close())
			continue
		}
		if got != nil || ClassOf(err) != Corruption {
			if got != nil {
				_ = got.Close()
			}
			t.Fatalf("FINDING: ignored replay input failure published readiness: session=%v err=%v", got != nil, err)
		}
	}
}

// Claim (recovery doc in session_linux.go; spec :228-232): a failed recovery
// operation inside a consumer callback does not release ownership before the
// callback returns: a contender in the same callback still sees
// ownership-contended. Control plant: O-teardown-in-callback.
func TestGuardNoReleaseInsideCallback(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.Close())
	control, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, p []byte, r *Recovery) error { return r.ReplaceRecord("control", p) }}, false)
	must(t, err)
	must(t, control.Close())
	before := disk(t, c)
	e.calls.rename = func(int, string, int, string) error { return syscall.EIO }
	inside := Class("")
	got, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, p []byte, r *Recovery) error {
		_ = r.ReplaceRecord("cache", p) // fails: rename EIO
		e2 := &engine{calls: nativeCalls}
		e2.calls.statfs = e.calls.statfs
		other, cerr := e2.enter(c, nil, consumerFuncs{}, false)
		if other != nil {
			_ = other.Close()
		}
		inside = ClassOf(cerr)
		return nil
	}}, false)
	wantClass(t, err, StorageWriteFailure)
	if got != nil || !bytes.Equal(before, disk(t, c)) {
		t.Fatal("CALLBACK-OWNERSHIP: failure published or changed history")
	}
	if inside != OwnershipContended {
		t.Fatalf("FINDING: ownership released inside the consumer callback: contender class=%q", inside)
	}
}

// Claim (spec :72-74, :209, :215): fsync errno classes from an independent list,
// not from the production unsupported() helper: EINVAL, ENOLCK, ENOSYS and
// EOPNOTSUPP are unsupported-storage; EIO, EINTR and ENOSPC are
// storage-write-failure; all poison and require exit. Control plants:
// O-unsupported-einval, O-unsupported-eopnotsupp, O-unsupported-enolck.
func TestGuardSyncErrnoClasses(t *testing.T) {
	for errno, want := range map[syscall.Errno]Class{
		syscall.EINVAL: UnsupportedStorage, syscall.ENOLCK: UnsupportedStorage, syscall.ENOSYS: UnsupportedStorage, syscall.EOPNOTSUPP: UnsupportedStorage,
		syscall.EIO: StorageWriteFailure, syscall.EINTR: StorageWriteFailure, syscall.ENOSPC: StorageWriteFailure,
	} {
		c, e := fixture(t, true)
		s := start(t, c, e)
		e.calls.sync = func(int) error { return errno }
		_, err := s.Append([]byte{'b'})
		cl, poisoned := guardErr(err)
		if cl != want || !poisoned || !MustExit(err) {
			t.Errorf("FINDING: fsync %v: class=%s poisoned=%v mustExit=%v, want %s", errno, cl, poisoned, MustExit(err), want)
		}
	}
}

// Claim (spec :89-93): the journal descriptor is fstat-checked immediately after
// open, before any read. A hard-linked (nlink 2) journal whose bytes are corrupt
// must refuse invalid-state with zero journal reads (witness for row 36, the
// journal metadata gate, whose removal the committed suite does not detect).
func TestGuardJournalCheckBeforeRead(t *testing.T) {
	c, e := fixture(t, true)
	j := filepath.Join(c.Directory, "journal")
	put(t, j, []byte("SDJRNL01 corrupt"), 0600)
	must(t, os.Link(j, filepath.Join(c.Directory, "journal-alias")))
	reads := 0
	rd := e.calls.read
	e.calls.read = func(fd int, p []byte, o int64) (int, error) { reads++; return rd(fd, p, o) }
	_, err := e.enter(c, nil, consumerFuncs{}, false)
	if ClassOf(err) != InvalidState || reads != 0 {
		t.Fatalf("JOURNAL-CHECK: hardlinked corrupt journal: class=%q reads=%d, want invalid-state with 0 reads", ClassOf(err), reads)
	}
	// Positive control: the same bytes without the extra link reach replay.
	must(t, os.Remove(filepath.Join(c.Directory, "journal-alias")))
	_, err = e.enter(c, nil, consumerFuncs{}, false)
	wantClass(t, err, Corruption)
}

// Claim (spec :105-106): flock is taken on the CHECKED lock descriptor. An
// unsafe lock (mode 0640) refuses invalid-state with zero flock calls.
func TestGuardLockCheckedBeforeFlock(t *testing.T) {
	c, e := fixture(t, true)
	put(t, filepath.Join(c.Directory, "lock"), nil, 0640)
	flocks := 0
	fl := e.calls.flock
	e.calls.flock = func(fd, how int) error { flocks++; return fl(fd, how) }
	_, err := e.enter(c, []byte{'a'}, nil, true)
	if ClassOf(err) != InvalidState || flocks != 0 {
		t.Fatalf("LOCK-CHECK: unsafe lock: class=%q flock calls=%d, want invalid-state and 0", ClassOf(err), flocks)
	}
	must(t, os.Chmod(filepath.Join(c.Directory, "lock"), 0600))
	s := start(t, c, e)
	must(t, s.Close())
	if flocks != 1 {
		t.Fatal("LOCK-CHECK control")
	}
}

// Claim (spec :105-107): records are not accessed before ownership. Under
// contention the records and inputs directories are never opened.
func TestGuardNoRecordAccessBeforeFlock(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.Close())
	e.calls.flock = func(int, int) error { return syscall.EWOULDBLOCK }
	opened := map[string]int{}
	o := e.calls.open
	e.calls.open = func(d int, n string, f int, m uint32) (int, error) { opened[n]++; return o(d, n, f, m) }
	_, err := e.enter(c, []byte{'a'}, nil, true)
	wantClass(t, err, OwnershipContended)
	if opened["records"] != 0 || opened["inputs"] != 0 || opened["journal"] != 0 {
		t.Fatalf("PRE-FLOCK-ACCESS: contended acquisition opened %v", opened)
	}
}

// Claim: successful object operations release their per-call descriptors;
// a long session does not accumulate descriptors.
func TestGuardNoDescriptorGrowth(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.ReplaceRecord("r", []byte{'0'}))
	must(t, s.CreateInput("i0", []byte{'0'}))
	_, err := s.ReadInput("i0", sha256.Sum256([]byte{'0'}))
	must(t, err)
	base := openFDs(t)
	for i := 1; i <= 20; i++ {
		must(t, s.ReplaceRecord("r", []byte{byte(i)}))
		name := "i" + string(rune('a'+i))
		must(t, s.CreateInput(name, []byte{byte(i)}))
		_, err := s.ReadInput(name, sha256.Sum256([]byte{byte(i)}))
		must(t, err)
	}
	if now := openFDs(t); now != base {
		t.Fatalf("descriptor count grew from %d to %d over 20 object rounds", base, now)
	}
}

// Claim (spec :28-29, :324-328): an oversized input is refused before any
// size-proportional allocation.
func TestGuardInputSizeNoAlloc(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.CreateInput("arg", []byte("abc")))
	b, controlErr := s.ReadInput("arg", sha256.Sum256([]byte("abc")))
	must(t, controlErr)
	if string(b) != "abc" {
		t.Fatal("INPUT-ALLOC control")
	}
	st := e.calls.stat
	e.calls.stat = func(fd int, x *syscall.Stat_t) error {
		err := st(fd, x)
		if x.Size == 3 {
			x.Size = 64 << 20
		}
		return err
	}
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	_, err := s.ReadInput("arg", sha256.Sum256([]byte("abc")))
	runtime.ReadMemStats(&m1)
	wantClass(t, err, Corruption)
	if d := m1.TotalAlloc - m0.TotalAlloc; d >= 4<<20 {
		t.Fatalf("oversized input allocated %d bytes before refusal", d)
	}
}

// Claim: a FIFO planted at a record or input name is refused promptly as
// invalid-state (no indefinite block while holding ownership). On a block
// the test itself unblocks the opener (O_RDWR open of the
// FIFO from the test) and reports its own assertion, so a plant that drops
// O_NONBLOCK fails this named assertion instead of hanging into a timeout.
func TestGuardFIFOEntries(t *testing.T) {
	for _, where := range []string{"record", "input"} {
		t.Run(where, func(t *testing.T) {
			c, e := fixture(t, true)
			s, err := e.enter(c, []byte{'a'}, nil, true)
			must(t, err)
			must(t, s.ReplaceRecord("control", []byte("ok")))
			must(t, s.CreateInput("control", []byte("ok")))
			_, err = s.ReadInput("control", sha256.Sum256([]byte("ok")))
			must(t, err)
			dir := "records"
			if where == "input" {
				dir = "inputs"
			}
			fifo := filepath.Join(c.Directory, dir, "x")
			must(t, syscall.Mkfifo(fifo, 0600))
			done := make(chan error, 1)
			go func() {
				var err error
				if where == "record" {
					err = s.ReplaceRecord("x", []byte("r"))
				} else {
					_, err = s.ReadInput("x", sha256.Sum256(nil))
				}
				done <- err
			}()
			select {
			case err = <-done:
				_ = s.Close()
				wantClass(t, err, InvalidState)
			case <-time.After(3 * time.Second):
				fd, oerr := syscall.Open(fifo, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
				if oerr == nil {
					<-done
					syscall.Close(fd)
				}
				_ = s.Close()
				t.Fatalf("FIFO-BLOCK: %s FIFO open blocked >3s while holding ownership", where)
			}
		})
	}
}

func guardErr(err error) (Class, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Class, e.Poisoned
	}
	return "", false
}
func openFDs(t *testing.T) int {
	t.Helper()
	es, err := os.ReadDir("/proc/self/fd")
	must(t, err)
	return len(es)
}

// Claims (spec 141-150): the write seam observes the session mutex held; a
// concurrent Append cannot write until the held first write is released.
func TestGuardAppendSerialization(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	entered := make(chan struct{})
	release := make(chan struct{})
	second := make(chan struct{}, 1)
	var calls atomic.Int32
	var unlocked atomic.Bool
	wr := e.calls.write
	e.calls.write = func(fd int, p []byte, o int64) (int, error) {
		if s.mu.TryLock() {
			unlocked.Store(true)
			s.mu.Unlock()
		}
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		} else {
			second <- struct{}{}
		}
		return wr(fd, p, o)
	}
	type result struct {
		seq uint64
		err error
	}
	results := make(chan result, 2)
	go func() { q, err := s.Append([]byte{'b'}); results <- result{q, err} }()
	<-entered
	started := make(chan struct{})
	go func() { close(started); q, err := s.Append([]byte{'c'}); results <- result{q, err} }()
	<-started
	early := false
	select {
	case <-second:
		early = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	a, b := <-results, <-results
	if early || unlocked.Load() {
		t.Fatal("APPEND-SERIAL: write reached without exclusive session mutex")
	}
	if a.err != nil || b.err != nil || a.seq+b.seq != 5 || s.State().Next != 4 {
		t.Fatalf("APPEND-SERIAL control: %v %v %+v", a, b, s.State())
	}
}

// Claims (217-224,313-315): the original operation error cannot be erased by a
// valid later operation, and no record or recovered session is published.
func TestGuardRecoveryStickyAfterSuccess(t *testing.T) {
	for _, kind := range []string{"record", "input"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			must(t, s.CreateInput("arg", []byte("abc")))
			must(t, s.Close())
			good := consumerFuncs{consume: func(_ uint64, p []byte, r *Recovery) error {
				if kind == "record" {
					return r.ReplaceRecord("control", p)
				}
				b, err := r.ReadInput("arg", sha256.Sum256([]byte("abc")))
				if err == nil {
					must(t, r.Budget.Release(uint64(len(b))))
				}
				return err
			}}
			s, err := e.enter(c, nil, good, false)
			must(t, err)
			must(t, s.Close())
			before := disk(t, c)
			want := InvalidState
			if kind == "input" {
				want = MissingState
			}
			var first, second error
			got, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, p []byte, r *Recovery) error {
				if kind == "record" {
					first = r.ReplaceRecord("../escape", p)
					second = r.ReplaceRecord("after-failure", p)
				} else {
					_, first = r.ReadInput("missing", [32]byte{})
					_, second = r.ReadInput("arg", sha256.Sum256([]byte("abc")))
				}
				return nil
			}}, false)
			if got != nil {
				_ = got.Close()
			}
			if ClassOf(first) != want || ClassOf(second) != want || ClassOf(err) != want || got != nil {
				t.Fatalf("RECOVERY-STICKY %s: earlier failure erased: first=%v second=%v entry=%v", kind, first, second, err)
			}
			if !bytes.Equal(before, disk(t, c)) {
				t.Fatal("RECOVERY-STICKY journal changed")
			}
			if _, err := os.Stat(filepath.Join(c.Directory, "records", "after-failure")); !os.IsNotExist(err) {
				t.Fatal("RECOVERY-STICKY later record published")
			}
		})
	}
}

// Claims (141-143): a one-byte shrink poisons before any write; fitting append
// is the positive control. Original remaining bytes and counters are retained.
func TestGuardShrinkDrift(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	_, err := s.Append([]byte{'b'})
	must(t, err)
	state := s.State()
	must(t, os.Truncate(filepath.Join(c.Directory, "journal"), state.EOF-1))
	before := disk(t, c)
	writes := 0
	w := e.calls.write
	e.calls.write = func(fd int, p []byte, o int64) (int, error) { writes++; return w(fd, p, o) }
	_, err = s.Append([]byte{'c'})
	after := s.State()
	if ClassOf(err) != InvalidState || !after.Poisoned || !after.Closed || after.Ready || after.Next != state.Next || after.EOF != state.EOF || writes != 0 || !bytes.Equal(before, disk(t, c)) {
		t.Fatalf("SHRINK-DRIFT: class=%s state=%+v writes=%d", ClassOf(err), after, writes)
	}
}

// Claims (76-79): every owner bit refuses before opening; other declared masks
// proceed. The declaration is not a change to the real process umask.
func TestGuardDeclaredOwnerBits(t *testing.T) {
	for _, mask := range []uint32{0400, 0200, 0600, 0, 0077, 0177} {
		t.Run(fmt.Sprintf("%o", mask), func(t *testing.T) {
			c, e := fixture(t, true)
			c.DeclaredUmask = &mask
			opens := 0
			o := e.calls.open
			e.calls.open = func(d int, n string, f int, m uint32) (int, error) { opens++; return o(d, n, f, m) }
			s, err := e.enter(c, []byte{'a'}, nil, true)
			if mask&0600 != 0 {
				if s != nil {
					_ = s.Close()
				}
				if ClassOf(err) != InvalidState || opens != 0 {
					t.Fatalf("UMASK-BITS: mask %o class=%s opens=%d", mask, ClassOf(err), opens)
				}
			} else {
				must(t, err)
				if opens == 0 {
					t.Fatal("UMASK-BITS control no opens")
				}
				must(t, s.Close())
			}
		})
	}
}

// Claim (57-59): invalid input names cause zero opens, even with a digest that
// matches a real outside file; a valid input is read successfully first.
func TestGuardReadInputNames(t *testing.T) {
	for _, name := range []string{"../journal", "../lock", "/journal", ".", "..", "arg/../arg", ""} {
		t.Run(name, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			must(t, s.CreateInput("arg", []byte("abc")))
			b, err := s.ReadInput("arg", sha256.Sum256([]byte("abc")))
			must(t, err)
			if string(b) != "abc" {
				t.Fatal("INPUT-NAME control")
			}
			opens := 0
			o := e.calls.open
			e.calls.open = func(d int, n string, f int, m uint32) (int, error) { opens++; return o(d, n, f, m) }
			b, err = s.ReadInput(name, sha256.Sum256(disk(t, c)))
			if ClassOf(err) != InvalidState || b != nil || opens != 0 {
				t.Fatalf("INPUT-NAME: name=%q class=%s opens=%d", name, ClassOf(err), opens)
			}
		})
	}
}

// Claims (54-55): non-clean and dot root paths refuse before ANY open. Clean
// real roots proceed; a symlink with a slash/dot suffix cannot bypass nofollow.
func TestGuardRootPaths(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.Close())
	link := filepath.Join(t.TempDir(), "link")
	must(t, os.Symlink(c.Directory, link))
	for _, path := range []string{link + "/", link + "/.", link + "/./", c.Directory + "/", c.Directory + "/.", ".", "..", c.Directory + "/.."} {
		cc := c
		cc.Directory = path
		opens := 0
		o := e.calls.open
		e.calls.open = func(d int, n string, f int, m uint32) (int, error) { opens++; return o(d, n, f, m) }
		got, err := e.enter(cc, nil, consumerFuncs{}, false)
		e.calls.open = o
		if got != nil {
			_ = got.Close()
		}
		if ClassOf(err) != InvalidState || opens != 0 {
			t.Fatalf("ROOT-PATH: class=%s opens=%d path=%q", ClassOf(err), opens, path)
		}
	}
}

// Claims (91-93,211,219-222): nonregular lock/journal objects refuse without
// poison or replacing their inode. Regular initialization/recovery is a control.
func TestGuardNonregularEntries(t *testing.T) {
	for _, name := range []string{"lock", "journal"} {
		for _, kind := range []string{"directory", "socket", "fifo"} {
			t.Run(name+"-"+kind, func(t *testing.T) {
				c, e := fixture(t, true)
				s := start(t, c, e)
				must(t, s.Close())
				path := filepath.Join(c.Directory, name)
				must(t, os.Remove(path))
				switch kind {
				case "directory":
					must(t, os.Mkdir(path, 0700))
				case "fifo":
					must(t, syscall.Mkfifo(path, 0600))
				case "socket":
					// Relative bind avoids the Unix socket path-length limit on long TMPDIRs.
					wd, err := os.Getwd()
					must(t, err)
					must(t, os.Chdir(c.Directory))
					l, err := net.Listen("unix", name)
					must(t, os.Chdir(wd))
					must(t, err)
					defer l.Close()
				}
				before, err := os.Lstat(path)
				must(t, err)
				got, err := e.enter(c, nil, consumerFuncs{}, false)
				if got != nil {
					_ = got.Close()
				}
				cl, p := guardErr(err)
				after, se := os.Lstat(path)
				must(t, se)
				if cl != InvalidState || p || got != nil || !os.SameFile(before, after) {
					t.Fatalf("NONREGULAR: %s class=%s poisoned=%v", kind, cl, p)
				}
			})
		}
	}
}

// Claim (106-110): neither descendant may alias root. Stat substitution models
// a bind alias without mount privilege; different dev OR inode remains valid.
func TestGuardRootAliases(t *testing.T) {
	for _, name := range []string{"records", "inputs"} {
		for _, variant := range []string{"equal", "different-dev", "different-ino"} {
			t.Run(name+"-"+variant, func(t *testing.T) {
				c, e := fixture(t, true)
				var root syscall.Stat_t
				must(t, syscall.Stat(c.Directory, &root))
				target := -1
				o := e.calls.open
				e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
					fd, err := o(d, n, f, m)
					if n == name {
						target = fd
					}
					return fd, err
				}
				st := e.calls.stat
				e.calls.stat = func(fd int, x *syscall.Stat_t) error {
					err := st(fd, x)
					if fd == target {
						x.Dev = root.Dev
						x.Ino = root.Ino
						if variant == "different-dev" {
							x.Dev++
						}
						if variant == "different-ino" {
							x.Ino ^= uint64(1) << 63
						}
					}
					return err
				}
				s, err := e.enter(c, []byte{'a'}, nil, true)
				if variant == "equal" {
					if s != nil {
						_ = s.Close()
					}
					if ClassOf(err) != InvalidState || s != nil {
						t.Fatalf("ROOT-ALIAS: %s accepted root identity", name)
					}
				} else {
					must(t, err)
					must(t, s.Close())
				}
			})
		}
	}
}

// Claim (274-276): exactly st.Size is charged before read/allocation. A 3-byte
// input fits 4096 bytes; with only 2 bytes left it returns nothing and reads none.
func TestGuardInputBudgetOrder(t *testing.T) {
	for _, enough := range []bool{true, false} {
		t.Run(fmt.Sprint(enough), func(t *testing.T) {
			c, e := fixture(t, true)
			c.ConsumerBudget = 4096
			s := start(t, c, e)
			must(t, s.CreateInput("arg", []byte("abc")))
			must(t, s.Close())
			var budget *Budget
			reads := 0
			rd := e.calls.read
			e.calls.read = func(fd int, p []byte, o int64) (int, error) {
				if budget != nil {
					reads++
					if budget.Used() != 3 {
						t.Errorf("INPUT-BUDGET-ORDER: read before exact charge: %d", budget.Used())
					}
				}
				return rd(fd, p, o)
			}
			got, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error {
				if !enough {
					must(t, r.Budget.Charge(4094))
				}
				budget = r.Budget
				b, err := r.ReadInput("arg", sha256.Sum256([]byte("abc")))
				budget = nil
				if enough {
					if err != nil || string(b) != "abc" || r.Budget.Used() != 3 {
						t.Errorf("INPUT-BUDGET-SIZE: bytes=%q used=%d err=%v", b, r.Budget.Used(), err)
					}
				} else {
					if ClassOf(err) != ConsumerFailure || b != nil || reads != 0 {
						t.Errorf("INPUT-BUDGET-ORDER: insufficient budget class=%s reads=%d", ClassOf(err), reads)
					}
				}
				return nil
			}}, false)
			if enough {
				must(t, err)
				must(t, got.Close())
				if reads == 0 {
					t.Fatal("INPUT-BUDGET control no read")
				}
			} else {
				if got != nil {
					_ = got.Close()
				}
				if ClassOf(err) != ConsumerFailure || got != nil {
					t.Fatal("INPUT-BUDGET-ORDER: failure published session")
				}
			}
		})
	}
}

// Claim (179-180): both nonce and counter use lowercase hex, exercised with
// a-f digits deterministically. Rename failure retains the exact temporary.
func TestGuardTempSpelling(t *testing.T) {
	c, e := fixture(t, true)
	e.calls.random = func(p []byte) (int, error) {
		for i := range p {
			p[i] = 0xab
		}
		return len(p), nil
	}
	s := start(t, c, e)
	must(t, s.ReplaceRecord("control", []byte{'a'}))
	s.counter = 9
	e.calls.rename = func(int, string, int, string) error { return syscall.EIO }
	wantClass(t, s.ReplaceRecord("record", []byte{'b'}), StorageWriteFailure)
	entries, err := os.ReadDir(filepath.Join(c.Directory, "records"))
	must(t, err)
	want := ".record-tmp-" + strings.Repeat("ab", 16) + "-000000000000000a"
	found := false
	for _, entry := range entries {
		if entry.Name() == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("TEMP-SPELLING: missing exact lowercase name %s", want)
	}
}

// Claim (67-74): both literal accepted types proceed; tmpfs refuses. This is
// a compatibility-seam witness and does not qualify actual XFS durability.
func TestGuardXFSType(t *testing.T) {
	for _, typ := range []int64{0xEF53, 0x58465342, 0x01021994} {
		c, e := fixture(t, true)
		e.calls.statfs = func(_ int, st *syscall.Statfs_t) error { st.Type = typ; return nil }
		s, err := e.enter(c, []byte{'a'}, nil, true)
		if typ == 0x01021994 {
			if ClassOf(err) != UnsupportedStorage || s != nil {
				t.Fatal("FS-TYPE refusal")
			}
		} else {
			if err != nil || s == nil {
				t.Fatalf("XFS-TYPE: accepted magic %x refused: %v", typ, err)
			}
			must(t, s.Close())
		}
	}
}

// Claim (105-107): real flock contention returns within seconds. The test
// releases its first owner on timeout, so a blocking plant reports an assertion.
func TestGuardFlockNonblocking(t *testing.T) {
	c, e := fixture(t, true)
	e.calls.flock = syscall.Flock
	s, err := e.enter(c, []byte{'a'}, nil, true)
	must(t, err)
	type result struct {
		s   *Session
		err error
	}
	done := make(chan result, 1)
	go func() { x, err := e.enter(c, nil, consumerFuncs{}, false); done <- result{x, err} }()
	blocked := false
	var r result
	select {
	case r = <-done:
	case <-time.After(3 * time.Second):
		blocked = true
		must(t, s.Close())
		select {
		case r = <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("FLOCK-BLOCK: did not return after owner release")
		}
	}
	must(t, s.Close())
	if r.s != nil {
		_ = r.s.Close()
	}
	if blocked {
		t.Fatal("FLOCK-BLOCK: contender exceeded three seconds")
	}
	if ClassOf(r.err) != OwnershipContended {
		t.Fatalf("FLOCK-BLOCK: class=%s", ClassOf(r.err))
	}
	x, err := e.enter(c, nil, consumerFuncs{}, false)
	must(t, err)
	must(t, x.Close())
}
