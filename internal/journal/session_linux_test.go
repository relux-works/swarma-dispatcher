//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type consumerFuncs struct {
	consume func(uint64, []byte, *Recovery) error
	finish  func(*Recovery) error
}

func (c consumerFuncs) Consume(n uint64, p []byte, r *Recovery) error {
	if c.consume != nil {
		return c.consume(n, p, r)
	}
	return nil
}
func (c consumerFuncs) Finish(r *Recovery) error {
	if c.finish != nil {
		return c.finish(r)
	}
	return nil
}
func fixture(t *testing.T, mechanism bool) (Config, *engine) {
	t.Helper()
	d := t.TempDir()
	must(t, os.Chmod(d, 0700))
	for _, n := range []string{"records", "inputs"} {
		must(t, os.Mkdir(filepath.Join(d, n), 0700))
		must(t, os.Chmod(filepath.Join(d, n), 0700))
	}
	m := uint32(0077)
	c := Config{Directory: d, Records: "records", Inputs: "inputs", DeclaredUmask: &m, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 4 * MaxPayload}
	e := &engine{calls: nativeCalls}
	if mechanism {
		// Fault-mechanism tests deliberately substitute only the type result. They
		// exercise real descriptors but do not establish supported storage/durability.
		e.calls.statfs = func(_ int, s *syscall.Statfs_t) error { s.Type = 0xEF53; return nil }
	} else {
		var fs syscall.Statfs_t
		must(t, syscall.Statfs(d, &fs))
		if fs.Type != 0xEF53 && fs.Type != 0x58465342 {
			t.Skipf("real storage UNRUN: fixture filesystem type %#x is neither ext4 nor XFS", fs.Type)
		}
	}
	return c, e
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func start(t *testing.T, c Config, e *engine) *Session {
	t.Helper()
	s, err := e.enter(c, []byte{'a'}, nil, true)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func disk(t *testing.T, c Config) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(c.Directory, "journal"))
	must(t, err)
	return b
}
func put(t *testing.T, path string, b []byte, mode os.FileMode) {
	t.Helper()
	must(t, os.WriteFile(path, b, mode))
	must(t, os.Chmod(path, mode))
}
func ended(t *testing.T, s *Session, poisoned bool) {
	t.Helper()
	st := s.State()
	if st.Ready || !st.Closed || st.Poisoned != poisoned {
		t.Fatalf("terminal disposition: %+v want poison=%v", st, poisoned)
	}
	_, err := s.Append([]byte{'x'})
	wantClass(t, err, InvalidState)
}

// Real ext4/XFS positive controls cover initialize, append, replay, inputs,
// record replacement, pinned paths and lifetime ownership without fault adapters.
func TestRealLifecycle(t *testing.T) {
	c, e := fixture(t, false)
	s := start(t, c, e)
	if !bytes.Equal(disk(t, c), golden(t)) || s.State().Next != 2 || !s.State().Ready {
		t.Fatal("initialize committed genuine first frame")
	}
	input := []byte("immutable")
	must(t, s.CreateInput("arg", input))
	got, err := s.ReadInput("arg", sha256.Sum256(input))
	must(t, err)
	if !bytes.Equal(got, input) {
		t.Fatal("digest-checked input")
	}
	must(t, s.ReplaceRecord("claim", []byte("one")))
	must(t, s.ReplaceRecord("claim", []byte("two")))
	before := disk(t, c)
	st := s.State()
	for _, p := range [][]byte{nil, make([]byte, MaxPayload+1)} {
		_, err = s.Append(p)
		wantClass(t, err, PayloadInvalid)
		if s.State() != st || !bytes.Equal(disk(t, c), before) {
			t.Fatal("clean refusal changes state")
		}
	}
	seq, err := s.Append([]byte{'b'})
	must(t, err)
	if seq != 2 {
		t.Fatal("clean refusal consumed sequence")
	}
	other, err := e.enter(c, nil, consumerFuncs{}, false)
	wantClass(t, err, OwnershipContended)
	if other != nil {
		t.Fatal("contender returned session")
	}
	must(t, s.Close())
	count := 0
	s, err = e.enter(c, nil, consumerFuncs{consume: func(n uint64, p []byte, r *Recovery) error {
		count++
		if n != uint64(count) {
			t.Fatal("replay sequence")
		}
		return r.ReplaceRecord("replayed", p)
	}}, false)
	must(t, err)
	defer s.Close()
	if count != 2 || s.State().Next != 3 || !s.State().Ready {
		t.Fatal("whole recovery ready")
	}
	// A renamed root must not redirect any operation to its replacement pathname.
	moved := c.Directory + "-pinned"
	must(t, os.Rename(c.Directory, moved))
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	must(t, os.Mkdir(c.Directory, 0700))
	must(t, s.CreateInput("pinned", []byte{'p'}))
	must(t, s.ReplaceRecord("pinned", []byte{'r'}))
	_, err = s.Append([]byte{'c'})
	must(t, err)
	if _, err = os.Stat(filepath.Join(c.Directory, "journal")); !os.IsNotExist(err) {
		t.Fatal("root replacement redirected writes")
	}
	b, err := os.ReadFile(filepath.Join(moved, "inputs", "pinned"))
	must(t, err)
	if string(b) != "p" {
		t.Fatal("pinned input missing")
	}
}

// Invalid entry arguments make zero state opens; neighboring valid entry reaches
// native open. These are deterministic precondition guards, not deployment proof.
func TestEntryNoTouch(t *testing.T) {
	c, e := fixture(t, true)
	opens := 0
	native := e.calls.open
	e.calls.open = func(d int, n string, f int, m uint32) (int, error) { opens++; return native(d, n, f, m) }
	for _, tc := range []struct {
		name  string
		edit  func(*Config)
		first []byte
		init  bool
		class Class
	}{
		{"mask missing", func(c *Config) { c.DeclaredUmask = nil }, []byte{'a'}, true, InvalidState},
		{"mask restrictive", func(c *Config) { m := uint32(0277); c.DeclaredUmask = &m }, []byte{'a'}, true, InvalidState},
		{"budget", func(c *Config) { c.ConsumerBudget = 0 }, []byte{'a'}, true, ConsumerFailure},
		{"first missing", func(*Config) {}, nil, true, PayloadInvalid},
		{"first too large", func(*Config) {}, make([]byte, MaxPayload+1), true, PayloadInvalid},
		{"attestation", func(c *Config) { c.RecoveryPrerequisiteEstablished = false }, nil, false, InvalidState},
		{"storage declaration", func(c *Config) { c.SupportedLocalStorage = false }, []byte{'a'}, true, UnsupportedStorage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := c
			tc.edit(&copy)
			_, err := e.enter(copy, tc.first, consumerFuncs{}, tc.init)
			wantClass(t, err, tc.class)
			if opens != 0 {
				t.Fatal("invalid entry touched state")
			}
			entries, err := os.ReadDir(c.Directory)
			must(t, err)
			if len(entries) != 2 {
				t.Fatal("invalid entry created files")
			}
		})
	}
	s := start(t, c, e)
	if opens == 0 {
		t.Fatal("valid entry did not reach storage")
	}
	must(t, s.Close())
}

