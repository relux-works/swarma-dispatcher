// Package hygiene asserts the H3 interface before the launch path spawns
// R_i: the Keymaker descriptor, the Source socket and every L-owned path are
// close-on-exec and absent from the child's environment and argv.
//
// It is an assertion for the S2-M0 launch path, not a sanitizer: it refuses
// and never edits argv, env or descriptor flags.
package hygiene

import (
	"errors"
	"strconv"
	"strings"
)

// Launch is what the child is about to receive.
type Launch struct {
	Argv []string
	Env  []string
	// ProtectedPaths are L-owned paths and the Source socket path.
	ProtectedPaths []string
	// ProtectedFDs are descriptors the child must not see: the Keymaker
	// connection, the Source socket, journal descriptors.
	ProtectedFDs []int
	// InheritedFDs are descriptors the launch path intends to pass on.
	InheritedFDs []int
}

var (
	// ErrHygiene is the root of every refusal here.
	ErrHygiene = errors.New("hygiene: child interface violation")
	// ErrNotCloexec reports a protected descriptor without FD_CLOEXEC.
	ErrNotCloexec = errors.New("hygiene: protected descriptor not close-on-exec")
	// ErrInherited reports a protected descriptor in the inherited set.
	ErrInherited = errors.New("hygiene: protected descriptor passed to child")
	// ErrLeak reports a protected path or descriptor reference in argv or env.
	ErrLeak = errors.New("hygiene: protected path or descriptor in argv or env")
	// ErrEnvShape reports an environment entry without KEY=VALUE shape.
	ErrEnvShape = errors.New("hygiene: environment entry without '='")
)

func fail(root error, what string) error {
	return errors.Join(ErrHygiene, root, errors.New("hygiene: "+what))
}

// Assert checks the launch. Descriptor flags are read with fcntl(F_GETFD).
func Assert(l Launch) error {
	inherited := make(map[int]bool, len(l.InheritedFDs))
	for _, fd := range l.InheritedFDs {
		inherited[fd] = true
	}
	for _, fd := range l.ProtectedFDs {
		if fd < 0 {
			return fail(ErrNotCloexec, "negative protected descriptor")
		}
		if inherited[fd] {
			return fail(ErrInherited, "fd "+strconv.Itoa(fd))
		}
		ok, err := cloexec(fd)
		if err != nil {
			return fail(ErrNotCloexec, "fd "+strconv.Itoa(fd)+": "+err.Error())
		}
		if !ok {
			return fail(ErrNotCloexec, "fd "+strconv.Itoa(fd))
		}
	}
	needles := make([]string, 0, len(l.ProtectedPaths)+2*len(l.ProtectedFDs))
	for _, p := range l.ProtectedPaths {
		if p == "" {
			return fail(ErrLeak, "empty protected path")
		}
		needles = append(needles, p)
	}
	for _, fd := range l.ProtectedFDs {
		n := strconv.Itoa(fd)
		needles = append(needles, "/proc/self/fd/"+n, "/dev/fd/"+n)
	}
	for i, a := range l.Argv {
		for _, n := range needles {
			if strings.Contains(a, n) {
				return fail(ErrLeak, "argv["+strconv.Itoa(i)+"]")
			}
		}
	}
	// An environment entry may itself be a secret (for example a token
	// appended without KEY=), so refusals name the index only and never copy
	// any part of the entry.
	for i, e := range l.Env {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			return fail(ErrEnvShape, "env["+strconv.Itoa(i)+"]")
		}
		for _, n := range needles {
			if strings.Contains(e, n) {
				return fail(ErrLeak, "env["+strconv.Itoa(i)+"]")
			}
		}
	}
	return nil
}
