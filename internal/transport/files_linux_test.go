//go:build linux

package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Prefer t.TempDir under protected temporary ancestry. Otherwise use the
// checkout's .temp, creating that base when absent without repairing ancestors
// or changing an existing base. Both routes use the real directory pin checks.
// Current-UID mechanisms are tested directly; this is not protected root config
// or distinct-role deployment admission, which remains a separate fixture gap.
func protectedFixture(t *testing.T, mode os.FileMode) *directory {
	t.Helper()
	name, temporaryErr := filepath.Abs(fixtureTemporaryRoot())
	var d *directory
	if temporaryErr == nil {
		var root *directory
		root, temporaryErr = pinDirectory(name, uint32(os.Geteuid()))
		if temporaryErr == nil {
			root.close()
			name = t.TempDir()
			d, temporaryErr = pinFixture(t, name, mode)
		}
	}
	if temporaryErr == nil {
		t.Cleanup(d.close)
		return d
	}
	base, err := filepath.Abs(filepath.Join("..", "..", ".temp"))
	var fallbackErr error
	if err != nil {
		fallbackErr = err
	} else {
		var parent *directory
		// Validate the actual checkout ancestry before attempting any writes.
		parent, fallbackErr = pinDirectory(filepath.Dir(base), uint32(os.Geteuid()))
		if fallbackErr == nil {
			parent.close()
			if err := os.Mkdir(base, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				fallbackErr = err
			}
		}
		if fallbackErr == nil {
			parent, fallbackErr = pinDirectory(base, uint32(os.Geteuid()))
		}
		if fallbackErr == nil {
			id := parent.ids[len(parent.ids)-1]
			if id.uid != uint32(os.Geteuid()) {
				fallbackErr = errors.New("checkout .temp base must be current-UID owned")
			}
			parent.close()
		}
		if fallbackErr == nil {
			var local string
			local, fallbackErr = os.MkdirTemp(base, "transport-")
			if fallbackErr == nil {
				// Remove only this allocation, never the shared base or siblings.
				t.Cleanup(func() {
					if err := os.RemoveAll(local); err != nil {
						t.Error(err)
					}
				})
				d, fallbackErr = pinFixture(t, local, mode)
				if fallbackErr == nil {
					t.Cleanup(d.close)
					return d
				}
			}
		}
	}
	t.Fatalf("neither fixture base usable: t.TempDir %q: %v; checkout .temp %q: %v", name, temporaryErr, base, fallbackErr)
	return nil
}

// Go's testing.TempDir honors GOTMPDIR before the platform temporary root.
func fixtureTemporaryRoot() string {
	if name := os.Getenv("GOTMPDIR"); name != "" {
		return name
	}
	return os.TempDir()
}

// Only the freshly allocated leaf gets its requested fixture mode. Ancestor
// ownership, permissions and symlinks are checked without modification.
func pinFixture(t *testing.T, name string, mode os.FileMode) (*directory, error) {
	if err := os.Chmod(name, mode); err != nil {
		return nil, err
	}
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != mode {
		return nil, fmt.Errorf("fixture directory mode: want %04o: %v %v", mode, info, err)
	}
	for path := name; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil {
			t.Logf("fixture ancestor %q: %v", path, err)
		} else {
			id := info.Sys().(*syscall.Stat_t)
			t.Logf("fixture ancestor %q uid=%d mode=%#o symlink=%t", path, id.Uid, id.Mode, info.Mode()&os.ModeSymlink != 0)
		}
		if path == "/" {
			break
		}
	}
	return pinDirectory(name, uint32(os.Geteuid()))
}

func TestProtectedFixtureSelectionAndCleanup(t *testing.T) {
	// Keep a live sibling through the child cleanup to catch overbroad removal.
	sibling := protectedFixture(t, 0700)
	sentinel := filepath.Join(sibling.path, "sentinel")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	// A sentinel directly in the selected base also survives leaf cleanup.
	base := filepath.Dir(sibling.path)
	before, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(base, "sentinel-")
	if err != nil {
		t.Fatal(err)
	}
	baseSentinel := f.Name()
	t.Cleanup(func() {
		if err := os.Remove(baseSentinel); err != nil {
			t.Error(err)
		}
	})
	if _, err := f.WriteString("base untouched"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var path string
	t.Run("selection", func(t *testing.T) {
		d := protectedFixture(t, 0711)
		path = d.path
		base, err := filepath.Abs(fixtureTemporaryRoot())
		if err != nil {
			t.Fatal(err)
		}
		pin, err := pinDirectory(base, uint32(os.Geteuid()))
		if err == nil {
			pin.close()
		} else {
			base, err = filepath.Abs(filepath.Join("..", "..", ".temp"))
			if err != nil {
				t.Fatal(err)
			}
		}
		rel, err := filepath.Rel(base, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			t.Fatalf("fixture %q outside selected base %q: %v", path, base, err)
		}
		if err := d.recheck(); err != nil {
			t.Fatal(err)
		}
		t.Logf("selected fixture %s", path)
	})
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture allocation survived child cleanup", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "untouched" {
		t.Fatal("sibling changed by cleanup", err)
	}
	if data, err := os.ReadFile(baseSentinel); err != nil || string(data) != "base untouched" {
		t.Fatal("base sentinel changed by cleanup", err)
	}
	after, err := os.Stat(base)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("fixture base replaced or permissions changed", err)
	}
}