// Established missing/empty and initialize-existing never recreate/truncate;
// complete unacknowledged history is included, but a malformed last frame causes
// zero consumer calls and leaves the entire prefix and tail byte-identical.
func TestEntryAndRecoveryRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		b     []byte
		class Class
	}{
		{"missing", nil, MissingState}, {"empty", []byte{}, MissingState}, {"partial", []byte{'S'}, IncompleteTail},
		{"bad-last", append(literalFrame(1, []byte{'a'}), literalFrame(3, []byte{'b'})...), Corruption},
		{"torn-last", append(literalFrame(1, []byte{'a'}), []byte("SDJ")...), IncompleteTail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, e := fixture(t, true)
			if tc.b != nil {
				put(t, filepath.Join(c.Directory, "journal"), tc.b, 0600)
			}
			calls := 0
			_, err := e.enter(c, nil, consumerFuncs{consume: func(uint64, []byte, *Recovery) error { calls++; return nil }}, false)
			wantClass(t, err, tc.class)
			if calls != 0 {
				t.Fatal("invalid history reached consumer")
			}
			if tc.b != nil {
				if !bytes.Equal(disk(t, c), tc.b) {
					t.Fatal("replay modified failed history")
				}
				_, err = e.enter(c, []byte{'a'}, nil, true)
				wantClass(t, err, InvalidState)
				if !bytes.Equal(disk(t, c), tc.b) {
					t.Fatal("initialize overwrote existing")
				}
			} else {
				if _, err = os.Stat(filepath.Join(c.Directory, "journal")); !os.IsNotExist(err) {
					t.Fatal("established created missing journal")
				}
			}
		})
	}
	c, e := fixture(t, true)
	put(t, filepath.Join(c.Directory, "journal"), golden(t), 0600)
	calls := 0
	s, err := e.enter(c, nil, consumerFuncs{consume: func(uint64, []byte, *Recovery) error { calls++; return nil }}, false)
	must(t, err)
	defer s.Close()
	if calls != 1 || s.State().Next != 2 {
		t.Fatal("unacknowledged first frame lost")
	}
}

// Real descriptor plants must refuse without repair; synthetic UID and alias
// metadata tests below cover guards unavailable without privileged fixtures.
func TestUnsafeDescriptors(t *testing.T) {
	for _, kind := range []string{"root-mode", "records-mode", "inputs-mode", "lock-mode", "journal-mode", "symlink-lock", "symlink-journal", "symlink-records", "hardlink-lock", "fifo-lock"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			switch kind {
			case "root-mode":
				must(t, os.Chmod(c.Directory, 0750))
			case "records-mode":
				must(t, os.Chmod(filepath.Join(c.Directory, "records"), 0770))
			case "inputs-mode":
				must(t, os.Chmod(filepath.Join(c.Directory, "inputs"), 0755))
			case "lock-mode":
				put(t, filepath.Join(c.Directory, "lock"), []byte("retain"), 0640)
			case "journal-mode":
				put(t, filepath.Join(c.Directory, "journal"), golden(t), 0400)
			case "symlink-lock", "symlink-journal":
				name := strings.TrimPrefix(kind, "symlink-")
				target := filepath.Join(c.Directory, "target")
				put(t, target, golden(t), 0600)
				must(t, os.Symlink(target, filepath.Join(c.Directory, name)))
			case "symlink-records":
				must(t, os.Remove(filepath.Join(c.Directory, "records")))
				must(t, os.Symlink("inputs", filepath.Join(c.Directory, "records")))
			case "hardlink-lock":
				put(t, filepath.Join(c.Directory, "target"), []byte("retain"), 0600)
				must(t, os.Link(filepath.Join(c.Directory, "target"), filepath.Join(c.Directory, "lock")))
			case "fifo-lock":
				must(t, syscall.Mkfifo(filepath.Join(c.Directory, "lock"), 0600))
			}
			_, err := e.enter(c, nil, consumerFuncs{}, false)
			wantClass(t, err, InvalidState)
			if kind == "lock-mode" {
				st, err := os.Stat(filepath.Join(c.Directory, "lock"))
				must(t, err)
				if st.Mode().Perm() != 0640 {
					t.Fatal("lock mode repaired")
				}
			}
		})
	}
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.Close()) // valid descriptor neighbor
}

// Injected fstat guards isolate exact owner/type/special-bit/link checks. These
// do not claim real cross-UID or bind-mount execution.
func TestMetadataGuards(t *testing.T) {
	for _, kind := range []string{"uid", "special", "links", "zero-links", "type", "directory-uid", "directory-special", "directory-type", "alias"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			native := e.calls.stat
			var rec syscall.Stat_t
			e.calls.stat = func(fd int, st *syscall.Stat_t) error {
				if err := native(fd, st); err != nil {
					return err
				}
				dir := st.Mode&syscall.S_IFMT == syscall.S_IFDIR
				switch kind {
				case "uid":
					if !dir {
						st.Uid++
					}
				case "special":
					if !dir {
						st.Mode |= 04000
					}
				case "links":
					if !dir {
						st.Nlink = 2
					}
				case "zero-links":
					if !dir {
						st.Nlink = 0
					}
				case "directory-special":
					if dir {
						st.Mode |= 02000
					}
				case "type":
					if !dir {
						st.Mode = syscall.S_IFDIR | 0600
					}
				case "directory-uid":
					if dir {
						st.Uid++
					}
				case "directory-type":
					if dir {
						st.Mode = syscall.S_IFREG | 0700
					}
				case "alias":
					if dir {
						if rec.Ino == 0 {
							rec = *st
						}
						st.Dev = rec.Dev
						st.Ino = rec.Ino
					}
				}
				return nil
			}
			_, err := e.enter(c, []byte{'a'}, nil, true)
			wantClass(t, err, InvalidState)
		})
	}
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.Close())
}

// Flag witnesses observe real opens and reject weakening of NOFOLLOW/CLOEXEC,
// exclusive creation or journal open modes; fcntl independently checks CLOEXEC.
func TestOpenFlagsAndCloseOrder(t *testing.T) {
	c, e := fixture(t, true)
	open := e.calls.open
	closeCall := e.calls.close
	lock := -1
	var closed []int
	e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
		if f&syscall.O_NOFOLLOW == 0 || f&syscall.O_CLOEXEC == 0 {
			t.Fatal("state open missing nofollow/cloexec")
		}
		if f&(syscall.O_TRUNC|syscall.O_APPEND) != 0 {
			t.Fatal("forbidden truncate/append")
		}
		if n == "journal" || n == "arg" || strings.HasPrefix(n, ".record-tmp-") {
			if f&syscall.O_CREAT != 0 && f&syscall.O_EXCL == 0 {
				t.Fatal("creation missing exclusive flag")
			}
		}
		fd, err := open(d, n, f, m)
		if err == nil {
			v, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
			if errno != 0 || v&syscall.FD_CLOEXEC == 0 {
				t.Fatal("descriptor not close-on-exec")
			}
			if n == "lock" {
				lock = fd
			}
		}
		return fd, err
	}
	e.calls.close = func(fd int) error { closed = append(closed, fd); return closeCall(fd) }
	s := start(t, c, e)
	must(t, s.CreateInput("arg", []byte{'i'}))
	must(t, s.ReplaceRecord("r", []byte{'r'}))
	must(t, s.Close())
	if len(closed) < 6 || closed[len(closed)-1] != lock {
		t.Fatal("flock descriptor not closed last")
	}
}

