//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Child exit points model process loss around successful syscalls, not power
// loss or writeback-error durability. Recovery attestations in the parent are
// fixture assumptions, never evidence that a same-boot restart is safe.
func TestCrashHelper(t *testing.T) {
	phase := os.Getenv("JOURNAL_CRASH_PHASE")
	if phase == "" {
		return
	}
	d := os.Getenv("JOURNAL_TEST_DIR")
	m := uint32(0077)
	c := Config{Directory: d, Records: "records", Inputs: "inputs", DeclaredUmask: &m, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: MaxPayload}
	cals := nativeCalls
	if phase == "create" {
		process.calls.open = func(d int, n string, f int, m uint32) (int, error) {
			fd, err := cals.open(d, n, f, m)
			if n == "journal" && err == nil {
				os.Exit(71)
			}
			return fd, err
		}
		_, _ = Initialize(c, []byte{'a'})
		t.Fatal("creation exit point not reached")
	}
	s, err := Initialize(c, []byte{'a'})
	must(t, err)
	switch phase {
	case "append-partial", "append-full":
		process.calls.write = func(fd int, p []byte, off int64) (int, error) {
			if phase == "append-partial" {
				p = p[:10]
			}
			n, err := cals.write(fd, p, off)
			if err == nil {
				os.Exit(71)
			}
			return n, err
		}
		_, _ = s.Append([]byte{'b'})
	case "append-sync":
		process.calls.sync = func(fd int) error {
			err := cals.sync(fd)
			if err == nil {
				os.Exit(71)
			}
			return err
		}
		_, _ = s.Append([]byte{'b'})
	case "input-partial":
		process.calls.write = func(fd int, p []byte, off int64) (int, error) {
			n, err := cals.write(fd, p[:1], off)
			if err == nil {
				os.Exit(71)
			}
			return n, err
		}
		_ = s.CreateInput("arg", []byte("abc"))
	case "temp-sync":
		process.calls.sync = func(fd int) error {
			err := cals.sync(fd)
			if err == nil {
				os.Exit(71)
			}
			return err
		}
		_ = s.ReplaceRecord("record", []byte("record"))
	case "rename":
		process.calls.rename = func(a int, x string, b int, y string) error {
			err := cals.rename(a, x, b, y)
			if err == nil {
				os.Exit(71)
			}
			return err
		}
		_ = s.ReplaceRecord("record", []byte("record"))
	case "record-dirsync":
		count := 0
		process.calls.sync = func(fd int) error {
			err := cals.sync(fd)
			count++
			if err == nil && count == 2 {
				os.Exit(71)
			}
			return err
		}
		_ = s.ReplaceRecord("record", []byte("record"))
	case "ack":
		_, err = s.Append([]byte{'b'})
		must(t, err)
		os.Exit(71)
	default:
		t.Fatal("unknown crash point")
	}
	t.Fatal("exit point not reached")
}

// Real process death releases ownership, but never authorizes automatic repair
// or same-boot recovery. Simulated recovery preserves tails, unacknowledged frames,
// partial inputs and stale temps; this is not a host-restart qualification.
func TestCrashBoundaries(t *testing.T) {
	for _, phase := range []string{"create", "append-partial", "append-full", "append-sync", "input-partial", "temp-sync", "rename", "record-dirsync", "ack"} {
		t.Run(phase, func(t *testing.T) {
			c, e := fixture(t, false)
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "JOURNAL_CRASH_PHASE="+phase, "JOURNAL_TEST_DIR="+c.Directory)
			out, err := cmd.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 71 {
				t.Fatalf("crash point not reached: %v\n%s", err, out)
			}
			before := disk(t, c)
			without := c
			without.RecoveryPrerequisiteEstablished = false
			_, err = e.enter(without, nil, consumerFuncs{}, false)
			wantClass(t, err, InvalidState)
			count := 0
			s, err := e.enter(c, nil, consumerFuncs{consume: func(uint64, []byte, *Recovery) error { count++; return nil }}, false)
			if phase == "create" || phase == "append-partial" {
				want := IncompleteTail
				if phase == "create" {
					want = MissingState
				}
				wantClass(t, err, want)
				if count != 0 || s != nil {
					t.Fatal("crashed incomplete journal admitted")
				}
				if !bytes.Equal(before, disk(t, c)) {
					t.Fatal("crashed history repaired")
				}
				return
			}
			must(t, err)
			defer s.Close()
			want := 1
			if strings.HasPrefix(phase, "append-") || phase == "ack" {
				want = 2
			}
			if count != want {
				t.Fatal("unacknowledged intent lost", count, want)
			}
			if phase == "input-partial" {
				err = s.CreateInput("arg", []byte("abc"))
				wantClass(t, err, InvalidState)
				b, err := os.ReadFile(filepath.Join(c.Directory, "inputs", "arg"))
				must(t, err)
				if string(b) != "a" {
					t.Fatal("crashed partial input changed")
				}
			}
			if phase == "temp-sync" {
				entries, err := os.ReadDir(filepath.Join(c.Directory, "records"))
				must(t, err)
				if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".record-tmp-") {
					t.Fatal("crashed temporary erased")
				}
				path := filepath.Join(c.Directory, "records", entries[0].Name())
				old, err := os.ReadFile(path)
				must(t, err)
				must(t, s.ReplaceRecord("record", []byte("new")))
				now, err := os.ReadFile(path)
				must(t, err)
				if !bytes.Equal(old, now) {
					t.Fatal("stale crash temporary changed")
				}
			}
			if !bytes.Equal(before, disk(t, c)) {
				t.Fatal("recovery rewrote committed history")
			}
		})
	}
}

// Failed initialization never yields a usable zero-frame session. Empty,
// partial and complete uncertain files remain exactly as the write attempt left.
func TestInitializationWriteFailures(t *testing.T) {
	for _, n := range []int{0, 10, 57} {
		t.Run(string(rune('A'+n)), func(t *testing.T) {
			c, e := fixture(t, true)
			native := e.calls.write
			calls := 0
			e.calls.write = func(fd int, p []byte, off int64) (int, error) {
				calls++
				if n > 0 {
					count, err := native(fd, p[:n], off)
					if err != nil {
						return count, err
					}
				}
				return n, syscall.EIO
			}
			s, err := e.enter(c, []byte{'a'}, nil, true)
			wantClass(t, err, StorageWriteFailure)
			if s != nil || calls != 1 {
				t.Fatal("failed initialization returned session or retried")
			}
			b := disk(t, c)
			if len(b) != n || !bytes.Equal(b, golden(t)[:n]) {
				t.Fatal("failed initialization bytes repaired")
			}
			e.calls.write = native
			_, err = e.enter(c, []byte{'a'}, nil, true)
			wantClass(t, err, InvalidState)
		})
	}
}
