//go:build linux

package m0client

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// denyProtectionCall installs a child-thread-only, unprivileged filter. The
// locked test thread runs the caller; no parent process or host setting changes.
func denyProtectionCall(t *testing.T, number uintptr) {
	t.Helper()
	if _, _, err := syscall.RawSyscall6(syscall.SYS_PRCTL, 38, 1, 0, 0, 0, 0); err != 0 {
		t.Fatal("no-new-privileges setup", err)
	}
	filter := []syscall.SockFilter{
		{Code: 0x20, K: 0}, // Load the native syscall number.
		{Code: 0x15, K: uint32(number), Jf: 1},
		{Code: 0x06, K: 0x00050000 | uint32(syscall.EPERM)},
		{Code: 0x06, K: 0x7fff0000},
	}
	program := syscall.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	// PR_SET_SECCOMP, SECCOMP_MODE_FILTER. The filter is not synchronized
	// onto other threads; runtime.LockOSThread keeps this test on its thread.
	_, _, err := syscall.RawSyscall6(syscall.SYS_PRCTL, 22, 2, uintptr(unsafe.Pointer(&program)), 0, 0, 0)
	runtime.KeepAlive(filter)
	if err != 0 {
		t.Fatal("unprivileged protection-denial filter", err)
	}
}

// Claim: dumpability and both core limits are zero before a seed read; denied prctl or setrlimit returns 64 before a seed open, beside an accepted subprocess control.
func TestKeyProcessProtection(t *testing.T) {
	mode := os.Getenv("CALLER_TEST_PROTECTION")
	if mode == "" {
		_ = newEnv(t)
		for _, denied := range []string{"prctl", "setrlimit"} {
			t.Run(denied, func(t *testing.T) {
				for _, child := range []string{"control", denied} {
					cmd := exec.Command(os.Args[0], "-test.run=^TestKeyProcessProtection$", "-test.v")
					cmd.Env = append(os.Environ(), "CALLER_TEST_PROTECTION="+child)
					output, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("%s subprocess: %v\n%s", child, err, output)
					}
					t.Logf("%s subprocess:\n%s", child, output)
				}
			})
		}
		return
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e := newEnv(t)
	l := e.listen(modeAccept)
	original := e.host
	opened := false
	e.host.stat = func(fd int) (syscall.Stat_t, error) {
		st, err := original.fstat(fd)
		name, _ := os.Readlink("/proc/self/fd/" + fdString(fd))
		if err == nil && name == e.keyPath {
			opened = true
			if mode == "control" {
				dumpable, _, call := syscall.RawSyscall6(syscall.SYS_PRCTL, syscall.PR_GET_DUMPABLE, 0, 0, 0, 0, 0)
				var limit syscall.Rlimit
				if call != 0 || dumpable != 0 || syscall.Getrlimit(syscall.RLIMIT_CORE, &limit) != nil || limit.Cur != 0 || limit.Max != 0 {
					t.Fatal("key opened before dump protection", dumpable, call, limit)
				}
			}
		}
		return st, err
	}
	switch mode {
	case "prctl":
		denyProtectionCall(t, syscall.SYS_PRCTL)
	case "setrlimit":
		// The standard library implements Setrlimit with prlimit64 on Linux.
		denyProtectionCall(t, syscall.SYS_PRLIMIT64)
	case "control":
	default:
		t.Fatal("unknown protection subprocess mode")
	}
	code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
	connections := len(l.drain())
	t.Logf("tool exit=%d; seed-open observed=%v", code, opened)
	if mode == "control" {
		if code != ExitAccepted || !opened || stderr != "" || e.storedCount() != 1 || connections != 1 {
			t.Fatalf("protected control: exit %d stderr %q", code, stderr)
		}
		return
	}
	if code != ExitLocal || stdout != "" || opened || !strings.Contains(stderr, reasonKeyFileUnsafe) || !strings.Contains(stderr, syscall.EPERM.Error()) || e.storedCount() != 0 || connections != 0 {
		t.Fatalf("protection refusal: exit %d stderr %q opened %v connections %d", code, stderr, opened, connections)
	}
}