// Short writes advance only by known counts; zero/error/uncertain outcomes stop
// exactly once. Fault adapters do not prove a kernel's writeback behavior.
func TestFullWriteCounts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []struct {
			n   int
			err error
		}
		wantCalls int
		ok        bool
	}{
		{"short", []struct {
			n   int
			err error
		}{{2, nil}, {3, nil}}, 2, true},
		{"eintr-three", []struct {
			n   int
			err error
		}{{0, syscall.EINTR}, {0, syscall.EINTR}, {0, syscall.EINTR}, {5, nil}}, 4, true},
		{"eintr-reset", []struct {
			n   int
			err error
		}{{0, syscall.EINTR}, {1, nil}, {0, syscall.EINTR}, {4, nil}}, 4, true},
		{"eintr-four", []struct {
			n   int
			err error
		}{{0, syscall.EINTR}, {0, syscall.EINTR}, {0, syscall.EINTR}, {0, syscall.EINTR}}, 4, false},
		{"zero", []struct {
			n   int
			err error
		}{{0, nil}}, 1, false},
		{"positive-error", []struct {
			n   int
			err error
		}{{2, syscall.EIO}}, 1, false},
		{"full-error", []struct {
			n   int
			err error
		}{{5, syscall.EIO}}, 1, false},
		{"positive-eintr", []struct {
			n   int
			err error
		}{{1, syscall.EINTR}}, 1, false},
		{"negative", []struct {
			n   int
			err error
		}{{-1, nil}}, 1, false},
		{"too-large", []struct {
			n   int
			err error
		}{{6, nil}}, 1, false},
		{"enospc", []struct {
			n   int
			err error
		}{{0, syscall.ENOSPC}}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &engine{}
			s := &Session{e: e}
			calls := 0
			progress := 0
			e.calls.write = func(_ int, p []byte, o int64) (int, error) {
				if calls >= len(tc.results) {
					t.Fatal("uncertain write retried")
				}
				if o != 100+int64(progress) || string(p) != "abcde"[progress:] {
					t.Fatal("write suffix/offset changed")
				}
				r := tc.results[calls]
				calls++
				if r.err == nil && r.n > 0 && r.n <= len(p) {
					progress += r.n
				}
				return r.n, r.err
			}
			err := s.fullWrite(0, []byte("abcde"), 100)
			if tc.ok {
				must(t, err)
			} else {
				wantClass(t, err, StorageWriteFailure)
				var je *Error
				if !errors.As(err, &je) || !je.Poisoned {
					t.Fatal("write failure not poisoned")
				}
			}
			if calls != tc.wantCalls {
				t.Fatal("write retry count", calls)
			}
		})
	}
}

// Append fault tests retain prior committed bytes and sequence, terminate the
// session and permit a new ownership attempt only after ordinary non-sync errors.
func TestAppendFailuresAndDrift(t *testing.T) {
	for _, kind := range []string{"write", "close", "drift", "stat"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			before := disk(t, c)
			st := s.State()
			switch kind {
			case "write":
				e.calls.write = func(int, []byte, int64) (int, error) { return 0, syscall.ENOSPC }
			case "close":
				native := e.calls.close
				once := true
				e.calls.close = func(fd int) error {
					err := native(fd)
					if once {
						once = false
						return syscall.EIO
					}
					return err
				}
			case "drift":
				f, err := os.OpenFile(filepath.Join(c.Directory, "journal"), os.O_WRONLY|os.O_APPEND, 0)
				must(t, err)
				_, err = f.Write([]byte{'x'})
				must(t, err)
				must(t, f.Close())
			case "stat":
				e.calls.stat = func(int, *syscall.Stat_t) error { return syscall.EIO }
			}
			var err error
			switch kind {
			case "close":
				err = s.Close()
				wantClass(t, err, StorageWriteFailure)
			case "drift":
				_, err = s.Append(nil)
				wantClass(t, err, InvalidState)
			case "stat":
				_, err = s.Append([]byte{'b'})
				wantClass(t, err, StorageReadFailure)
			default:
				_, err = s.Append([]byte{'b'})
				wantClass(t, err, StorageWriteFailure)
			}
			ended(t, s, kind != "stat")
			if s.State().EOF != st.EOF || s.State().Next != st.Next {
				t.Fatal("failed append advanced durable state")
			}
			now := disk(t, c)
			if !bytes.Equal(now[:len(before)], before) {
				t.Fatal("committed prefix overwritten")
			}
		})
	}
}

// Synthetic EOF positions isolate cap/no-successor clean refusals without a
// 64 MiB write per test. Real byte controls and exact-cap codec coverage are separate.
func TestAppendCleanCap(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	native := e.calls.stat
	original := s.state
	e.calls.stat = func(fd int, st *syscall.Stat_t) error {
		err := native(fd, st)
		if fd == s.journal {
			st.Size = s.state.EOF
		}
		return err
	}
	for _, st := range []State{{EOF: FileCap - 57, Next: 2, Ready: true}, {EOF: 57, Next: 0, Ready: true}} {
		s.state = st
		before := disk(t, c)
		_, err := s.Append([]byte{'a', 'b'})
		wantClass(t, err, JournalFull)
		if s.State() != st || !bytes.Equal(before, disk(t, c)) {
			t.Fatal("journal-full mutated state")
		}
	}
	s.state = original
	e.calls.stat = native
	seq, err := s.Append([]byte{'b'})
	must(t, err)
	if seq != 2 {
		t.Fatal("valid append after journal-full")
	}
}

// Every actual sync boundary is observable, never retried, and latches this
// isolated engine. The public process-wide path is exercised in a subprocess.
func TestSyncFailureBoundaries(t *testing.T) {
	for _, phase := range []string{"init-file", "init-dir", "append", "recovery", "record-file", "record-dir", "input-file", "input-dir"} {
		for _, errno := range []syscall.Errno{syscall.EIO, syscall.EINTR, syscall.EINVAL, syscall.ENOLCK, syscall.ENOSYS, syscall.EOPNOTSUPP} {
			t.Run(fmt.Sprintf("%s/%d", phase, errno), func(t *testing.T) {
				c, e := fixture(t, true)
				var s *Session
				var err error
				if !strings.HasPrefix(phase, "init") {
					s = start(t, c, e)
					if phase == "recovery" {
						must(t, s.Close())
					}
				}
				calls := 0
				native := e.calls.sync
				target := 1
				if strings.HasSuffix(phase, "dir") {
					target = 2
				}
				e.calls.sync = func(fd int) error {
					calls++
					if calls == target {
						return errno
					}
					return native(fd)
				}
				switch phase {
				case "init-file", "init-dir":
					s, err = e.enter(c, []byte{'a'}, nil, true)
				case "recovery":
					s, err = e.enter(c, nil, consumerFuncs{consume: func(uint64, []byte, *Recovery) error { t.Fatal("callback before successful recovery sync"); return nil }}, false)
				case "append":
					_, err = s.Append([]byte{'b'})
				case "record-file", "record-dir":
					err = s.ReplaceRecord("r", []byte{'r'})
				case "input-file", "input-dir":
					err = s.CreateInput("i", []byte{'i'})
				}
				cl := StorageWriteFailure
				if unsupported(errno) {
					cl = UnsupportedStorage
				}
				wantClass(t, err, cl)
				if !MustExit(err) || !errors.Is(err, errno) || !e.syncFailed.Load() {
					t.Fatal("sync failure barrier/cause missing")
				}
				if calls != target {
					t.Fatal("fsync retried after failure")
				}
				if s != nil {
					ended(t, s, true)
				}
				opens := 0
				e.calls.open = func(int, string, int, uint32) (int, error) { opens++; return -1, syscall.EIO }
				_, err = e.enter(c, nil, consumerFuncs{}, false)
				if !MustExit(err) || opens != 0 {
					t.Fatal("same-process sync barrier opened state")
				}
				_, err = e.enter(c, []byte{'a'}, nil, true)
				if !MustExit(err) || opens != 0 {
					t.Fatal("sync barrier allowed initialize")
				}
			})
		}
	}
}

