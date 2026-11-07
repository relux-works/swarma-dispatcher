//go:build linux

package demolaunch

import (
	"errors"
	"os"
	"syscall"
	"time"
)

type endpoint struct {
	dir            *directory
	lock, listener *os.File
	owned          *fileID
}

func acquireLock(d *directory, uid, gid uint32) (*os.File, error) {
	f, err := openAt(d.last(), lockName, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*os.File, error) { f.Close(); return nil, e }
	s, err := identity(f)
	if err != nil {
		return fail(err)
	}
	named, err := atIdentity(d, lockName)
	if err != nil {
		return fail(err)
	}
	if s.mode&syscall.S_IFMT != syscall.S_IFREG || s.uid != uid || s.gid != gid || s.mode&07777 != 0600 || s.links != 1 || !s.attrs(named) {
		return fail(ErrOwnership)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(err)
	}
	named, err = atIdentity(d, lockName)
	if err != nil || !s.attrs(named) {
		return fail(errors.Join(ErrOwnership, err))
	}
	return f, nil
}

// removeOwned assumes the held lifetime lock and trusted root/service directory custodian.
func removeOwned(d *directory, owned *fileID) error {
	if owned == nil {
		return nil
	}
	if err := d.recheck(); err != nil {
		return errors.Join(ErrOwnership, err)
	}
	now, err := atIdentity(d, endpointName)
	if err != nil {
		return errors.Join(ErrOwnership, err)
	}
	if !owned.same(now) || now.uid != owned.uid || now.mode&syscall.S_IFMT != syscall.S_IFSOCK {
		return ErrOwnership
	}
	return syscall.Unlinkat(int(d.last().Fd()), endpointName)
}
func (e *endpoint) close() error { return e.closeBefore(time.Time{}) }
func (e *endpoint) closeBefore(deadline time.Time) error {
	var err error
	if e.listener != nil {
		wakeClose(e.listener)
		e.listener = nil
	}
	if e.lock != nil {
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			err = ErrShutdown // Retain the endpoint rather than begin cleanup after budget.
		} else {
			err = removeOwned(e.dir, e.owned)
		}
		e.owned = nil
		err = errors.Join(err, e.lock.Close())
		e.lock = nil
	}
	e.dir.close()
	return err
}

// createEndpoint owns d on success and failure. Umask 0177 is set once at process startup.
type publicationCalls struct {
	chgrp  func(*directory, uint32) error
	chmod  func(*directory) error
	listen func(int) error
}

func publicationSyscalls() publicationCalls {
	return publicationCalls{
		chgrp: func(d *directory, gid uint32) error {
			return syscall.Fchownat(int(d.last().Fd()), endpointName, -1, int(gid), 0x100)
		},
		chmod:  func(d *directory) error { return syscall.Fchmodat(int(d.last().Fd()), endpointName, 0660, 0) },
		listen: func(fd int) error { return syscall.Listen(fd, 16) },
	}
}
func createEndpoint(d *directory, c commonConfig) (*endpoint, error) {
	return createEndpointCalls(d, c, publicationSyscalls(), nil)
}

// The unexported call bundle permits deterministic publication-error tests;
// deployment/configuration cannot supply callbacks or bypass validation.
func createEndpointCalls(d *directory, c commonConfig, calls publicationCalls, stop *shutdown) (_ *endpoint, err error) {
	e := &endpoint{dir: d}
	defer func() {
		if err != nil {
			if stop != nil {
				stop.request(err)
				begin, _ := stop.snapshot()
				err = errors.Join(err, e.closeBefore(begin.Add(shutdownBudget)))
			} else {
				err = errors.Join(err, e.close())
			}
		}
	}()
	e.lock, err = acquireLock(d, c.ServiceUID, c.ServiceGID)
	if err != nil {
		return nil, err
	}
	if _, x := atIdentity(d, endpointName); x == nil {
		return nil, ErrOwnership
	} else if !errors.Is(x, syscall.ENOENT) {
		return nil, x
	}
	fd, x := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if x != nil {
		return nil, x
	}
	e.listener = os.NewFile(uintptr(fd), "dispatcher listener")
	if err = tuneSocket(e.listener); err != nil {
		return nil, err
	}
	if err = d.recheck(); err != nil {
		return nil, err
	}
	if err = syscall.Bind(fd, &syscall.SockaddrUnix{Name: descriptorPath(d, endpointName)}); err != nil {
		return nil, err
	}
	s, x := atIdentity(d, endpointName)
	if x != nil {
		return nil, errors.Join(ErrOwnership, x)
	}
	// Record before publication so every later failure uses identity-scoped cleanup.
	e.owned = &s
	if s.uid != c.ServiceUID || s.gid != c.ServiceGID || s.mode&syscall.S_IFMT != syscall.S_IFSOCK || s.mode&07777 != 0600 || s.links != 1 {
		return nil, ErrOwnership
	}
	if err = calls.chgrp(d, c.ConnectGID); err != nil {
		return nil, err
	}
	if err = d.recheck(); err != nil {
		return nil, err
	}
	now, x := atIdentity(d, endpointName)
	if x != nil || !s.same(now) {
		return nil, errors.Join(ErrOwnership, x)
	}
	if err = calls.chmod(d); err != nil {
		return nil, err
	}
	now, x = atIdentity(d, endpointName)
	if x != nil {
		return nil, x
	}
	if !socketIdentity(now, c) || !s.same(now) {
		return nil, ErrOwnership
	}
	if err = d.recheck(); err != nil {
		return nil, err
	}
	if err = calls.listen(fd); err != nil {
		return nil, err
	}
	return e, nil
}
func socketIdentity(s fileID, c commonConfig) bool {
	return s.mode&syscall.S_IFMT == syscall.S_IFSOCK && s.uid == c.ServiceUID && s.gid == c.ConnectGID && s.mode&07777 == 0660 && s.links == 1
}
