//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type intakeResult struct {
	Error   string
	Size    int
	Elapsed time.Duration
}

// Re-exec only intake, on a locked thread so tgkill interrupts its ppoll.
// No helper, dispatcher, or privileged command is reachable from this child.
func TestIntakeGuardChild(t *testing.T) {
	if os.Getenv("DEMO_INTAKE_GUARD_CHILD") != "1" {
		return
	}
	runtime.LockOSThread()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	fmt.Println(syscall.Gettid())
	start := time.Now()
	b, err := intake(os.Stdin, 5*time.Second)
	r := intakeResult{Size: len(b), Elapsed: time.Since(start)}
	if err != nil {
		r.Error = err.Error()
	}
	json.NewEncoder(os.Stdout).Encode(r)
	os.Exit(0)
}

// The parent bounds a stuck blocking read independently of the child. Return a
// sentinel on timeout so the particular witness owns the failure assertion.
func guardedIntake(t *testing.T, input *os.File, during func(int, int)) intakeResult {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(exe, "-test.run=^TestIntakeGuardChild$")
	c.Env = []string{"DEMO_INTAKE_GUARD_CHILD=1", "GORACE=atexit_sleep_ms=0"}
	c.Stdin = input
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	c.Stderr = os.Stderr
	if err = c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	rd := bufio.NewReader(out)
	ready := make(chan string, 1)
	go func() { line, _ := rd.ReadString('\n'); ready <- line }()
	var line string
	select {
	case line = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("intake child failed to start")
	}
	tid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal("intake thread identity", line, err)
	}
	if during != nil {
		during(c.Process.Pid, tid)
	}
	done := make(chan intakeResult, 1)
	go func() {
		var result intakeResult
		if err := json.NewDecoder(rd).Decode(&result); err != nil {
			result.Error = err.Error()
		}
		done <- result
	}()
	select {
	case result := <-done:
		return result
	case <-time.After(8 * time.Second):
		return intakeResult{Error: "outer deadline", Elapsed: 8 * time.Second}
	}
}

// 1.5 s scheduling margin over the literal five-second contract, below a
// doubled budget. The child's clock excludes exec/race startup. EOF is a
// positive control; stalled, partial progress and thread-directed EINTR must
// all retain the original deadline. Writers/signallers are joined on cleanup.
func TestIntakeOriginalBudget(t *testing.T) {
	for _, variant := range []string{"eof", "stalled", "trickle", "eintr"} {
		t.Run(variant, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			stop, workerDone := make(chan struct{}), make(chan struct{})
			var sent atomic.Int64
			if variant == "eof" {
				w.Write([]byte("control"))
				w.Close()
			}
			res := guardedIntake(t, r, func(pid, tid int) {
				go func() {
					defer close(workerDone)
					tick := time.NewTicker(20 * time.Millisecond)
					defer tick.Stop()
					for {
						select {
						case <-stop:
							return
						case <-tick.C:
							if variant == "eintr" {
								if syscall.Tgkill(pid, tid, syscall.SIGWINCH) != nil {
									return
								}
								sent.Add(1)
							}
							if variant == "trickle" {
								if _, err := w.Write([]byte("x")); err != nil {
									return
								}
							}
						}
					}
				}()
			})
			close(stop)
			<-workerDone
			if variant == "eof" {
				if res.Error != "" || res.Size != 7 {
					t.Fatalf("intake EOF control: %+v", res)
				}
				return
			}
			if variant == "eintr" && sent.Load() < 10 {
				t.Fatal("intake signal stimulus missing")
			}
			if res.Error != os.ErrDeadlineExceeded.Error() || res.Elapsed < 4900*time.Millisecond || res.Elapsed > 6500*time.Millisecond {
				t.Fatalf("intake original budget exceeded (%s): %+v", variant, res)
			}
		})
	}
}