// Successful order witnesses fail if any sync is deleted, even when failure
// injection would no longer fire. They observe real syscalls in isolated fixtures.
func TestDurabilityOrder(t *testing.T) {
	c, e := fixture(t, true)
	var events []string
	names := map[int]string{}
	o := e.calls.open
	w := e.calls.write
	sy := e.calls.sync
	rn := e.calls.rename
	e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
		fd, err := o(d, n, f, m)
		if err == nil {
			names[fd] = n
		}
		return fd, err
	}
	label := func(fd int) string {
		n := names[fd]
		if strings.HasPrefix(n, ".record-tmp-") {
			return "tmp"
		}
		if n == c.Directory {
			return "root"
		}
		return n
	}
	e.calls.write = func(fd int, p []byte, off int64) (int, error) {
		events = append(events, "write:"+label(fd))
		return w(fd, p, off)
	}
	e.calls.sync = func(fd int) error { events = append(events, "sync:"+label(fd)); return sy(fd) }
	e.calls.rename = func(a int, x string, b int, y string) error {
		events = append(events, "rename")
		if a != b || label(a) != "records" {
			t.Fatal("rename escaped record directory")
		}
		return rn(a, x, b, y)
	}
	expect := func(want string) {
		t.Helper()
		if strings.Join(events, ",") != want {
			t.Fatalf("durability order: %v want %s", events, want)
		}
		events = nil
	}
	s := start(t, c, e)
	expect("write:journal,sync:journal,sync:root")
	_, err := s.Append([]byte{'b'})
	must(t, err)
	expect("write:journal,sync:journal")
	must(t, s.CreateInput("arg", []byte{'i'}))
	expect("write:arg,sync:arg,sync:inputs")
	must(t, s.ReplaceRecord("r", []byte{'r'}))
	expect("write:tmp,sync:tmp,rename,sync:records")
	must(t, s.Close())
	s, err = e.enter(c, nil, consumerFuncs{consume: func(_ uint64, p []byte, r *Recovery) error {
		events = append(events, "consume")
		return r.ReplaceRecord("r", p)
	}, finish: func(*Recovery) error { events = append(events, "finish"); return nil }}, false)
	must(t, err)
	defer s.Close()
	expect("sync:journal,consume,write:tmp,sync:tmp,rename,sync:records,consume,write:tmp,sync:tmp,rename,sync:records,finish")
}

// Recovery must never publish partial readiness or release ownership while a
// consumer survives. Budget and ignored operation failures remain sticky.
func TestRecoveryFailures(t *testing.T) {
	for _, kind := range []string{"first-read", "second-read", "consumer", "finish", "budget", "ignored-record", "before-drift", "after-drift"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			must(t, s.Close())
			before := disk(t, c)
			callbacks := 0
			materialized := false
			reads := 0
			rd := e.calls.read
			stat := e.calls.stat
			stats := 0
			if kind == "first-read" || kind == "second-read" {
				e.calls.read = func(fd int, p []byte, off int64) (int, error) {
					reads++
					if (kind == "first-read" && reads == 1) || (kind == "second-read" && reads == 3) {
						return 0, syscall.EIO
					}
					return rd(fd, p, off)
				}
			}
			if kind == "before-drift" {
				e.calls.stat = func(fd int, st *syscall.Stat_t) error {
					err := stat(fd, st)
					if st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Size == 57 {
						stats++
						if stats == 2 {
							st.Size++
						}
					}
					return err
				}
			}
			consumer := consumerFuncs{consume: func(_ uint64, p []byte, r *Recovery) error {
				callbacks++
				if kind == "budget" {
					_ = r.Budget.Charge(c.ConsumerBudget + 1)
					return nil
				}
				if kind == "ignored-record" {
					_ = r.ReplaceRecord("../escape", p)
					return nil
				}
				must(t, r.ReplaceRecord("cache", p))
				materialized = true
				// A new acquisition still contends from within the callback.
				_, err := e.enter(c, nil, consumerFuncs{}, false)
				wantClass(t, err, OwnershipContended)
				if kind == "consumer" {
					return errors.New("semantic failure")
				}
				return nil
			}, finish: func(*Recovery) error {
				if kind == "finish" {
					return errors.New("whole state rejected")
				}
				if kind == "after-drift" {
					e.calls.stat = func(fd int, st *syscall.Stat_t) error {
						err := stat(fd, st)
						if st.Size == 57 && st.Mode&syscall.S_IFMT == syscall.S_IFREG {
							st.Size++
						}
						return err
					}
				}
				return nil
			}}
			got, err := e.enter(c, nil, consumer, false)
			if got != nil {
				t.Fatal("failed recovery published session")
			}
			want := ConsumerFailure
			if strings.Contains(kind, "read") {
				want = StorageReadFailure
			}
			if kind == "ignored-record" || strings.Contains(kind, "drift") {
				want = InvalidState
			}
			wantClass(t, err, want)
			var je *Error
			if !errors.As(err, &je) {
				t.Fatal("typed error missing")
			}
			if materialized && !je.Poisoned {
				t.Fatal("post-materialization failure not poisoned")
			}
			if (kind == "first-read" || kind == "second-read" || kind == "before-drift") && callbacks != 0 {
				t.Fatal("consumer before complete validation")
			}
			if !bytes.Equal(before, disk(t, c)) {
				t.Fatal("recovery failure modified journal")
			}
			// Restore only fault adapters, then reacquire to show teardown completed.
			e.calls = nativeCalls
			e.calls.statfs = func(_ int, s *syscall.Statfs_t) error { s.Type = 0xEF53; return nil }
			got, err = e.enter(c, nil, consumerFuncs{}, false)
			must(t, err)
			must(t, got.Close())
		})
	}
}