func fixtureConfig() commonConfig {
	return commonConfig{ConnectGID: uint32(os.Getegid()), ServiceGID: uint32(os.Getegid()), ServiceUID: uint32(os.Geteuid())}
}
func fixtureEndpoint(t *testing.T) (*endpoint, commonConfig) {
	t.Helper()
	// Create traversable temporary ancestors before applying the socket umask.
	d := protectedFixture(t, 0711)
	previous := syscall.Umask(0177)
	t.Cleanup(func() { syscall.Umask(previous) })
	e, err := createEndpoint(d, fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e.lock != nil {
			if err := e.close(); err != nil {
				t.Error(err)
			}
		}
	})
	return e, fixtureConfig()
}

// Real directory checks accept an unchanged owned directory and reject a
// symlink, insecure mode or replacement without repairing any filesystem state.
func TestDirectoryPins(t *testing.T) {
	d := protectedFixture(t, 0700)
	if err := d.recheck(); err != nil {
		t.Fatal(err)
	}
	st := d.ids[len(d.ids)-1]
	p := principal{Dev: st.dev, Ino: st.ino, Dir: d.path, UID: st.uid}
	pin, err := principalPin(p)
	if err != nil {
		t.Fatal(err)
	}
	pin.close()
	if err = os.Chmod(d.path, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err = principalPin(p); !errors.Is(err, ErrIdentity) {
		t.Fatal("insecure pin accepted", err)
	}
	info, err := os.Stat(d.path)
	if err != nil || info.Mode().Perm() != 0770 {
		t.Fatal("pin silently repaired", err)
	}
	if err = os.Chmod(d.path, 0700); err != nil {
		t.Fatal(err)
	}
	original := d.path + "-original"
	if err = os.Rename(d.path, original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(original) })
	if err = os.Mkdir(d.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = d.recheck(); !errors.Is(err, ErrIdentity) {
		t.Fatal("replacement accepted", err)
	}
	if err = os.Remove(d.path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(original, d.path); err != nil {
		t.Fatal(err)
	}
	if _, err = pinDirectory(d.path, p.UID); err == nil {
		t.Fatal("symlink accepted")
	}
}

// Existing lock ownership/mode and open-description flock remain authoritative;
// a second holder fails and neither failed acquisition nor close unlinks the lock.
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

// The source gate rejects an ordinary unprotected config without modifying it;
// successful root-owned snapshot/CLI deployment is intentionally not simulated.
func TestUnprotectedConfigurationSource(t *testing.T) {
	d := protectedFixture(t, 0700)
	p := filepath.Join(d.path, "client.json")
	content := canonicalFixture(configFixture(false))
	if err := os.WriteFile(p, content, 0600); err != nil {
		t.Fatal(err)
	}
	if b, err := sourceSnapshot(p, false); b != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("unprotected source admitted", err)
	}
	after, err := os.ReadFile(p)
	if err != nil || string(after) != string(content) {
		t.Fatal("source modified", err)
	}
}

// A first fatal publication request starts the budget before cleanup. An
// already-expired cleanup never unlinks even its own endpoint and reports debt.
func TestCleanupBudgetAndFirstFatalRequest(t *testing.T) {
	e, _ := fixtureEndpoint(t)
	path := filepath.Join(e.dir.path, endpointName)
	if err := e.closeBefore(time.Now().Add(-time.Second)); !errors.Is(err, ErrShutdown) {
		t.Fatal("expired cleanup passed", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("endpoint unlinked beyond budget", err)
	}
	d := protectedFixture(t, 0711)
	old := syscall.Umask(0177)
	defer syscall.Umask(old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := &shutdown{cancel: cancel}
	calls := publicationSyscalls()
	planted := errors.New("publication error")
	calls.chgrp = func(*directory, uint32) error { return planted }
	if e, err := createEndpointCalls(d, fixtureConfig(), calls, stop); e != nil || !errors.Is(err, planted) {
		t.Fatal(err)
	}
	begin, cause := stop.snapshot()
	if begin.IsZero() || !errors.Is(cause, planted) || ctx.Err() == nil {
		t.Fatal("fatal request not recorded before owned cleanup")
	}
}

// Endpoint refusal is exercised with the lifetime lock available, so rejection
// actually reaches the path check. Regular/symlink/stale/live entries survive.
func TestExistingEndpointRefusedWithoutDeletion(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "stale", "live"} {
		t.Run(kind, func(t *testing.T) {
			d := protectedFixture(t, 0711)
			previous := syscall.Umask(0177)
			defer syscall.Umask(previous)
			path := filepath.Join(d.path, endpointName)
			target := filepath.Join(d.path, "sentinel")
			if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "regular":
				if err := os.WriteFile(path, []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			default:
				fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				if err = syscall.Bind(fd, &syscall.SockaddrUnix{Name: descriptorPath(d, endpointName)}); err != nil {
					syscall.Close(fd)
					t.Fatal(err)
				}
				if kind == "live" {
					if err = syscall.Listen(fd, 16); err != nil {
						syscall.Close(fd)
						t.Fatal(err)
					}
					t.Cleanup(func() { syscall.Close(fd) })
				} else {
					syscall.Close(fd)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if e, err := createEndpoint(d, fixtureConfig()); e != nil || !errors.Is(err, ErrOwnership) {
				if e != nil {
					e.close()
				}
				t.Fatal("existing endpoint admitted", err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("existing endpoint changed", err)
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != "untouched" {
				t.Fatal("foreign target changed", err)
			}
			if kind == "regular" {
				if data, err := os.ReadFile(path); err != nil || string(data) != "foreign" {
					t.Fatal("foreign contents changed", err)
				}
			}
		})
	}
}
