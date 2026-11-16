//go:build linux

package m0client

import (
	"crypto/ed25519"
	"errors"
	"os"
	"syscall"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

const (
	reasonConfig           = "config_invalid"
	reasonKeyFileUnsafe    = "key_file_unsafe"
	reasonKeyNotAssociated = "key_not_associated"
	reasonState            = "state_dir_unsafe"
	reasonStore            = "state_store_failed"
	reasonStoredRequest    = "stored_request_refused"

	seedBytes = 32
	permMask  = 07777
)

var (
	errShape = errors.New("file type, owner, links, mode or size")
)

func fstat(fd int) (syscall.Stat_t, error) {
	var st syscall.Stat_t
	err := syscall.Fstat(fd, &st)
	return st, err
}

// readUpTo reads until end of file or until p is full.
func readUpTo(fd int, p []byte) (int, error) {
	n := 0
	for n < len(p) {
		m, err := syscall.Read(fd, p[n:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return n, err
		}
		if m == 0 {
			break
		}
		n += m
	}
	return n, nil
}

// loadConfig reads the protected configuration: an absolute path naming a
// regular file that is not a symlink, is owned by root or the effective UID
// and is not writable by group or others. Nothing else happens before this.
func loadConfig(name string) (*Config, error) {
	if !validAbsolute(name) {
		return nil, errors.New("configuration path must be absolute and clean")
	}
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	st, err := fstat(fd)
	if err != nil {
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0022 != 0 || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
		return nil, errShape
	}
	buf := make([]byte, MaxConfigBytes+1)
	n, err := readUpTo(fd, buf)
	if err != nil {
		return nil, err
	}
	after, err := fstat(fd)
	if err != nil {
		return nil, err
	}
	if after.Ino != st.Ino || after.Dev != st.Dev || after.Size != int64(n) || after.Mtim != st.Mtim {
		return nil, errShape
	}
	return ParseConfig(buf[:n])
}

// signPreimage reads the 32-byte seed and signs the preimage. The file must
// be a regular single-link file owned by the effective UID with mode exactly
// 0600 and size exactly 32, opened without following a symlink. The derived
// public key must be the association key. The seed and the expanded key are
// zeroed before return; neither is ever printed, logged or copied elsewhere.
func signPreimage(c *Config, preimage []byte) (sig string, reason string, err error) {
	fd, err := syscall.Open(c.KeyFile, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	defer syscall.Close(fd)
	st, err := fstat(fd)
	if err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) || st.Mode&permMask != 0600 || st.Size != seedBytes {
		return "", reasonKeyFileUnsafe, errShape
	}
	var buf [seedBytes + 1]byte
	defer clear(buf[:])
	n, err := readUpTo(fd, buf[:])
	if err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	if n != seedBytes {
		return "", reasonKeyFileUnsafe, errShape
	}
	private := ed25519.NewKeyFromSeed(buf[:seedBytes])
	defer clear(private)
	spelling, err := m0.KeySpelling(private[seedBytes:])
	if err != nil || spelling != c.Association.Key {
		return "", reasonKeyNotAssociated, nil
	}
	raw := ed25519.Sign(private, preimage)
	sig, err = m0.SignatureSpelling(raw)
	if err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	return sig, "", nil
}

// openStateDir pins the state directory: a directory, not a symlink, owned by
// the effective UID, mode exactly 0700. It is never created or repaired.
func openStateDir(name string) (int, error) {
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	st, err := fstat(fd)
	if err == nil && (st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Uid != uint32(os.Geteuid()) || st.Mode&permMask != 0700) {
		err = errShape
	}
	if err != nil {
		syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

func requestFile(id string) string { return id + ".req" }

// storeRequest writes the exact wire bytes to <id>.req: exclusive create,
// mode 0600, file and directory synced. A failure after creation removes only
// the file this call created.
func storeRequest(dir int, id string, wire []byte) error {
	name := requestFile(id)
	fd, err := syscall.Openat(dir, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	err = syscall.Fchmod(fd, 0600)
	for p := wire; err == nil && len(p) > 0; {
		var n int
		n, err = syscall.Write(fd, p)
		if err == syscall.EINTR {
			err = nil
			continue
		}
		if err == nil {
			p = p[n:]
		}
	}
	if err == nil {
		err = syscall.Fsync(fd)
	}
	if closeErr := syscall.Close(fd); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = syscall.Unlinkat(dir, name)
		_ = syscall.Fsync(dir)
		return err
	}
	return syscall.Fsync(dir)
}

// loadRequest reads <id>.req unchanged: a regular single-link file owned by
// the effective UID, mode exactly 0600, within the request wire bound.
func loadRequest(dir int, id string) ([]byte, error) {
	fd, err := syscall.Openat(dir, requestFile(id), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	st, err := fstat(fd)
	if err != nil {
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) || st.Mode&permMask != 0600 || st.Size < 1 || st.Size > m0.MaxRequestWire {
		return nil, errShape
	}
	buf := make([]byte, m0.MaxRequestWire+1)
	n, err := readUpTo(fd, buf)
	if err != nil {
		return nil, err
	}
	if int64(n) != st.Size {
		return nil, errShape
	}
	return buf[:n], nil
}