// First-pass success does not excuse second-pass corruption/read errors after
// record materialization. Full history is validated a second time.
func TestSecondPassRevalidation(t *testing.T) {
	for _, kind := range []string{"read", "digest"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			_, err := s.Append([]byte{'b'})
			must(t, err)
			must(t, s.Close())
			before := disk(t, c)
			rd := e.calls.read
			reads := 0
			callbacks := 0
			e.calls.read = func(fd int, p []byte, o int64) (int, error) {
				reads++
				n, err := rd(fd, p, o)
				if reads == 8 {
					if kind == "read" {
						return n, syscall.EIO
					}
					p[n-1] ^= 1
				}
				return n, err
			}
			_, err = e.enter(c, nil, consumerFuncs{consume: func(_ uint64, p []byte, r *Recovery) error { callbacks++; return r.ReplaceRecord("cache", p) }}, false)
			want := Corruption
			if kind == "read" {
				want = StorageReadFailure
			}
			wantClass(t, err, want)
			var je *Error
			if !errors.As(err, &je) || !je.Poisoned || callbacks != 1 {
				t.Fatal("second-pass failure readiness/poison")
			}
			if !bytes.Equal(before, disk(t, c)) {
				t.Fatal("second-pass fault changed history")
			}
		})
	}
}

// Stale temporary bytes survive. Exact name collisions and exhausted counters
// terminate without retry, deletion, replacement, or loss of committed intent.
func TestRecordTemps(t *testing.T) {
	c, e := fixture(t, true)
	e.calls.random = func(p []byte) (int, error) { clear(p); return len(p), nil }
	s := start(t, c, e)
	stale := ".record-tmp-11111111111111111111111111111111-0000000000000001"
	path := filepath.Join(c.Directory, "records", stale)
	put(t, path, []byte("stale"), 0600)
	must(t, s.ReplaceRecord("r", []byte("one")))
	b, err := os.ReadFile(path)
	must(t, err)
	if string(b) != "stale" {
		t.Fatal("stale temporary changed")
	}
	collision := ".record-tmp-00000000000000000000000000000000-0000000000000002"
	target := filepath.Join(c.Directory, "records", collision)
	put(t, target, []byte("collision"), 0600)
	err = s.ReplaceRecord("r", []byte("two"))
	wantClass(t, err, InvalidState)
	ended(t, s, false)
	b, err = os.ReadFile(target)
	must(t, err)
	if string(b) != "collision" {
		t.Fatal("temporary collision overwritten")
	}
	b, err = os.ReadFile(filepath.Join(c.Directory, "records", "r"))
	must(t, err)
	if string(b) != "one" {
		t.Fatal("record changed after collision")
	}
	if !bytes.Equal(disk(t, c), golden(t)) {
		t.Fatal("collision lost committed intent")
	}
	c, e = fixture(t, true)
	s = start(t, c, e)
	s.counter = ^uint64(0)
	err = s.ReplaceRecord("r", []byte{'a'})
	wantClass(t, err, InvalidState)
	entries, err := os.ReadDir(filepath.Join(c.Directory, "records"))
	must(t, err)
	if len(entries) != 0 {
		t.Fatal("counter wrap created temporary")
	}
}

// Immutable input creation refuses existing/partial entries without reuse;
// digest mismatch returns no bytes, and record names cannot reach input paths.
func TestImmutableInputs(t *testing.T) {
	for _, kind := range []string{"existing", "partial", "digest", "escape", "reserved", "oversize", "symlink", "hardlink", "mode"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			p := []byte("input")
			path := filepath.Join(c.Directory, "inputs", "arg")
			if kind == "partial" {
				put(t, path, []byte{'i'}, 0600)
			} else {
				must(t, s.CreateInput("arg", p))
			}
			before, err := os.ReadFile(path)
			must(t, err)
			switch kind {
			case "existing", "partial":
				err = s.CreateInput("arg", p)
				wantClass(t, err, InvalidState)
			case "digest":
				var got []byte
				got, err = s.ReadInput("arg", sha256.Sum256([]byte("wrong")))
				wantClass(t, err, Corruption)
				if got != nil {
					t.Fatal("digest mismatch exposed input")
				}
			case "escape":
				err = s.ReplaceRecord("../inputs/arg", []byte("overwrite"))
				wantClass(t, err, InvalidState)
			case "reserved":
				err = s.ReplaceRecord(".record-tmp-x", p)
				wantClass(t, err, InvalidState)
			case "oversize":
				err = s.CreateInput("large", make([]byte, MaxPayload+1))
				wantClass(t, err, InvalidState)
			case "symlink":
				must(t, os.Symlink("arg", filepath.Join(c.Directory, "inputs", "link")))
				_, err = s.ReadInput("link", sha256.Sum256(p))
				wantClass(t, err, InvalidState)
			case "hardlink":
				must(t, os.Link(path, filepath.Join(c.Directory, "inputs", "link")))
				_, err = s.ReadInput("arg", sha256.Sum256(p))
				wantClass(t, err, InvalidState)
			case "mode":
				must(t, os.Chmod(path, 0644))
				_, err = s.ReadInput("arg", sha256.Sum256(p))
				wantClass(t, err, InvalidState)
			}
			after, re := os.ReadFile(path)
			must(t, re)
			if !bytes.Equal(before, after) {
				t.Fatal("immutable input modified")
			}
			ended(t, s, false)
		})
	}
}

// Random failures, unsupported types/operations and rename faults preserve their
// exact classes. Unsupported syncs are separately covered by barrier witnesses.
func TestStorageFaults(t *testing.T) {
	for _, kind := range []string{"random-error", "random-short", "fs-type", "fs-error", "flock-unsupported", "flock-read", "rename"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			want := StorageReadFailure
			switch kind {
			case "random-error":
				e.calls.random = func([]byte) (int, error) { return 0, syscall.EIO }
			case "random-short":
				e.calls.random = func([]byte) (int, error) { return 15, nil }
			case "fs-type":
				e.calls.statfs = func(_ int, s *syscall.Statfs_t) error { s.Type = 0x794c7630; return nil }
				want = UnsupportedStorage
			case "fs-error":
				e.calls.statfs = func(int, *syscall.Statfs_t) error { return syscall.EIO }
			case "flock-unsupported":
				e.calls.flock = func(int, int) error { return syscall.ENOSYS }
				want = UnsupportedStorage
			case "flock-read":
				e.calls.flock = func(int, int) error { return syscall.EIO }
			}
			if kind == "rename" {
				s := start(t, c, e)
				e.calls.rename = func(int, string, int, string) error { return syscall.EIO }
				err := s.ReplaceRecord("r", []byte{'r'})
				wantClass(t, err, StorageWriteFailure)
				ended(t, s, true)
				if !bytes.Equal(disk(t, c), golden(t)) {
					t.Fatal("rename failure lost journal")
				}
				entries, err := os.ReadDir(filepath.Join(c.Directory, "records"))
				must(t, err)
				if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".record-tmp-") {
					t.Fatal("rename failure did not retain temporary")
				}
				return
			}
			_, err := e.enter(c, []byte{'a'}, nil, true)
			wantClass(t, err, want)
			if _, err = os.Stat(filepath.Join(c.Directory, "journal")); !os.IsNotExist(err) {
				t.Fatal("failed acquisition created journal")
			}
		})
	}
}

