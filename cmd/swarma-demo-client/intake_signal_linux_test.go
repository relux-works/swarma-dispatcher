//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Re-exec runs only intake. A byte consumed from the shared pipe establishes
// that intake has begun before either signal; no scheduling sleep is evidence.
func TestSignalIntakeChild(t *testing.T) {
	if os.Getenv("DEMO_SIGNAL_INTAKE") != "1" {
		return
	}
	_, _ = intake(os.Stdin, 30*time.Second)
	os.Exit(42)
}
func TestIntakeSignalFlags(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			fd := r.Fd()
			flags := func() string {
				b, e := os.ReadFile("/proc/self/fdinfo/" + fmt.Sprint(fd))
				if e != nil {
					t.Fatal(e)
				}
				for _, line := range strings.Split(string(b), "\n") {
					if strings.HasPrefix(line, "flags:") {
						return line
					}
				}
				t.Fatal("flags absent")
				return ""
			}
			before := flags()
			if _, e := w.Write([]byte("x")); e != nil {
				t.Fatal(e)
			}
			exe, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			c := exec.Command(exe, "-test.run=^TestSignalIntakeChild$")
			c.Env = []string{"DEMO_SIGNAL_INTAKE=1", "GORACE=atexit_sleep_ms=0"}
			c.Stdin = r
			if e = c.Start(); e != nil {
				t.Fatal(e)
			}
			waited := false
			defer func() {
				if !waited {
					_ = c.Process.Kill()
					_ = c.Wait()
				}
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				var available int32
				_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCINQ, uintptr(unsafe.Pointer(&available)))
				if errno != 0 {
					t.Fatal(errno)
				}
				if available == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("intake never consumed synchronization byte")
				}
				time.Sleep(time.Millisecond)
			}
			during := flags()
			if e = c.Process.Signal(sig); e != nil {
				t.Fatal(e)
			}
			e = c.Wait()
			waited = true
			status, ok := c.ProcessState.Sys().(syscall.WaitStatus)
			if e == nil || !ok || !status.Signaled() || status.Signal() != sig {
				t.Fatalf("intake did not terminate on signal: %v %v", status, e)
			}
			after := flags()
			if before != during || before != after {
				t.Fatalf("signal intake changed shared flags: before=%s during=%s after=%s", before, during, after)
			}
		})
	}
}
