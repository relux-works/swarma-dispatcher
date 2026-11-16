package sweep

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ErrNotDurable requires the process to exit without leasing or doing more state I/O.
var ErrNotDurable = errors.New("sweep: quarantine is not durable; process must exit")

// QuarantineName is the versioned lease ledger, separate from the run journal.
const QuarantineName = "quarantine.json"

// Entry is a durable, lease-scoped hold. Helper is monotone for that lease.
type Entry struct {
	UID    uint32 `json:"uid"`
	Holder string `json:"holder"`
	Reason string `json:"reason"`
	Helper bool   `json:"helper"`
}

type ledger struct {
	Version int              `json:"version"`
	Entries map[uint32]Entry `json:"entries"`
}

// Start takes the lifetime launcher lock, then loads the ledger. A missing
// ledger is fresh state; every lease is persisted before it can be used.
// A malformed ledger blocks ALL uids, including uids not seen in any record.
// All launchers of one process must share this Quarantine and call Close only
// after all runs stop. The reaper must take this lock before opening a journal.
func (q *Quarantine) Start(directory string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started {
		if q.directory != directory {
			return errors.New("sweep: quarantine shared across different state directories")
		}
		return q.blocked
	}
	if directory == "" || !filepath.IsAbs(directory) {
		return errors.New("sweep: absolute state directory required")
	}
	fi, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid()) {
		return errors.New("sweep: state directory must be private and launcher-owned")
	}
	fd, err := syscall.Open(filepath.Join(directory, "launcher.lock"), syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "launcher.lock")
	var lockStat syscall.Stat_t
	if err = syscall.Fstat(fd, &lockStat); err != nil || lockStat.Mode&syscall.S_IFMT != syscall.S_IFREG || lockStat.Mode&07777 != 0600 || lockStat.Uid != uint32(os.Geteuid()) || lockStat.Nlink != 1 {
		_ = f.Close()
		return errors.Join(errors.New("sweep: launcher lock protection refused"), err)
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return errors.Join(errors.New("sweep: launcher state ownership contended"), err)
	}
	q.lock = f
	q.directory = directory
	q.started = true
	if q.held == nil {
		q.held = map[uint32]Entry{}
	}
	path := filepath.Join(directory, QuarantineName)
	fd, err = syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return q.persistLocked()
	}
	if err != nil {
		q.blocked = errors.Join(ErrQuarantined, errors.New("sweep: quarantine unreadable; all uids held"), err)
		return q.blocked
	}
	in := os.NewFile(uintptr(fd), QuarantineName)
	defer in.Close()
	var fileStat syscall.Stat_t
	if err = syscall.Fstat(fd, &fileStat); err != nil || fileStat.Mode&syscall.S_IFMT != syscall.S_IFREG || fileStat.Mode&07777 != 0600 || fileStat.Uid != uint32(os.Geteuid()) || fileStat.Nlink != 1 || fileStat.Size > 4<<20 {
		q.blocked = errors.Join(ErrQuarantined, errors.New("sweep: quarantine protection or size refused; all uids held"), err)
		return q.blocked
	}
	var l ledger
	dec := json.NewDecoder(io.LimitReader(in, 4<<20))
	dec.DisallowUnknownFields()
	err = dec.Decode(&l)
	if err == nil {
		var extra any
		if dec.Decode(&extra) != io.EOF {
			err = errors.New("trailing data")
		}
	}
	if err == nil && (l.Version != 1 || l.Entries == nil) {
		err = errors.New("unknown ledger version or missing entries")
	}
	if err == nil {
		for uid, e := range l.Entries {
			if uid != e.UID || e.Holder == "" || e.Reason == "" {
				err = errors.New("invalid lease entry")
				break
			}
		}
	}
	if err != nil {
		q.blocked = errors.Join(ErrQuarantined, errors.New("sweep: quarantine corrupt; all uids held"), err)
		return q.blocked
	}
	q.held = l.Entries
	return nil
}

// Entry returns the current lease identity and reason for operator recovery.
// It is an observation, never proof or permission to release a uid.
func (q *Quarantine) Entry(uid uint32) (Entry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.held[uid]
	return e, ok
}

// Error reports the sticky fail-closed startup/durability error, if any.
func (q *Quarantine) Error() error { q.mu.Lock(); defer q.mu.Unlock(); return q.blocked }

// Close drops the lifetime ownership lock. It is not a lease release.
func (q *Quarantine) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.lock == nil {
		return nil
	}
	err := q.lock.Close()
	q.lock = nil
	q.blocked = errors.New("sweep: quarantine closed; all uids held")
	return err
}

// Note records the reason a pre-record lease could not return, durably.
func (q *Quarantine) Note(uid uint32, key, reason string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.held[uid]
	if !ok || e.Holder != key {
		return ErrQuarantined
	}
	e.Reason = reason
	q.held[uid] = e
	return q.persistLocked()
}

// Transfer assigns the pre-record token to its immutable run key, durably.
func (q *Quarantine) Transfer(uid uint32, old, key string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.held[uid]
	if !ok || e.Holder != old || q.busy[uid] != 0 {
		return ErrQuarantined
	}
	e.Holder = key
	q.held[uid] = e
	return q.persistLocked()
}

func (q *Quarantine) persistLocked() error {
	if q.blocked != nil {
		return q.blocked
	}
	if q.directory == "" {
		return nil
	} // Standalone decision tests; launcher always calls Start.
	fail := func(err error) error { q.blocked = errors.Join(ErrNotDurable, err); return q.blocked }
	b, err := json.Marshal(ledger{Version: 1, Entries: q.held})
	if err != nil {
		return fail(err)
	}
	f, err := os.CreateTemp(q.directory, ".quarantine-")
	if err != nil {
		return fail(err)
	}
	defer os.Remove(f.Name())
	syncFile := q.syncFile
	if syncFile == nil {
		syncFile = func(f *os.File) error { return f.Sync() }
	}
	if _, err = f.Write(b); err == nil {
		err = syncFile(f)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return fail(err)
	}
	if err = os.Rename(f.Name(), filepath.Join(q.directory, QuarantineName)); err != nil {
		return fail(err)
	}
	d, err := os.Open(q.directory)
	if err != nil {
		return fail(err)
	}
	err = errors.Join(syncFile(d), d.Close())
	if err != nil {
		return fail(err)
	}
	return nil
}
