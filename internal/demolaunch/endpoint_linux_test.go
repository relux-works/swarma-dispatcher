//go:build linux

package demolaunch

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Fixture adapters for the copied transport endpoint witnesses. Same UID/GID
// operations do not qualify cross-account publication permissions.
func protectedFixture(t *testing.T, mode os.FileMode) *directory {
	root := fixtureDir(t)
	must(t, os.Chmod(root, mode))
	d, err := pinDirectory(root, uint32(os.Geteuid()))
	must(t, err)
	t.Cleanup(d.close)
	return d
}
func fixtureConfig() commonConfig {
	return commonConfig{ServiceUID: uint32(os.Geteuid()), ServiceGID: uint32(os.Getegid()), ConnectGID: uint32(os.Getegid())}
}
func fixtureEndpoint(t *testing.T) (*endpoint, commonConfig) {
	d := protectedFixture(t, 0711)
	c := fixtureConfig()
	old := syscall.Umask(0177)
	e, err := createEndpoint(d, c)
	syscall.Umask(old)
	must(t, err)
	return e, c
}

// The exclusive lifetime lock rejects a second holder and preserves its inode.
func TestLifetimeLock(t *testing.T) {
	d := protectedFixture(t, 0711)
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	old := syscall.Umask(0177)
	defer syscall.Umask(old)
	f, err := acquireLock(d, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	before, err := atIdentity(d, lockName)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := acquireLock(d, uid, gid); other != nil || !(errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)) {
		if other != nil {
			other.Close()
		}
		t.Fatal("duplicate lock admitted", err)
	}
	f.Close()
	again, err := acquireLock(d, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
	after, err := atIdentity(d, lockName)
	if err != nil || !before.attrs(after) {
		t.Fatal("lock replaced or deleted", err)
	}
	if err = os.Chmod(filepath.Join(d.path, lockName), 0640); err != nil {
		t.Fatal(err)
	}
	if f, err := acquireLock(d, uid, gid); f != nil || !errors.Is(err, ErrOwnership) {
		if f != nil {
			f.Close()
		}
		t.Fatal("bad lock accepted", err)
	}
	info, err := os.Stat(filepath.Join(d.path, lockName))
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("lock repaired", err)
	}
}

// Genuine socket creation and owned cleanup are positive controls. A replaced
// pathname survives cleanup failure; the stable lock and foreign contents survive.
func TestEndpointOwnership(t *testing.T) {
	e, c := fixtureEndpoint(t)
	actual, err := atIdentity(e.dir, endpointName)
	if err != nil || !socketIdentity(actual, c) {
		t.Fatal("published socket", err)
	}
	d, err := pinDirectory(e.dir.path, c.ServiceUID)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := createEndpoint(d, c); other != nil || err == nil {
		if other != nil {
			other.close()
		}
		t.Fatal("live endpoint adopted", err)
	}
	path := filepath.Join(e.dir.path, endpointName)
	lock := filepath.Join(e.dir.path, lockName)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("foreign sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = e.close(); !errors.Is(err, ErrOwnership) {
		t.Fatal("foreign replacement cleanup passed", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "foreign sentinel" {
		t.Fatal("foreign data changed", err)
	}
	if _, err = os.Stat(lock); err != nil {
		t.Fatal("lock removed", err)
	}
}

// Narrow deterministic publication failures are paired with actual socket and
// filesystem operations. These test error cleanup/order, not cross-GID authority.
func TestPostBindFailureCleanup(t *testing.T) {
	for _, stage := range []string{"chgrp", "chmod", "listen"} {
		t.Run(stage, func(t *testing.T) {
			d := protectedFixture(t, 0711)
			old := syscall.Umask(0177)
			defer syscall.Umask(old)
			path := d.path
			cfg := fixtureConfig()
			calls := publicationSyscalls()
			sentinel := errors.New("planted publication failure")
			var observed []string
			chgrp, chmod, listen := calls.chgrp, calls.chmod, calls.listen
			calls.chgrp = func(d *directory, g uint32) error {
				observed = append(observed, "chgrp")
				s, err := atIdentity(d, endpointName)
				if err != nil {
					return err
				}
				if s.mode&07777 != 0600 {
					t.Error("initial mode is not 0600")
				}
				if stage == "chgrp" {
					return sentinel
				}
				return chgrp(d, g)
			}
			calls.chmod = func(d *directory) error {
				observed = append(observed, "chmod")
				if stage == "chmod" {
					return sentinel
				}
				return chmod(d)
			}
			calls.listen = func(fd int) error {
				observed = append(observed, "listen")
				if stage == "listen" {
					return sentinel
				}
				return listen(fd)
			}
			if e, err := createEndpointCalls(d, cfg, calls, nil); e != nil || !errors.Is(err, sentinel) {
				if e != nil {
					e.close()
				}
				t.Fatal("wrong publication failure", err)
			}
			if len(observed) == 0 || observed[0] != "chgrp" || len(observed) > 1 && observed[1] != "chmod" {
				t.Fatal("publication order", observed)
			}
			if _, err := os.Lstat(filepath.Join(path, endpointName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("owned residual after failure", err)
			}
			if _, err := os.Lstat(filepath.Join(path, lockName)); err != nil {
				t.Fatal("stable lock lost", err)
			}
			valid, err := pinDirectory(path, cfg.ServiceUID)
			if err != nil {
				t.Fatal(err)
			}
			e, err := createEndpoint(valid, cfg)
			if err != nil {
				t.Fatal("valid control after failure", err)
			}
			if err = e.close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Replacing the socket between publication stages cannot publish the foreign
// inode. The ordinary publication is the positive control.
func TestPublicationIdentity(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(jsonNumber(boolInt(replace)), func(t *testing.T) {
			d := protectedFixture(t, 0711)
			path := filepath.Join(d.path, endpointName)
			c := fixtureConfig()
			calls := publicationSyscalls()
			real := calls.chmod
			calls.chmod = func(d *directory) error {
				if replace {
					if err := os.Remove(path); err != nil {
						return err
					}
					if err := os.WriteFile(path, []byte("foreign"), 0660); err != nil {
						return err
					}
					return nil
				}
				return real(d)
			}
			old := syscall.Umask(0177)
			e, err := createEndpointCalls(d, c, calls, nil)
			syscall.Umask(old)
			if !replace {
				must(t, err)
				must(t, e.close())
			} else {
				if !errors.Is(err, ErrOwnership) || e != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(path)
				must(t, err)
				if string(b) != "foreign" {
					t.Fatal("foreign replacement changed")
				}
			}
		})
	}
}
