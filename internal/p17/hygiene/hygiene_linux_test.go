//go:build linux

package hygiene_test

import (
	"errors"
	"strconv"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/hygiene"
)

func pairFDs(t *testing.T) (int, int) {
	t.Helper()
	a, b, err := fakes.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	fa, err := fakes.RawFD(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := fakes.RawFD(b)
	if err != nil {
		t.Fatal(err)
	}
	return fa, fb
}

// Claim (H3): a launch whose Keymaker and Source descriptors are close-on-exec
// and whose argv and env carry neither protected paths nor descriptor
// references passes.
func TestAssertClean(t *testing.T) {
	km, src := pairFDs(t)
	err := hygiene.Assert(hygiene.Launch{
		Argv:           []string{"/usr/bin/agent", "--run"},
		Env:            []string{"PATH=/usr/bin", "HOME=/home/agent"},
		ProtectedPaths: []string{"/run/source/origin.sock", "/var/lib/launcher"},
		ProtectedFDs:   []int{km, src},
		InheritedFDs:   []int{0, 1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Claim (H3, plant): a protected descriptor without FD_CLOEXEC refuses with
// ErrNotCloexec; the same descriptor with the flag set is the positive
// control inside this test.
func TestAssertNotCloexec(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0) // deliberately without SOCK_CLOEXEC
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fds[0]); _ = syscall.Close(fds[1]) })
	l := hygiene.Launch{Argv: []string{"agent"}, Env: []string{"A=b"}, ProtectedFDs: []int{fds[0]}}
	if err := hygiene.Assert(l); !errors.Is(err, hygiene.ErrNotCloexec) {
		t.Fatalf("plant: %v", err)
	}
	syscall.CloseOnExec(fds[0])
	if err := hygiene.Assert(l); err != nil {
		t.Fatalf("positive control: %v", err)
	}
}

// Claim (H3): a protected descriptor listed for inheritance, a protected path
// in argv or env, a /proc/self/fd or /dev/fd reference to a protected
// descriptor, and an env entry without '=' each refuse with their own error.
func TestAssertLeaks(t *testing.T) {
	km, _ := pairFDs(t)
	base := hygiene.Launch{Argv: []string{"agent"}, Env: []string{"A=b"}, ProtectedPaths: []string{"/run/source/origin.sock"}, ProtectedFDs: []int{km}}
	cases := []struct {
		name string
		mut  func(*hygiene.Launch)
		want error
	}{
		{"inherited keymaker fd", func(l *hygiene.Launch) { l.InheritedFDs = []int{km} }, hygiene.ErrInherited},
		{"path in argv", func(l *hygiene.Launch) { l.Argv = []string{"agent", "--sock=/run/source/origin.sock"} }, hygiene.ErrLeak},
		{"path in env", func(l *hygiene.Launch) { l.Env = []string{"SOURCE=/run/source/origin.sock"} }, hygiene.ErrLeak},
		{"proc fd ref in env", func(l *hygiene.Launch) { l.Env = []string{"KM=/proc/self/fd/" + strconv.Itoa(km)} }, hygiene.ErrLeak},
		{"dev fd ref in argv", func(l *hygiene.Launch) { l.Argv = []string{"agent", "/dev/fd/" + strconv.Itoa(km)} }, hygiene.ErrLeak},
		{"env without equals", func(l *hygiene.Launch) { l.Env = []string{"BROKEN"} }, hygiene.ErrEnvShape},
		{"empty protected path", func(l *hygiene.Launch) { l.ProtectedPaths = []string{""} }, hygiene.ErrLeak},
	}
	for _, c := range cases {
		l := base
		c.mut(&l)
		if err := hygiene.Assert(l); !errors.Is(err, c.want) || !errors.Is(err, hygiene.ErrHygiene) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if err := hygiene.Assert(base); err != nil {
		t.Fatalf("positive control: %v", err)
	}
}