// Raw Linux syscall adapters have real positive controls here. This is local
// syscall behavior, not crash durability or kernel writeback-error qualification.
func TestRawAdapters(t *testing.T) {
	c, _ := fixture(t, false)
	path := filepath.Join(c.Directory, "raw")
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC, 0600)
	must(t, err)
	defer syscall.Close(fd)
	n, err := rawWrite(fd, []byte("abc"), 2)
	must(t, err)
	if n != 3 {
		t.Fatal("raw pwrite count")
	}
	buf := make([]byte, 3)
	n, err = rawRead(fd, buf, 2)
	must(t, err)
	if n != 3 || string(buf) != "abc" {
		t.Fatal("raw pread offset/count")
	}
	_, err = rawWrite(-1, []byte{'a'}, 0)
	if err != syscall.EBADF {
		t.Fatal("raw write errno")
	}
	_, err = rawRead(-1, buf, 0)
	if err != syscall.EBADF {
		t.Fatal("raw read errno")
	}
	var random [16]byte
	n, err = rawRandom(random[:])
	must(t, err)
	if n != 16 {
		t.Fatal("observable random count")
	}
}

// The child modifies its own umask or process barrier only. The parent never
// changes process-global umask or resets the production sync-failure latch.
func TestProcessHelper(t *testing.T) {
	mode := os.Getenv("JOURNAL_TEST_CHILD")
	if mode == "" {
		return
	}
	d := os.Getenv("JOURNAL_TEST_DIR")
	mask := uint32(0077)
	c := Config{Directory: d, Records: "records", Inputs: "inputs", DeclaredUmask: &mask, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: MaxPayload}
	switch mode {
	case "contention":
		_, err := OpenEstablished(c, consumerFuncs{})
		wantClass(t, err, OwnershipContended)
	case "umask":
		syscall.Umask(0277)
		_, err := Initialize(c, []byte{'a'})
		wantClass(t, err, InvalidState)
		st, err := os.Stat(filepath.Join(d, "lock"))
		must(t, err)
		if st.Mode().Perm() != 0400 {
			t.Fatal("false declaration did not retain unsafe lock")
		}
		syscall.Umask(0077)
		_, err = Initialize(c, []byte{'a'})
		wantClass(t, err, InvalidState)
		st, err = os.Stat(filepath.Join(d, "lock"))
		must(t, err)
		if st.Mode().Perm() != 0400 {
			t.Fatal("unsafe lock repaired")
		}
	case "barrier":
		process.calls.sync = func(int) error { return syscall.EIO }
		_, err := Initialize(c, []byte{'a'})
		if !MustExit(err) {
			t.Fatal("public sync sentinel")
		}
		process.calls = nativeCalls
		_, err = OpenEstablished(c, consumerFuncs{})
		if !MustExit(err) {
			t.Fatal("public process latch cleared")
		}
		_, err = Initialize(c, []byte{'a'})
		if !MustExit(err) {
			t.Fatal("public initialize bypassed latch")
		}
	default:
		t.Fatal("unknown child mode")
	}
}
func child(t *testing.T, c Config, mode string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "JOURNAL_TEST_CHILD="+mode, "JOURNAL_TEST_DIR="+c.Directory)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child %s: %v\n%s", mode, err, out)
	}
}

// These are real process/flock and false-declaration witnesses on supported
// storage. They do not claim host restart or operator disposition was exercised.
func TestRealProcessBoundaries(t *testing.T) {
	for _, mode := range []string{"contention", "umask", "barrier"} {
		t.Run(mode, func(t *testing.T) {
			c, e := fixture(t, false)
			if mode == "contention" {
				s := start(t, c, e)
				before := disk(t, c)
				child(t, c, mode)
				if !bytes.Equal(before, disk(t, c)) {
					t.Fatal("contending process changed journal")
				}
				must(t, s.Close())
				s, err := e.enter(c, nil, consumerFuncs{}, false)
				must(t, err)
				must(t, s.Close())
			} else {
				child(t, c, mode)
			}
		})
	}
}

// Same-directory aliases are refused by actual inode identity; the two valid
// names remain a positive control. No privileged bind mount is required.
func TestDirectoryAlias(t *testing.T) {
	c, e := fixture(t, true)
	c.Inputs = c.Records
	_, err := e.enter(c, []byte{'a'}, nil, true)
	wantClass(t, err, InvalidState)
	if _, err = os.Stat(filepath.Join(c.Directory, "journal")); !os.IsNotExist(err) {
		t.Fatal("aliased input directory created journal")
	}
	c.Inputs = "inputs"
	s := start(t, c, e)
	must(t, s.Close())
}

// Every descriptor category is independently validated. Mutating only one
// category must still refuse, including temporaries and existing record reads.
func TestPerDescriptorValidation(t *testing.T) {
	for _, target := range []string{"root", "records", "inputs", "lock", "journal", "temp", "record", "input"} {
		t.Run(target, func(t *testing.T) {
			c, e := fixture(t, true)
			fds := map[int]string{}
			o := e.calls.open
			st := e.calls.stat
			e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
				fd, err := o(d, n, f, m)
				if err == nil {
					label := n
					if n == c.Directory {
						label = "root"
					}
					if strings.HasPrefix(n, ".record-tmp-") {
						label = "temp"
					}
					fds[fd] = label
				}
				return fd, err
			}
			armed := target != "temp" && target != "record" && target != "input"
			e.calls.stat = func(fd int, s *syscall.Stat_t) error {
				err := st(fd, s)
				if err == nil && armed && fds[fd] == target {
					s.Mode |= 0040
				}
				return err
			}
			if armed {
				_, err := e.enter(c, []byte{'a'}, nil, true)
				wantClass(t, err, InvalidState)
				return
			}
			s := start(t, c, e)
			must(t, s.ReplaceRecord("record", []byte{'r'}))
			must(t, s.CreateInput("input", []byte{'i'}))
			armed = true
			var err error
			if target == "input" {
				_, err = s.ReadInput("input", sha256.Sum256([]byte{'i'}))
			} else {
				err = s.ReplaceRecord("record", []byte{'n'})
			}
			wantClass(t, err, InvalidState)
			if !bytes.Equal(disk(t, c), golden(t)) {
				t.Fatal("descriptor refusal lost history")
			}
		})
	}
}

// Metadata and read count/size failures never expose unchecked input bytes.
// Valid reads and the explicit consumer charge/release are neighboring controls.
func TestInputReadBounds(t *testing.T) {
	for _, kind := range []string{"oversize", "negative-size", "short", "full-error", "size-drift"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			must(t, s.CreateInput("arg", []byte("abc")))
			stat := e.calls.stat
			rd := e.calls.read
			seen := 0
			e.calls.stat = func(fd int, st *syscall.Stat_t) error {
				err := stat(fd, st)
				if st.Size == 3 {
					seen++
					switch kind {
					case "oversize":
						st.Size = MaxPayload + 1
					case "negative-size":
						st.Size = -1
					case "size-drift":
						if seen == 2 {
							st.Size = 4
						}
					}
				}
				return err
			}
			e.calls.read = func(fd int, p []byte, o int64) (int, error) {
				if kind == "oversize" || kind == "negative-size" {
					t.Fatal("oversized input allocated/read")
				}
				if kind == "short" {
					return 0, nil
				}
				n, err := rd(fd, p, o)
				if kind == "full-error" {
					return n, syscall.EIO
				}
				return n, err
			}
			b, err := s.ReadInput("arg", sha256.Sum256([]byte("abc")))
			want := Corruption
			if kind == "full-error" {
				want = StorageReadFailure
			}
			wantClass(t, err, want)
			if b != nil {
				t.Fatal("unchecked input returned")
			}
			ended(t, s, false)
		})
	}
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.CreateInput("arg", []byte("abc")))
	must(t, s.Close())
	var retained *Recovery
	s, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error {
		retained = r
		b, err := r.ReadInput("arg", sha256.Sum256([]byte("abc")))
		if err != nil {
			return err
		}
		if string(b) != "abc" || r.Budget.Used() != 3 {
			t.Fatal("input consumer budget charge")
		}
		return r.Budget.Release(3)
	}}, false)
	must(t, err)
	must(t, s.Close())
	wantClass(t, retained.ReplaceRecord("late", nil), InvalidState)
	_, err = retained.ReadInput("arg", [32]byte{})
	wantClass(t, err, InvalidState)
}

