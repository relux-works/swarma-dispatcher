//go:build linux

package m0client

import (
	"os"
	"syscall"
)

// host is the OS boundary. The zero value always uses native credentials,
// fstat and the fixed endpoint. Tests can model protected ownership and a
// managed caller without root, chown or provisioning the production path.
// No command-line, configuration or environment value can select these seams.
type host struct {
	stat      func(int) (syscall.Stat_t, error)
	uid, euid func() int
	endpoint  string
}

func (h host) fstat(fd int) (syscall.Stat_t, error) {
	if h.stat != nil {
		return h.stat(fd)
	}
	return fstat(fd)
}
func (h host) realUID() uint32 {
	if h.uid != nil {
		return uint32(h.uid())
	}
	return uint32(os.Getuid())
}
func (h host) effectiveUID() uint32 {
	if h.euid != nil {
		return uint32(h.euid())
	}
	return uint32(os.Geteuid())
}
func (h host) endpointPath() string {
	if h.endpoint != "" {
		return h.endpoint
	}
	return endpointDir
}
