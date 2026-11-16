//go:build linux

package keymaker

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

// UnixDialer verifies the protected path, exact socket pins, post-connect
// identity and SO_PEERCRED before returning a fresh close-on-exec connection.
// Root is the deployment-protected trust anchor; its own mode is checked too.
// Ancestors above Root are deployment responsibility, as with Source's walk.
// No configuration parser or default selection is provided here.
type UnixDialer struct {
	Root       string
	Path       string
	SocketUID  uint32
	SocketGID  uint32
	SocketMode os.FileMode
	PeerUID    uint32
	Timeout    time.Duration
	// Private observation seams: production always uses the kernel and net.Dialer.
	peer         func(*net.UnixConn) (uint32, error)
	stat         func(string) (os.FileInfo, error)
	afterConnect func()
}

func socketRefusal(check string, cause error) error {
	return codes.Refuse(codes.BindRefused, "keymaker endpoint pin refused", errors.Join(ErrEndpointPin, cause)).With("check", check)
}

// check intentionally keeps Source's path walk independent: Keymaker pins
// group and mode and rechecks identity, whereas Source has fixed 0711/0666.
func (d UnixDialer) check() (os.FileInfo, error) {
	if !filepath.IsAbs(d.Root) || filepath.Clean(d.Root) != d.Root || !filepath.IsAbs(d.Path) || filepath.Clean(d.Path) != d.Path {
		return nil, socketRefusal("absolute clean path required", nil)
	}
	rel, err := filepath.Rel(d.Root, d.Path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, socketRefusal("path outside protected root", err)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	current := d.Root
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			current = filepath.Join(current, parts[i])
		}
		observe := d.stat
		if observe == nil {
			observe = os.Lstat
		}
		fi, err := observe(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, codes.Refuse(codes.BindUnavailable, "keymaker endpoint absent", err)
			}
			return nil, socketRefusal("lstat", err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, socketRefusal("symlink component", nil)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, socketRefusal("ownership unreadable", nil)
		}
		if i == len(parts)-1 {
			if fi.Mode()&os.ModeSocket == 0 {
				return nil, socketRefusal("not a socket", nil)
			}
			if st.Uid != d.SocketUID {
				return nil, socketRefusal("owner pin", nil)
			}
			if st.Gid != d.SocketGID {
				return nil, socketRefusal("group pin", nil)
			}
			if fi.Mode().Perm() != d.SocketMode || fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
				return nil, socketRefusal("mode pin", nil)
			}
			return fi, nil
		}
		if !fi.IsDir() {
			return nil, socketRefusal("ancestor not directory", nil)
		}
		if st.Uid != 0 && st.Uid != d.SocketUID {
			return nil, socketRefusal("ancestor owner", nil)
		}
		if fi.Mode().Perm()&0022 != 0 {
			return nil, socketRefusal("writable ancestor", nil)
		}
	}
	return nil, socketRefusal("missing socket", nil)
}

func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var call error
	err = raw.Control(func(fd uintptr) {
		cred, call = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || call != nil {
		return 0, errors.Join(err, call)
	}
	if cred == nil {
		return 0, errors.New("missing SO_PEERCRED")
	}
	return cred.Uid, nil
}

// Dial does not write even a hello until every check succeeds.
func (d UnixDialer) Dial(ctx context.Context) (net.Conn, error) {
	before, err := d.check()
	if err != nil {
		return nil, err
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = HelloBudget
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", d.Path)
	if err != nil {
		return nil, codes.Refuse(codes.BindUnavailable, "keymaker connect transport failure", err)
	}
	fail := func(err error) (net.Conn, error) { _ = c.Close(); return nil, err }
	if d.afterConnect != nil {
		d.afterConnect()
	}
	after, err := d.check()
	if err != nil {
		if errors.Is(err, ErrEndpointPin) {
			return fail(err)
		}
		return fail(socketRefusal("identity changed after connect", err))
	}
	if !os.SameFile(before, after) {
		return fail(socketRefusal("identity changed after connect", nil))
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return fail(socketRefusal("not a Unix connection", nil))
	}
	observe := d.peer
	if observe == nil {
		observe = peerUID
	}
	uid, err := observe(uc)
	if err != nil || uid != d.PeerUID {
		return fail(socketRefusal("peer uid pin", err))
	}
	return c, nil
}