// Mutation failures include partial writes and close errors on input/temp files.
// The partial bytes remain, no rename/ack is allowed, and the session ends.
func TestObjectWriteAndCloseFailures(t *testing.T) {
	for _, object := range []string{"input", "record"} {
		for _, fault := range []string{"write", "close", "create"} {
			t.Run(object+"/"+fault, func(t *testing.T) {
				c, e := fixture(t, true)
				s := start(t, c, e)
				w := e.calls.write
				cl := e.calls.close
				o := e.calls.open
				calls := 0
				switch fault {
				case "write":
					e.calls.write = func(fd int, p []byte, off int64) (int, error) {
						calls++
						if calls == 1 {
							n, err := w(fd, p[:1], off)
							if err != nil {
								return n, err
							}
							return n, syscall.ENOSPC
						}
						t.Fatal("object write retried uncertain bytes")
						return 0, nil
					}
				case "close":
					e.calls.close = func(fd int) error {
						err := cl(fd)
						calls++
						if calls == 1 {
							return syscall.EIO
						}
						return err
					}
				case "create":
					e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
						if f&syscall.O_CREAT != 0 {
							return -1, syscall.ENOSPC
						}
						return o(d, n, f, m)
					}
				}
				var err error
				if object == "input" {
					err = s.CreateInput("arg", []byte("abc"))
				} else {
					err = s.ReplaceRecord("r", []byte("abc"))
				}
				wantClass(t, err, StorageWriteFailure)
				ended(t, s, true)
				if !bytes.Equal(disk(t, c), golden(t)) {
					t.Fatal("object failure changed committed journal")
				}
				if fault == "write" {
					dir := "records"
					if object == "input" {
						dir = "inputs"
					}
					entries, err := os.ReadDir(filepath.Join(c.Directory, dir))
					must(t, err)
					if len(entries) != 1 {
						t.Fatal("partial object not retained")
					}
					b, err := os.ReadFile(filepath.Join(c.Directory, dir, entries[0].Name()))
					must(t, err)
					if string(b) != "a" {
						t.Fatal("partial object bytes rewritten")
					}
				}
			})
		}
	}
}

// Teardown does not hide a sync sentinel behind additional close errors, and
// every descriptor closes once with flock last even in failed initialization.
func TestTeardownCauses(t *testing.T) {
	c, e := fixture(t, true)
	cl := e.calls.close
	seen := map[int]bool{}
	e.calls.close = func(fd int) error {
		if seen[fd] {
			t.Fatal("descriptor close retried")
		}
		seen[fd] = true
		_ = cl(fd)
		return syscall.EIO
	}
	e.calls.sync = func(int) error { return syscall.EINTR }
	_, err := e.enter(c, []byte{'a'}, nil, true)
	wantClass(t, err, StorageWriteFailure)
	if !MustExit(err) || !errors.Is(err, syscall.EINTR) || !errors.Is(err, syscall.EIO) {
		t.Fatal("teardown lost original sync/close causes")
	}
}

// Terminal methods cannot issue additional syscalls or accumulate new error
// history; repeated refusals retain the original disposition and committed bytes.
func TestTerminalMethods(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.Close())
	before := disk(t, c)
	terminal := s.terminal
	state := s.State()
	e.calls.open = func(int, string, int, uint32) (int, error) { t.Fatal("closed session opened state"); return -1, nil }
	e.calls.write = func(int, []byte, int64) (int, error) { t.Fatal("closed session wrote state"); return 0, nil }
	e.calls.close = func(int) error { t.Fatal("closed session closed descriptor again"); return nil }
	for i := 0; i < 3; i++ {
		_, err := s.Append([]byte{'a'})
		wantClass(t, err, InvalidState)
		wantClass(t, s.CreateInput("arg", nil), InvalidState)
		wantClass(t, s.ReplaceRecord("r", nil), InvalidState)
		_, err = s.ReadInput("arg", [32]byte{})
		wantClass(t, err, InvalidState)
		must(t, s.Close())
	}
	if s.terminal != terminal || s.State() != state || !bytes.Equal(before, disk(t, c)) {
		t.Fatal("terminal state changed on repeated calls")
	}
}

// Maximum-sized initialized/appended frames are real writes, with max+1 a clean
// no-op. They supplement bounded virtual-file codec tests, not crash durability.
func TestRealMaximumFrame(t *testing.T) {
	c, e := fixture(t, false)
	p := bytes.Repeat([]byte{0xa5}, MaxPayload)
	s, err := e.enter(c, p, nil, true)
	must(t, err)
	defer s.Close()
	if s.State().EOF != 1048632 || s.State().Next != 2 {
		t.Fatal("maximum first frame size")
	}
	before := s.State()
	_, err = s.Append(make([]byte, MaxPayload+1))
	wantClass(t, err, PayloadInvalid)
	if s.State() != before {
		t.Fatal("oversized append changes counters")
	}
	seq, err := s.Append(p)
	must(t, err)
	if seq != 2 || s.State().EOF != 2097264 {
		t.Fatal("maximum append after refusal")
	}
	must(t, s.Close())
	count := 0
	s, err = e.enter(c, nil, consumerFuncs{consume: func(_ uint64, b []byte, _ *Recovery) error {
		count++
		if !bytes.Equal(b, p) {
			t.Fatal("maximum payload altered")
		}
		return nil
	}}, false)
	must(t, err)
	must(t, s.Close())
	if count != 2 {
		t.Fatal("maximum frames not replayed")
	}
}

// A consumer is mandatory before any state access, and protected root symlinks
// cannot redirect acquisition. Missing paths remain missing-state, never init.
func TestAdditionalEntryGuards(t *testing.T) {
	c, e := fixture(t, true)
	calls := 0
	o := e.calls.open
	e.calls.open = func(d int, n string, f int, m uint32) (int, error) { calls++; return o(d, n, f, m) }
	_, err := e.enter(c, nil, nil, false)
	wantClass(t, err, ConsumerFailure)
	if calls != 0 {
		t.Fatal("missing consumer touched state")
	}
	link := filepath.Join(t.TempDir(), "link")
	must(t, os.Symlink(c.Directory, link))
	copy := c
	copy.Directory = link
	_, err = e.enter(copy, []byte{'a'}, nil, true)
	wantClass(t, err, InvalidState)
	copy.Directory = filepath.Join(c.Directory, "missing")
	_, err = e.enter(copy, nil, consumerFuncs{}, false)
	wantClass(t, err, MissingState)
	c.Inputs = "missing"
	_, err = e.enter(c, []byte{'a'}, nil, true)
	wantClass(t, err, MissingState)
}

