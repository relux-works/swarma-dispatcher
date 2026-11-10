//go:build linux && amd64

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Trap flag writes on all threads, regardless of the calling package. This
// amd64-only instrument covers F_SETFL and FIONBIO, not every descriptor API.
// Controls below require SIGSYS from each forbidden syscall, never a timeout
// or setup failure. No dump is produced by these intentionally killed children.
func armIntakeFlagTrap() error {
	const load, equal, ret = 0x20, 0x15, 0x06
	const allow, kill = 0x7fff0000, 0x80000000
	filters := []syscall.SockFilter{
		{Code: load, K: 4}, // seccomp_data.arch
		{Code: equal, Jt: 1, K: 0xc000003e},
		{Code: ret, K: kill}, // refuse unexpected ABI
		{Code: load, K: 0},   // seccomp_data.nr
		{Code: equal, Jt: 2, K: syscall.SYS_FCNTL},
		{Code: equal, Jt: 4, K: syscall.SYS_IOCTL},
		{Code: ret, K: allow},
		{Code: load, K: 24}, // low word of args[1]
		{Code: equal, Jt: 4, K: syscall.F_SETFL},
		{Code: ret, K: allow},
		{Code: load, K: 24},
		{Code: equal, Jt: 1, K: 0x5421}, // FIONBIO
		{Code: ret, K: allow},
		{Code: ret, K: kill},
	}
	prog := syscall.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, 4, 0, 0); e != 0 {
		return e
	} // PR_SET_DUMPABLE
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, 38, 1, 0); e != 0 {
		return e
	} // PR_SET_NO_NEW_PRIVS
	// seccomp(SET_MODE_FILTER, FILTER_FLAG_TSYNC, prog); positive returns are
	// failed thread IDs, not successful installation.
	r, _, e := syscall.RawSyscall(317, 1, 1, uintptr(unsafe.Pointer(&prog)))
	if e != 0 {
		return e
	}
	if r != 0 {
		return fmt.Errorf("filter synchronization failed: %d", r)
	}
	return nil
}

func TestIntakeFlagTrapChild(t *testing.T) {
	mode := os.Getenv("DEMO_INTAKE_FLAG_TRAP")
	if mode == "" {
		return
	}
	if err := armIntakeFlagTrap(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	switch mode {
	case "fcntl-control":
		syscall.RawSyscall(syscall.SYS_FCNTL, 0, syscall.F_SETFL, 0)
		os.Exit(4)
	case "ioctl-control":
		on := int32(1)
		syscall.RawSyscall(syscall.SYS_IOCTL, 0, 0x5421, uintptr(unsafe.Pointer(&on)))
		os.Exit(4)
	}
	b, err := intake(os.Stdin, time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(5)
	}
	fmt.Printf("payload=%s\n", b)
	os.Exit(0)
}

// Successful pipe and blocking socket intake are positive controls for the
// same filter that detects both deliberate violations. A transient cross-package
// write is observed even when it restores the flags before returning. This does
// not qualify other architectures or paths not exercised by these EOF fixtures.
func TestIntakeNoFlagWrites(t *testing.T) {
	for _, mode := range []string{"pipe", "socket", "fcntl-control", "ioctl-control"} {
		t.Run(mode, func(t *testing.T) {
			var r, w *os.File
			if mode == "socket" {
				fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				r, w = os.NewFile(uintptr(fds[0]), "reader"), os.NewFile(uintptr(fds[1]), "writer")
			} else {
				var err error
				r, w, err = os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
			}
			defer r.Close()
			defer w.Close()
			_ = r.Fd() // Make the pipe blocking before inheritance, outside the filter.
			if _, err := w.Write([]byte("control")); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, exe, "-test.run=^TestIntakeFlagTrapChild$")
			c.Env = []string{"DEMO_INTAKE_FLAG_TRAP=" + mode, "GORACE=atexit_sleep_ms=0"}
			c.Stdin = r
			out, err := c.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("flag trap child exceeded outer bound: %v", ctx.Err())
			}
			if c.ProcessState == nil {
				t.Fatalf("flag trap child failed to start: %v", err)
			}
			status, ok := c.ProcessState.Sys().(syscall.WaitStatus)
			trapped := ok && status.Signaled() && status.Signal() == syscall.SIGSYS
			if mode == "fcntl-control" || mode == "ioctl-control" {
				if !trapped {
					t.Fatalf("flag trap missed forbidden control: %s %v %s", mode, err, out)
				}
				return
			}
			if trapped {
				t.Fatal("intake issued forbidden flag write")
			}
			if err != nil || string(out) != "payload=control\n" {
				t.Fatalf("flag trap intake control refused: %v %q", err, out)
			}
		})
	}
}