// A queued byte below SO_RCVLOWAT must not trap the post-poll read. A peer
// shutdown with data is the positive control for readiness and EOF handling.
func TestIntakeSocketReadBound(t *testing.T) {
	for _, eof := range []bool{false, true} {
		t.Run(map[bool]string{true: "eof", false: "low-water"}[eof], func(t *testing.T) {
			fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			r, w := os.NewFile(uintptr(fds[0]), "socket-reader"), os.NewFile(uintptr(fds[1]), "socket-writer")
			defer r.Close()
			defer w.Close()
			if err := syscall.SetsockoptInt(fds[0], syscall.SOL_SOCKET, syscall.SO_RCVLOWAT, 5); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if eof {
				if err := syscall.Shutdown(fds[1], syscall.SHUT_WR); err != nil {
					t.Fatal(err)
				}
			}
			res := guardedIntake(t, r, nil)
			if eof {
				if res.Error != "" || res.Size != 1 {
					t.Fatalf("socket EOF control: %+v", res)
				}
			} else if res.Error != os.ErrDeadlineExceeded.Error() || res.Elapsed < 4900*time.Millisecond || res.Elapsed > 6500*time.Millisecond {
				t.Fatalf("socket intake read exceeded budget: %+v", res)
			}
		})
	}
}

// No pipe resizing or privilege is needed: drain the residual stream after
// intake returns, allowing the writer to finish. Exact cap+1 consumption is
// independent of kernel pipe capacity. The exact-cap CLI test is the control.
func TestIntakePipeConsumption(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	const total = 200000
	written := make(chan error, 1)
	go func() { _, err := w.Write(bytes.Repeat([]byte("x"), total)); w.Close(); written <- err }()
	res := guardedIntake(t, r, nil)
	left, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if res.Error != errIntakeLimit.Error() || res.Size != 0 || total-len(left) != 131073 {
		t.Fatalf("pipe intake consumed beyond cap+1: consumed=%d result=%+v", total-len(left), res)
	}
}

// The unsupported-ioctl fallback still refuses /dev/zero at the cap; /dev/null
// is the zero-byte EOF control. Both devices are opened read-only, non-root.
func TestIntakeDeviceFallback(t *testing.T) {
	for _, path := range []string{"/dev/null", "/dev/zero"} {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		res := guardedIntake(t, f, nil)
		f.Close()
		want := ""
		if path == "/dev/zero" {
			want = errIntakeLimit.Error()
		}
		if res.Error != want || res.Size != 0 {
			t.Fatalf("device intake fallback: %s %+v", path, res)
		}
	}
}

// VMIN>0 plus VTIME>0 can extend a large blocking read on every trickled byte.
// A 400 ms trickle stays below the five-second inter-byte timer and keeps the
// fixed-size read plant beyond the parent's eight-second bound. The assertion
// requires the original five-second timeout (1.5 s scheduling margin), without
// depending on the kernel's eventual read size. This covers one private raw pty;
// skip only if a non-root pty is unavailable, and record that qualification gap.
func TestIntakePTYTrickle(t *testing.T) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("pty unavailable:", err)
	}
	defer m.Close()
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Skip("pty unlock unavailable:", e)
	}
	var number uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); e != 0 {
		t.Fatal(e)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(number)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("pty slave unavailable:", err)
	}
	defer s.Close()
	var term syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, s.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&term))); e != 0 {
		t.Fatal(e)
	}
	term.Iflag = 0
	term.Oflag = 0
	term.Lflag = 0
	term.Cc[syscall.VMIN], term.Cc[syscall.VTIME] = 255, 50
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, s.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&term))); e != 0 {
		t.Fatal(e)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	res := guardedIntake(t, s, func(int, int) {
		go func() {
			defer close(done)
			tick := time.NewTicker(400 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					if _, err := m.Write([]byte("x")); err != nil {
						return
					}
				}
			}
		}()
	})
	close(stop)
	<-done
	if res.Error != os.ErrDeadlineExceeded.Error() || res.Elapsed < 4900*time.Millisecond || res.Elapsed > 6500*time.Millisecond {
		t.Fatalf("pty intake read exceeded budget: %+v", res)
	}
}

// A source-level prohibition catches transient flag mutations between fdinfo
// samples. Inspect identifiers, excluding comments/strings. This deliberately
// forbids the four standard flag-setting APIs/tokens in this small source file;
// runtime success/timeout/signal tests remain the behavioral controls.
func TestIntakeNeverSetsFlags(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "intake_linux.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			switch id.Name {
			case "F_SETFL", "SetNonblock", "FIONBIO", "O_NONBLOCK":
				t.Errorf("intake mutates shared descriptor flags: %s", id.Name)
			}
		}
		return true
	})
}