// Sync success, not write completion, advances durable counters. Other active
// sessions also refuse after the process latch, and an acquisition cannot publish
// readiness if another ownership session failed sync while it was opening.
func TestCommitAndProcessBarrierOrdering(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	before := s.State()
	sy := e.calls.sync
	e.calls.sync = func(fd int) error {
		if fd == s.journal && (s.state.EOF != before.EOF || s.state.Next != before.Next) {
			t.Fatal("durable counters advanced before fsync")
		}
		return sy(fd)
	}
	_, err := s.Append([]byte{'b'})
	must(t, err)
	e.calls.sync = sy
	c2, _ := fixture(t, true)
	s2 := start(t, c2, e)
	e.calls.sync = func(int) error { return syscall.EIO }
	_, err = s.Append([]byte{'c'})
	if !MustExit(err) {
		t.Fatal("sync exit requirement")
	}
	writes := 0
	e.calls.write = func(int, []byte, int64) (int, error) { writes++; return 0, syscall.EIO }
	_, err = s2.Append([]byte{'x'})
	if !MustExit(err) || writes != 0 {
		t.Fatal("active session bypassed process barrier")
	}
	ended(t, s2, true)
	c, e = fixture(t, true)
	rnd := e.calls.random
	e.calls.random = func(p []byte) (int, error) { n, err := rnd(p); e.syncFailed.Store(true); return n, err }
	got, err := e.enter(c, []byte{'a'}, nil, true)
	if got != nil || !MustExit(err) {
		t.Fatal("acquisition published after concurrent sync failure")
	}
}

// Separate recovery EOF checks catch drift before sync, after sync and after
// consumption. Injected stat results isolate each checkpoint without file repair.
func TestRecoveryEOFCheckpoints(t *testing.T) {
	for _, at := range []int{2, 3, 4} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			must(t, s.Close())
			native := e.calls.stat
			count := 0
			consumed := 0
			e.calls.stat = func(fd int, st *syscall.Stat_t) error {
				err := native(fd, st)
				if st.Size == 57 && st.Mode&syscall.S_IFMT == syscall.S_IFREG {
					count++
					if count == at {
						st.Size++
					}
				}
				return err
			}
			got, err := e.enter(c, nil, consumerFuncs{consume: func(uint64, []byte, *Recovery) error { consumed++; return nil }}, false)
			wantClass(t, err, InvalidState)
			if got != nil {
				t.Fatal("EOF checkpoint published readiness")
			}
			if at < 4 && consumed != 0 {
				t.Fatal("EOF checkpoint allowed consumer")
			}
		})
	}
}

// Directory distinctness compares the pair, not inode alone or st_dev alone.
// Synthetic identities here do not qualify descendant mounts (outside scope).
func TestDirectoryIdentityPair(t *testing.T) {
	for _, kind := range []string{"equal", "different-dev", "different-ino"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			o := e.calls.open
			st := e.calls.stat
			records, inputs := -1, -1
			e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
				fd, err := o(d, n, f, m)
				if n == "records" {
					records = fd
				}
				if n == "inputs" {
					inputs = fd
				}
				return fd, err
			}
			e.calls.stat = func(fd int, s *syscall.Stat_t) error {
				err := st(fd, s)
				if fd == records || fd == inputs {
					s.Dev = 100
					s.Ino = 200
					if fd == inputs {
						if kind == "different-dev" {
							s.Dev++
						}
						if kind == "different-ino" {
							s.Ino++
						}
					}
				}
				return err
			}
			s, err := e.enter(c, []byte{'a'}, nil, true)
			if kind == "equal" {
				wantClass(t, err, InvalidState)
			} else {
				if err != nil {
					t.Fatal("distinct dev/inode pair refused", err)
				}
				must(t, s.Close())
			}
		})
	}
}

// Entry and object open flags are checked on both entries. A reused lock stays
// nonexclusive; established history is never created or truncated.
func TestExactOpenModes(t *testing.T) {
	c, e := fixture(t, true)
	native := e.calls.open
	established := false
	e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
		switch {
		case n == c.Directory || n == "records" || n == "inputs":
			if f&syscall.O_DIRECTORY == 0 {
				t.Fatal("directory flag missing")
			}
		case n == "lock":
			if f&syscall.O_RDWR == 0 || f&syscall.O_CREAT == 0 || f&syscall.O_EXCL != 0 || m != 0600 {
				t.Fatal("stable lock open flags")
			}
		case n == "journal":
			if f&syscall.O_RDWR == 0 {
				t.Fatal("journal not read/write")
			}
			if established {
				if f&(syscall.O_CREAT|syscall.O_EXCL|syscall.O_TRUNC|syscall.O_APPEND) != 0 {
					t.Fatal("established journal creation flags")
				}
			} else if f&(syscall.O_CREAT|syscall.O_EXCL) != (syscall.O_CREAT|syscall.O_EXCL) || m != 0600 {
				t.Fatal("initialize exclusive journal flags")
			}
		case n == "arg" || strings.HasPrefix(n, ".record-tmp-"):
			if f&(syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL) != (syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL) || m != 0600 {
				t.Fatal("object exclusive creation flags")
			}
		}
		return native(d, n, f, m)
	}
	s := start(t, c, e)
	must(t, s.CreateInput("arg", []byte{'a'}))
	must(t, s.ReplaceRecord("r", []byte{'a'}))
	must(t, s.Close())
	established = true
	s, err := e.enter(c, nil, consumerFuncs{}, false)
	must(t, err)
	must(t, s.Close())
}

// A budget rejection cannot be followed by materialization even when a consumer
// ignores the failed charge; no cache is written after the failed admission.
func TestBudgetStopsMaterialization(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	must(t, s.Close())
	_, err := e.enter(c, nil, consumerFuncs{consume: func(_ uint64, p []byte, r *Recovery) error {
		_ = r.Budget.Charge(c.ConsumerBudget + 1)
		wantClass(t, r.ReplaceRecord("forbidden", p), ConsumerFailure)
		return nil
	}}, false)
	wantClass(t, err, ConsumerFailure)
	entries, err := os.ReadDir(filepath.Join(c.Directory, "records"))
	must(t, err)
	if len(entries) != 0 {
		t.Fatal("budget failure materialized a record")
	}
}

// A short read must fail even if zero-filled unread bytes would happen to match
// the supplied digest; checksum equality cannot excuse incomplete byte access.
func TestShortInputMatchingDigest(t *testing.T) {
	c, e := fixture(t, true)
	s := start(t, c, e)
	p := make([]byte, 3)
	must(t, s.CreateInput("arg", p))
	b, err := s.ReadInput("arg", sha256.Sum256(p))
	must(t, err)
	if len(b) != 3 {
		t.Fatal("zero input positive control")
	}
	e.calls.read = func(int, []byte, int64) (int, error) { return 0, nil }
	b, err = s.ReadInput("arg", sha256.Sum256(p))
	wantClass(t, err, Corruption)
	if b != nil {
		t.Fatal("short matching-digest input returned")
	}
}
