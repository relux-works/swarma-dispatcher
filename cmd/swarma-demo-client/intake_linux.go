//go:build linux

package main

import (
	"io"
	"os"
	"syscall"
	"time"
)

// Rewrap a duplicate only after making a blocking inherited pipe nonblocking;
// os.NewFile then registers it with the runtime poller. No reader goroutine can
// survive a deadline. This standalone CLI owns stdin and its open description.
func intake(input *os.File, budget time.Duration) ([]byte, error) {
	info, err := input.Stat()
	if err != nil {
		return nil, err
	}
	reader := input
	if !info.Mode().IsRegular() {
		var fd int
		raw, e := input.SyscallConn()
		if e != nil {
			return nil, e
		}
		e = raw.Control(func(n uintptr) {
			r, _, x := syscall.Syscall(syscall.SYS_FCNTL, n, syscall.F_DUPFD_CLOEXEC, 0)
			if x != 0 {
				err = x
			} else {
				fd = int(r)
			}
		})
		if e != nil {
			return nil, e
		}
		if err != nil {
			return nil, err
		}
		if err = syscall.SetNonblock(fd, true); err != nil {
			syscall.Close(fd)
			return nil, err
		}
		reader = os.NewFile(uintptr(fd), "bounded stdin")
		defer reader.Close()
		if err = reader.SetReadDeadline(time.Now().Add(budget)); err != nil {
			return nil, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(reader, 131073))
	if err != nil {
		return nil, err
	}
	if len(b) > 131072 {
		return nil, errIntakeLimit
	}
	return b, nil
}
