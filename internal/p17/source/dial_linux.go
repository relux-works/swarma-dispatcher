//go:build linux

package source

import (
	"errors"
	"net"
	"syscall"
	"time"
)

// UnixDialer connects to the control socket with a connect timeout and reads
// the kernel peer credential with SO_PEERCRED. The socket is close-on-exec
// (Go sets it on every socket it creates).
type UnixDialer struct {
	Timeout time.Duration
}

type unixConn struct {
	*net.UnixConn
}

func (u unixConn) Peer() (Credential, error) {
	raw, err := u.SyscallConn()
	if err != nil {
		return Credential{}, err
	}
	var cred *syscall.Ucred
	var call error
	if err = raw.Control(func(fd uintptr) {
		cred, call = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return Credential{}, err
	}
	if call != nil {
		return Credential{}, call
	}
	if cred == nil {
		return Credential{}, errors.New("source: no peer credential")
	}
	return Credential{UID: cred.Uid, GID: cred.Gid, PID: cred.Pid}, nil
}

func (d UnixDialer) Dial(path string) (Conn, error) {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultBudget
	}
	nd := net.Dialer{Timeout: timeout}
	c, err := nd.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, errors.New("source: not a unix connection")
	}
	return unixConn{uc}, nil
}
