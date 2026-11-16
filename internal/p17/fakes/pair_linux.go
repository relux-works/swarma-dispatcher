//go:build linux

// Package fakes is hermetic test support for the P17 clients: connected
// socket pairs, a fake Source and a fake Keymaker driven by scripts, the
// test-only wire spellings those fakes speak, a fake procfs tree and a fake
// uid killer.
//
// Nothing here is a wire codec of Source or Keymaker. The test wires are a
// local JSON spelling over the repository's four-byte framing; the pinned
// codecs (see source.Pin and keymaker.Pin) replace them in the clients once
// vendored. Production code must not import this package.
package fakes

import (
	"net"
	"os"
	"syscall"
)

// SocketPair returns two connected AF_UNIX stream sockets, both close-on-exec.
func SocketPair() (net.Conn, net.Conn, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	fa := os.NewFile(uintptr(fds[0]), "pair-a")
	fb := os.NewFile(uintptr(fds[1]), "pair-b")
	a, err := net.FileConn(fa)
	_ = fa.Close()
	if err != nil {
		_ = fb.Close()
		return nil, nil, err
	}
	b, err := net.FileConn(fb)
	_ = fb.Close()
	if err != nil {
		_ = a.Close()
		return nil, nil, err
	}
	return a, b, nil
}

// PeerUID reads SO_PEERCRED of a unix connection.
func PeerUID(c net.Conn) (uint32, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, syscall.ENOTSOCK
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var call error
	if err = raw.Control(func(fd uintptr) {
		var cred *syscall.Ucred
		cred, call = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if call == nil {
			uid = cred.Uid
		}
	}); err != nil {
		return 0, err
	}
	return uid, call
}

// RawFD returns the descriptor number of a unix connection, for descriptor
// hygiene checks. It does not duplicate or detach it.
func RawFD(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1, syscall.ENOTSOCK
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1, err
	}
	fd := -1
	if err = raw.Control(func(f uintptr) { fd = int(f) }); err != nil {
		return -1, err
	}
	return fd, nil
}
