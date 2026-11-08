//go:build linux && (amd64 || arm64)

package journal

import (
	"syscall"
	"unsafe"
)

// calls is an unexported per-engine fault seam. Production uses these direct,
// single-call adapters only; configuration cannot replace them.
type calls struct {
	open   func(int, string, int, uint32) (int, error)
	stat   func(int, *syscall.Stat_t) error
	statfs func(int, *syscall.Statfs_t) error
	flock  func(int, int) error
	read   func(int, []byte, int64) (int, error)
	write  func(int, []byte, int64) (int, error)
	sync   func(int) error
	rename func(int, string, int, string) error
	close  func(int) error
	random func([]byte) (int, error)
}

var nativeCalls = calls{syscall.Openat, syscall.Fstat, syscall.Fstatfs, syscall.Flock, rawRead, rawWrite, syscall.Fsync, syscall.Renameat, syscall.Close, rawRandom}

func rawRead(fd int, p []byte, off int64) (int, error) { return rawIO(syscall.SYS_PREAD64, fd, p, off) }
func rawWrite(fd int, p []byte, off int64) (int, error) {
	return rawIO(syscall.SYS_PWRITE64, fd, p, off)
}
func rawIO(trap uintptr, fd int, p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, _, e := syscall.Syscall6(trap, uintptr(fd), uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)), uintptr(off), 0, 0)
	if e != 0 {
		return 0, e
	}
	return int(n), nil
}
func rawRandom(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, _, e := syscall.Syscall(sysGetrandom, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)), 0)
	if e != 0 {
		return 0, e
	}
	return int(n), nil
}
func unsupported(err error) bool {
	return err == syscall.EINVAL || err == syscall.ENOLCK || err == syscall.ENOSYS || err == syscall.EOPNOTSUPP
}
func openError(err error) error {
	switch err {
	case syscall.ENOENT:
		return failure(MissingState, "open", err)
	case syscall.EEXIST, syscall.ELOOP, syscall.ENOTDIR, syscall.EACCES:
		return failure(InvalidState, "open protection", err)
	default:
		return failure(StorageReadFailure, "open", err)
	}
}
