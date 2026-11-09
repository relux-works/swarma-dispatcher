//go:build linux

package main

import (
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Poll the inherited blocking description without changing its flags. Closing a
// duplicate cannot restore flags after a signal, so even temporary F_SETFL is
// forbidden. As with ordinary stdin, the caller must not concurrently consume
// this stream. Only S_IFREG files take the bounded synchronous read path.
// A terminal hangup is refused with EIO rather than treated as a clean EOF.
func intake(input *os.File, budget time.Duration) ([]byte, error) {
	info, err := input.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, syscall.EINVAL
	}
	if stat.Mode&syscall.S_IFMT == syscall.S_IFREG {
		b, err := io.ReadAll(io.LimitReader(input, 131073))
		if err != nil {
			return nil, err
		}
		if len(b) > 131072 {
			return nil, errIntakeLimit
		}
		return b, nil
	}
	var fd int
	raw, err := input.SyscallConn()
	if err != nil {
		return nil, err
	}
	var dupErr syscall.Errno
	err = raw.Control(func(n uintptr) {
		r, _, e := syscall.Syscall(syscall.SYS_FCNTL, n, syscall.F_DUPFD_CLOEXEC, 0)
		fd, dupErr = int(r), e
	})
	if err != nil {
		return nil, err
	}
	if dupErr != 0 {
		return nil, dupErr
	}
	defer syscall.Close(fd)
	deadline := time.Now().Add(budget)
	var result []byte
	var buf [131073]byte
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, os.ErrDeadlineExceeded
		}
		// ppoll is poll(2) with a timespec timeout; available on Linux amd64
		// and arm64. EINTR always recomputes the original remaining budget.
		p := struct {
			FD      int32
			Events  int16
			Revents int16
		}{FD: int32(fd), Events: 1}
		ts := syscall.NsecToTimespec(remaining.Nanoseconds())
		n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&p)), 1, uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return nil, errno
		}
		if n == 0 {
			return nil, os.ErrDeadlineExceeded
		}
		if p.Revents&32 != 0 {
			return nil, syscall.EBADF
		}
		// Read only queued bytes: readiness alone does not bound a blocking
		// socket/terminal read (SO_RCVLOWAT and VMIN/VTIME still apply).
		var available int32
		_, _, queryErr := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCINQ, uintptr(unsafe.Pointer(&available)))
		size := 32768 // bounded fallback for devices without FIONREAD
		if queryErr == 0 {
			size = max(1, int(available)) // one byte observes EOF
		} else if queryErr != syscall.ENOTTY && queryErr != syscall.EINVAL {
			if queryErr == syscall.EINTR {
				continue
			}
			return nil, queryErr
		}
		size = min(size, 131073-len(result))
		var count int
		var e error
		if stat.Mode&syscall.S_IFMT == syscall.S_IFSOCK {
			// TIOCINQ can include urgent bytes that a normal read skips.
			// Per-call nonblocking receive preserves the shared flags.
			count, _, e = syscall.Recvfrom(fd, buf[:size], syscall.MSG_DONTWAIT)
		} else {
			count, e = syscall.Read(fd, buf[:size])
		}
		if e == syscall.EINTR || e == syscall.EAGAIN {
			continue
		}
		if e != nil {
			return nil, e
		}
		if count == 0 {
			return result, nil
		}
		if len(result)+count > 131072 {
			return nil, errIntakeLimit
		}
		result = append(result, buf[:count]...)
	}
}
