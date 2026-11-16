//go:build linux

package hygiene

import "syscall"

func cloexec(fd int) (bool, error) {
	flags, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	if e != 0 {
		return false, e
	}
	return flags&syscall.FD_CLOEXEC != 0, nil
}
