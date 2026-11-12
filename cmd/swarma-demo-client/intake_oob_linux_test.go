//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A pending AF_UNIX urgent byte can make TIOCINQ overcount ordinary data.
// Held peers must still yield the five-second refusal, never late acceptance.
// The normal request plus peer shutdown is the acceptance control. This uses
// only private socketpairs; an unsupported MSG_OOB is a failure, not a skip.
func TestIntakeSocketUrgent(t *testing.T) {
	const request = `{"op":"demo.status","account":"demo"}`
	for _, variant := range []string{"normal", "urgent-only", "request-urgent", "late-urgent"} {
		t.Run(variant, func(t *testing.T) {
			fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			r, w := os.NewFile(uintptr(fds[0]), "reader"), os.NewFile(uintptr(fds[1]), "writer")
			defer r.Close()
			defer w.Close()
			if variant != "urgent-only" && variant != "late-urgent" {
				if _, err := w.Write([]byte(request)); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "normal" {
				if err := syscall.Shutdown(fds[1], syscall.SHUT_WR); err != nil {
					t.Fatal(err)
				}
			} else if variant != "late-urgent" {
				if err := syscall.Sendto(fds[1], []byte("!"), syscall.MSG_OOB, nil); err != nil {
					t.Fatal("urgent-data fixture unavailable:", err)
				}
			}
			// The child readiness callback runs immediately before intake. A late
			// urgent byte must not buy another five seconds after EAGAIN.
			var during func(int, int)
			if variant == "late-urgent" {
				during = func(_, _ int) {
					time.Sleep(3 * time.Second)
					if err := syscall.Sendto(fds[1], []byte("!"), syscall.MSG_OOB, nil); err != nil {
						t.Fatal("late urgent fixture failed:", err)
					}
				}
			}
			result := guardedIntake(t, r, during)
			if variant == "normal" {
				if result.Error != "" || result.Size != len(request) {
					t.Fatalf("socket request control refused: %+v", result)
				}
			} else if result.Error != os.ErrDeadlineExceeded.Error() || result.Size != 0 || result.Elapsed < 4900*time.Millisecond || result.Elapsed > 6500*time.Millisecond {
				if variant == "late-urgent" {
					t.Fatalf("socket late urgent refreshed budget: %+v", result)
				}
				t.Fatalf("socket urgent intake exceeded budget: %+v", result)
			}
		})
	}
}

// eventfd's zero S_IFMT looks regular through Go FileMode, but must not enter
// the untimed synchronous file read. A real regular file is the nearby control.
// An empty eventfd stays unreadable and must time out.
func TestIntakeAnonymousDescriptor(t *testing.T) {
	fd, _, errno := syscall.Syscall(syscall.SYS_EVENTFD2, 0, syscall.O_CLOEXEC, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	f := os.NewFile(fd, "event")
	defer f.Close()
	r := guardedIntake(t, f, nil)
	if r.Error != os.ErrDeadlineExceeded.Error() || r.Size != 0 || r.Elapsed < 4900*time.Millisecond || r.Elapsed > 6500*time.Millisecond {
		t.Fatalf("anonymous stdin entered untimed path: %+v", r)
	}
	path := filepath.Join(t.TempDir(), "request")
	if err := os.WriteFile(path, []byte("control"), 0600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	r = guardedIntake(t, regular, nil)
	if r.Error != "" || r.Size != 7 {
		t.Fatalf("regular stdin control refused: %+v", r)
	}
}
