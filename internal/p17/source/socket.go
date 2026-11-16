package source

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// SocketName is the control socket basename inside SourceControlDir.
const SocketName = "origin.sock"

// ErrSocketPath is the root of every control socket path refusal.
var ErrSocketPath = errors.New("source: control socket path")

func pathErr(what string) error {
	return errors.Join(ErrSocketPath, errors.New("source: control socket: "+what))
}

func ownerOf(fi os.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return st.Uid, true
}

// CheckControlSocket checks <dir>/origin.sock before any connection (H1 step
// 1). root is the trust anchor the deployment protects; every component
// strictly below root and above dir must be a non-symlink directory owned by
// root or by sourceUID with no group/other write bit. dir itself must be a
// non-symlink directory owned by sourceUID with permission bits exactly 0711.
// The socket must be a non-symlink socket owned by sourceUID with bits
// exactly 0666. Nothing is repaired. Returns the socket path on success.
func CheckControlSocket(root, dir string, sourceUID uint32) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", pathErr("root must be absolute and clean")
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", pathErr("dir must be absolute and clean")
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", pathErr("dir is not strictly below root")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	current := root
	for i, part := range parts {
		current = filepath.Join(current, part)
		fi, err := os.Lstat(current)
		if err != nil {
			return "", pathErr("lstat " + current)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", pathErr("symlink at " + current)
		}
		if !fi.IsDir() {
			return "", pathErr("not a directory: " + current)
		}
		uid, ok := ownerOf(fi)
		if !ok {
			return "", pathErr("owner unreadable: " + current)
		}
		perm := uint32(fi.Mode().Perm())
		if i == len(parts)-1 {
			if uid != sourceUID {
				return "", pathErr("control dir owner")
			}
			if perm != 0o711 || fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
				return "", pathErr("control dir mode")
			}
			continue
		}
		if uid != 0 && uid != sourceUID {
			return "", pathErr("ancestor owner: " + current)
		}
		if perm&0o022 != 0 {
			return "", pathErr("ancestor writable: " + current)
		}
	}
	sock := filepath.Join(dir, SocketName)
	fi, err := os.Lstat(sock)
	if err != nil {
		return "", pathErr("lstat socket")
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", pathErr("socket is a symlink")
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return "", pathErr("not a socket")
	}
	uid, ok := ownerOf(fi)
	if !ok || uid != sourceUID {
		return "", pathErr("socket owner")
	}
	if uint32(fi.Mode().Perm()) != 0o666 || fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return "", pathErr("socket mode")
	}
	return sock, nil
}
