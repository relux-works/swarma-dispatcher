//go:build linux

package m0client

import (
	"crypto/ed25519"
	"errors"
	"path"
	"syscall"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

const (
	reasonConfig           = "config_invalid"
	reasonPlan             = "signing_plan_invalid"
	reasonCaller           = "caller_uid_mismatch"
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

// snapshot pins all ancestors and the final file without following symlinks.
// Config snapshots require root ownership, group root, 0444 and one link.
// Plans require root/effective-UID ownership and no group/other write bits.
func (h host) snapshot(name string, limit int, config bool) ([]byte, error) {
	if !validAbsolute(name) {
		return nil, errShape
	}
	owner := h.effectiveUID()
	if config {
		owner = 0
	}
	dir, err := h.pinDirectory(path.Dir(name), owner)
	if err != nil {
		return nil, err
	}
	defer dir.close()
	fd, err := syscall.Openat(dir.last(), path.Base(name), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	st, err := h.fstat(fd)
	if err != nil {
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0022 != 0 || (st.Uid != 0 && st.Uid != owner) {
		return nil, errShape
	}
	if config && (st.Uid != 0 || st.Gid != 0 || st.Nlink != 1 || st.Mode&permMask != 0444) {
		return nil, errShape
	}
	buf := make([]byte, limit+1)
	n, err := readUpTo(fd, buf)
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, errShape
	}
	if err := dir.recheckFile(fd, path.Base(name), st, n); err != nil {
		return nil, err
	}
	if err := dir.recheck(path.Dir(name), owner); err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// recheckFile compares the opened file and its pinned name after a bounded read.
// Both snapshots and the signing key use this same metadata/identity check.
func (p *pinned) recheckFile(fd int, name string, before syscall.Stat_t, n int) error {
	after, err := p.host.fstat(fd)
	if err != nil {
		return err
	}
	named, err := p.stat(name)
	if err != nil {
		return err
	}
	if before.Size != int64(n) || after.Size != before.Size || after.Mtim != before.Mtim || after.Ctim != before.Ctim || identity(before) != identity(after) || named != identity(after) {
		return errShape
	}
	return nil
}

func loadConfig(name string) (*Config, error) { return (host{}).loadConfig(name) }

func (h host) loadConfig(name string) (*Config, error) {
	buf, err := h.snapshot(name, MaxConfigBytes, true)
	if err != nil {
		return nil, err
	}
	c, err := ParseConfig(buf)
	if err != nil {
		return nil, err
	}
	c.host = h
	if h.realUID() != c.Caller.UID || h.effectiveUID() != c.Caller.UID {
		return nil, errIdentity
	}
	if !groupMember(c.ConnectGID) || groupMember(c.ServiceGID) {
		return nil, errIdentity
	}
	pin, err := c.callerPin()
	if err != nil {
		return nil, err
	}
	pin.close()
	return c, nil
}

func (c *Config) loadPlan(op m0.Op) (*m0.SigningPlan, error) {
	name := c.DispatchPlan
	if op == m0.OpCancel {
		name = c.CancelPlan
	}
	buf, err := c.host.snapshot(name, m0.MaxRequestWire, false)
	if err != nil {
		return nil, err
	}
	plan, err := m0.DecodeSigningPlan(buf)
	if err != nil {
		return nil, err
	}
	if plan.Request.Op != op {
		return nil, errors.New("signing plan has the wrong operation")
	}
	return plan, nil
}

// signPreimage opens the 32-byte seed through a pinned caller-owned 0700
// parent and protected root/caller-owned ancestors, without symlinks. The
// file must be regular, single-link, effective-UID-owned and exactly 0600.
// Recheck the pinned path after reading, before deriving the associated key
// or signing. Clearing the local seed buffer and expanded key is best effort;
// copies inside the standard library are not cleared. Neither local buffer
// is printed, logged or copied elsewhere.
func signPreimage(c *Config, r *m0.Request) (sig string, reason string, err error) {
	if !validAbsolute(c.KeyFile) {
		return "", reasonKeyFileUnsafe, errShape
	}
	owner := c.host.effectiveUID()
	parent := path.Dir(c.KeyFile)
	dir, err := c.host.pinDirectory(parent, owner)
	if err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	defer dir.close()
	leaf := dir.ids[len(dir.ids)-1]
	if leaf.mode&syscall.S_IFMT != syscall.S_IFDIR || leaf.uid != owner || leaf.mode&permMask != 0700 {
		return "", reasonKeyFileUnsafe, errShape
	}
	if err := protectKeyProcess(); err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	fd, err := syscall.Openat(dir.last(), path.Base(c.KeyFile), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	defer syscall.Close(fd)
	st, err := c.host.fstat(fd)
	if err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 || st.Uid != c.host.effectiveUID() || st.Mode&permMask != 0600 || st.Size != seedBytes {
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
	if err := dir.recheckFile(fd, path.Base(c.KeyFile), st, n); err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	if err := dir.recheck(parent, owner); err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	private := ed25519.NewKeyFromSeed(buf[:seedBytes])
	defer clear(private)
	spelling, err := m0.KeySpelling(private[seedBytes:])
	if err != nil || spelling != r.Association.Key {
		return "", reasonKeyNotAssociated, nil
	}
	raw := ed25519.Sign(private, r.Preimage)
	sig, err = m0.SignatureSpelling(raw)
	if err != nil {
		return "", reasonKeyFileUnsafe, err
	}
	return sig, "", nil
}

// protectKeyProcess disables process dumps before any seed descriptor is opened.
// These restrictions are intentionally not restored after signing.
func protectKeyProcess() error {
	if _, _, err := syscall.RawSyscall6(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0, 0, 0, 0); err != 0 {
		return err
	}
	return syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
}

// openStateDir pins all ancestors and a caller-owned, exactly 0700 leaf.
// It returns the readable pinned directory descriptor for storage and fsync;
// nothing is created or repaired.
func openStateDir(name string) (int, error) { return (host{}).openStateDir(name) }

func (h host) openStateDir(name string) (int, error) {
	if !validAbsolute(name) {
		return -1, errShape
	}
	owner := h.effectiveUID()
	dir, err := h.pinDirectory(path.Dir(name), owner)
	if err != nil {
		return -1, err
	}
	defer dir.close()
	// O_NOFOLLOW alone gives ELOOP for a link. O_NONBLOCK avoids a FIFO
	// blocking before fstat; the descriptor must then pass the directory rule.
	fd, err := syscall.Openat(dir.last(), path.Base(name), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return -1, err
	}
	dir.fds = append(dir.fds, fd)
	st, err := h.fstat(fd)
	if err != nil {
		return -1, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Uid != owner || st.Mode&permMask != 0700 {
		return -1, errShape
	}
	dir.ids = append(dir.ids, identity(st))
	if err := dir.recheck(name, owner); err != nil {
		return -1, err
	}
	// Transfer only the final descriptor; the ancestor pins close on return.
	dir.fds = dir.fds[:len(dir.fds)-1]
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
func (h host) loadRequest(dir int, id string) ([]byte, error) {
	fd, err := syscall.Openat(dir, requestFile(id), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	st, err := h.fstat(fd)
	if err != nil {
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 || st.Uid != h.effectiveUID() || st.Mode&permMask != 0600 || st.Size < 1 || st.Size > m0.MaxRequestWire {
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
