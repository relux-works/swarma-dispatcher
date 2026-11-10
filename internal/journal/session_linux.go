//go:build linux && (amd64 || arm64)

package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
)

type engine struct {
	calls      calls
	syncFailed atomic.Bool
}

var process = engine{calls: nativeCalls}

// Session owns the pinned tree and flock. Do not copy it. Close it before exit.
type Session struct {
	mu                                   sync.Mutex
	e                                    *engine
	root, lock, journal, records, inputs int
	owned                                []int
	uid                                  uint32
	nonce                                string
	counter                              uint64
	state                                State
	terminal                             error
	callbackRunning                      bool
	callbackFailure                      error
}

// Initialize commits first as sequence one before returning a usable Session.
func Initialize(c Config, first []byte) (*Session, error) { return process.enter(c, first, nil, true) }

// OpenEstablished validates, syncs and reconstructs before returning a Session.
func OpenEstablished(c Config, consumer Consumer) (*Session, error) {
	return process.enter(c, nil, consumer, false)
}

func (e *engine) enter(c Config, first []byte, consumer Consumer, initializing bool) (_ *Session, err error) {
	if e.syncFailed.Load() {
		return nil, failure(StorageWriteFailure, "process sync barrier", ErrSyncFailure)
	}
	if err = validate(c, first, initializing); err != nil {
		return nil, err
	}
	if !initializing && consumer == nil {
		return nil, failure(ConsumerFailure, "consumer required", nil)
	}
	s := &Session{e: e, root: -1, lock: -1, journal: -1, records: -1, inputs: -1, uid: uint32(syscall.Geteuid())}
	success := false
	defer func() {
		if !success {
			err = s.fail(err)
		}
	}()
	s.root, err = s.open(-100, c.Directory, syscall.O_RDONLY|syscall.O_DIRECTORY, 0, false)
	if err != nil {
		return nil, err
	}
	root, err := s.check(s.root, true)
	if err != nil {
		return nil, err
	}
	var fs syscall.Statfs_t
	if err = e.calls.statfs(s.root, &fs); err != nil {
		return nil, failure(StorageReadFailure, "fstatfs", err)
	}
	if fs.Type != 0xEF53 && fs.Type != 0x58465342 {
		return nil, failure(UnsupportedStorage, "filesystem type", nil)
	}
	s.lock, err = s.open(s.root, "lock", syscall.O_RDWR|syscall.O_CREAT, 0600, true)
	if err != nil {
		return nil, err
	}
	// Keep lock out of owned: close it last, including acquisition failure.
	s.owned = s.owned[:len(s.owned)-1]
	if _, err = s.check(s.lock, false); err != nil {
		return nil, err
	}
	if err = e.calls.flock(s.lock, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, failure(OwnershipContended, "flock", err)
		}
		if unsupported(err) {
			return nil, failure(UnsupportedStorage, "flock", err)
		}
		return nil, failure(StorageReadFailure, "flock", err)
	}
	s.records, err = s.open(s.root, c.Records, syscall.O_RDONLY|syscall.O_DIRECTORY, 0, false)
	if err != nil {
		return nil, err
	}
	rec, err := s.check(s.records, true)
	if err != nil {
		return nil, err
	}
	s.inputs, err = s.open(s.root, c.Inputs, syscall.O_RDONLY|syscall.O_DIRECTORY, 0, false)
	if err != nil {
		return nil, err
	}
	inp, err := s.check(s.inputs, true)
	if err != nil {
		return nil, err
	}
	if (rec.Dev == root.Dev && rec.Ino == root.Ino) || (inp.Dev == root.Dev && inp.Ino == root.Ino) {
		return nil, failure(InvalidState, "root directory alias", nil)
	}
	if rec.Dev == inp.Dev && rec.Ino == inp.Ino {
		return nil, failure(InvalidState, "record/input directory alias", nil)
	}
	var nonce [16]byte
	n, re := e.calls.random(nonce[:])
	if re != nil || n != len(nonce) {
		return nil, failure(StorageReadFailure, "session randomness", re)
	}
	s.nonce = hex.EncodeToString(nonce[:])
	flags := syscall.O_RDWR
	if initializing {
		flags |= syscall.O_CREAT | syscall.O_EXCL
	}
	s.journal, err = s.open(s.root, "journal", flags, 0600, initializing)
	if err != nil {
		return nil, err
	}
	st, err := s.check(s.journal, false)
	if err != nil {
		return nil, err
	}
	if initializing {
		s.state.Next = 1
		if err = s.append(first); err != nil {
			return nil, err
		}
		if err = s.syncFD(s.root); err != nil {
			return nil, err
		}
	} else {
		s.state.EOF = st.Size
		read := func(p []byte, off int64) (int, error) { return e.calls.read(s.journal, p, off) }
		if _, err = walk(read, st.Size, nil); err != nil {
			return nil, err
		}
		if err = s.drift(); err != nil {
			return nil, err
		}
		if err = s.syncFD(s.journal); err != nil {
			return nil, err
		}
		if err = s.drift(); err != nil {
			return nil, err
		}
		r := &Recovery{s: s, Budget: &Budget{limit: c.ConsumerBudget}, active: true}
		defer func() { r.active = false }()
		next, walkErr := walk(read, st.Size, func(seq uint64, p []byte) error {
			ce := s.consume(func() error { return consumer.Consume(seq, p, r) })
			return r.result(ce)
		})
		if walkErr == nil {
			walkErr = r.result(s.consume(func() error { return consumer.Finish(r) }))
		}
		r.active = false
		if walkErr != nil {
			if r.materialized {
				walkErr = poison(walkErr)
			}
			return nil, walkErr
		}
		if err = s.drift(); err != nil {
			if r.materialized {
				err = poison(err)
			}
			return nil, err
		}
		s.state.Next = next
	}
	if e.syncFailed.Load() {
		return nil, poison(failure(StorageWriteFailure, "process sync barrier", ErrSyncFailure))
	}
	s.state.Ready = true
	success = true
	return s, nil
}

func poison(err error) error {
	if err == nil {
		err = failure(StorageWriteFailure, "aborted ownership", nil)
	}
	var e *Error
	if errors.As(err, &e) {
		return &Error{Class: e.Class, Op: e.Op, Cause: err, Poisoned: true}
	}
	return &Error{Class: StorageWriteFailure, Op: "state mutation", Cause: err, Poisoned: true}
}
func (s *Session) open(dir int, name string, flags int, mode uint32, mutation bool) (int, error) {
	fd, err := s.e.calls.open(dir, name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, mode)
	if err != nil {
		if err == syscall.EISDIR || err == syscall.ENXIO {
			return -1, failure(InvalidState, "nonregular entry", err)
		}
		if mutation && err != syscall.EEXIST && err != syscall.ELOOP && err != syscall.ENOTDIR && err != syscall.EACCES {
			return -1, poison(failure(StorageWriteFailure, "create", err))
		}
		return -1, openError(err)
	}
	s.owned = append(s.owned, fd)
	return fd, nil
}
func (s *Session) check(fd int, dir bool) (syscall.Stat_t, error) {
	var st syscall.Stat_t
	if err := s.e.calls.stat(fd, &st); err != nil {
		return st, failure(StorageReadFailure, "fstat", err)
	}
	typ, mode := uint32(syscall.S_IFREG), uint32(0600)
	if dir {
		typ = syscall.S_IFDIR
		mode = 0700
	}
	if st.Mode&syscall.S_IFMT != typ || st.Mode&07777 != mode || st.Uid != s.uid || (!dir && st.Nlink != 1) {
		return st, failure(InvalidState, "descriptor protection", nil)
	}
	return st, nil
}
func (s *Session) syncFD(fd int) error {
	if err := s.e.calls.sync(fd); err != nil {
		s.e.syncFailed.Store(true)
		c := StorageWriteFailure
		if unsupported(err) {
			c = UnsupportedStorage
		}
		return poison(failure(c, "fsync", errors.Join(ErrSyncFailure, err)))
	}
	return nil
}
func (s *Session) closeFD(fd int) error {
	for i, f := range s.owned {
		if f == fd {
			s.owned = append(s.owned[:i], s.owned[i+1:]...)
			break
		}
	}
	if err := s.e.calls.close(fd); err != nil {
		return poison(failure(StorageWriteFailure, "close", err))
	}
	return nil
}

// consume marks exactly the callback's dynamic extent, including panic unwinding.
// A helper may call fail, but ownership survives until enter regains control.
func (s *Session) consume(fn func() error) error {
	s.callbackRunning = true
	defer func() { s.callbackRunning = false }()
	return fn()
}

// hasPoison walks every branch of an error tree without a fixed depth limit.
// Class selection remains the first-match rule in poison, independently of this
// aggregate disposition (spec 215, 219-221).
// Production wrappers form acyclic trees. Consumer-supplied cycles are unsupported:
// this recursive walk can exhaust the stack; ClassOf/errors.Is also cannot safely
// traverse such cycles. This helper does not validate arbitrary consumer graphs.
func hasPoison(err error) bool {
	if je, ok := err.(*Error); ok {
		if je == nil {
			return false
		}
		return je.Poisoned || hasPoison(je.Cause)
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range e.Unwrap() {
			if hasPoison(child) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return hasPoison(e.Unwrap())
	}
	return false
}

// joinFailure preserves the first class and promotes poison from every input
// before errors.As can hide a later poisoned Error behind the first cause.
func joinFailure(first error, rest ...error) error {
	causes := append([]error{first}, rest...)
	joined := errors.Join(causes...)
	for _, cause := range causes {
		if hasPoison(cause) {
			return poison(joined)
		}
	}
	return joined
}

// fail defers all teardown while a consumer runs (spec 106-110, 228-232).
// enter joins the recorded failure after callback quiescence. close is never
// retried; Linux releases the descriptor even when reporting most close errors.
func (s *Session) fail(err error) error {
	if s.callbackRunning {
		if err == nil {
			err = poison(nil)
		}
		// Retain the first cause with bounded storage even if a helper repeats
		// failure. A later sync failure must still carry the exit sentinel.
		if s.callbackFailure == nil {
			s.callbackFailure = err
		} else if errors.Is(err, ErrSyncFailure) && !errors.Is(s.callbackFailure, ErrSyncFailure) {
			s.callbackFailure = errors.Join(s.callbackFailure, ErrSyncFailure)
		}
		if hasPoison(err) {
			s.state.Poisoned = true
		}
		return err
	}
	if s.callbackFailure != nil {
		err = joinFailure(s.callbackFailure, err)
	}
	if s.state.Closed {
		return err
	}
	s.state.Ready = false
	s.state.Closed = true
	for len(s.owned) > 0 {
		fd := s.owned[len(s.owned)-1]
		if ce := s.closeFD(fd); ce != nil {
			err = errors.Join(err, ce)
			s.state.Poisoned = true
		}
	}
	if s.lock >= 0 {
		fd := s.lock
		s.lock = -1
		if ce := s.e.calls.close(fd); ce != nil {
			err = errors.Join(err, poison(failure(StorageWriteFailure, "close lock", ce)))
			s.state.Poisoned = true
		}
	}
	if hasPoison(err) {
		s.state.Poisoned = true
	}
	// PJ8 equivalence of a plain callback join depends on this whole-tree
	// re-wrap and on Closed with a callback record remaining unreachable from
	// production callers. Revisit that equivalence if either condition changes;
	// Recovery.result still needs joinFailure promotion before reaching this tail.
	if s.state.Poisoned && err != nil {
		err = poison(err)
	}
	s.terminal = err
	return err
}
func (s *Session) usable() error {
	if s.state.Closed || !s.state.Ready {
		return failure(InvalidState, "session ended", s.terminal)
	}
	if s.e.syncFailed.Load() {
		return poison(failure(StorageWriteFailure, "process sync barrier", ErrSyncFailure))
	}
	return nil
}
func (s *Session) drift() error {
	st, err := s.check(s.journal, false)
	if err != nil {
		return err
	}
	if st.Size != s.state.EOF {
		return poison(failure(InvalidState, "journal EOF drift", nil))
	}
	return nil
}
func (s *Session) fullWrite(fd int, p []byte, off int64) error {
	retries := 0
	for len(p) > 0 {
		n, err := s.e.calls.write(fd, p, off)
		if err == syscall.EINTR && n == 0 && retries < 3 {
			retries++
			continue
		}
		if err != nil || n <= 0 || n > len(p) {
			return poison(failure(StorageWriteFailure, "pwrite", err))
		}
		p = p[n:]
		off += int64(n)
		retries = 0
	}
	return nil
}
func (s *Session) append(p []byte) error {
	if err := s.drift(); err != nil {
		return err
	}
	if len(p) < 1 || len(p) > MaxPayload {
		return failure(PayloadInvalid, "payload length", nil)
	}
	if s.state.Next == 0 || s.state.EOF > FileCap-int64(56+len(p)) {
		return failure(JournalFull, "projected capacity", nil)
	}
	// One owned frame fixes header, payload and checksum before any syscall.
	b := make([]byte, 56+len(p))
	h := header(len(p), s.state.Next)
	copy(b, h[:])
	copy(b[24:], p)
	sum := sha256.Sum256(b[:24+len(p)])
	copy(b[24+len(p):], sum[:])
	if err := s.fullWrite(s.journal, b, s.state.EOF); err != nil {
		return err
	}
	if err := s.syncFD(s.journal); err != nil {
		return err
	}
	s.state.EOF += int64(len(b))
	s.state.Next++
	return nil
}

// Append returns the committed sequence only after fsync. Two clean refusals
// preserve a usable session; every other error closes it promptly.
func (s *Session) Append(p []byte) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return 0, s.fail(err)
	}
	seq := s.state.Next
	err := s.append(p)
	if err != nil {
		if ClassOf(err) != PayloadInvalid && ClassOf(err) != JournalFull {
			err = s.fail(err)
		}
		return 0, err
	}
	return seq, nil
}
func (s *Session) State() State { s.mu.Lock(); defer s.mu.Unlock(); return s.state }
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Closed {
		return s.terminal
	}
	return s.fail(nil)
}

func (s *Session) objectArgs(name string, p []byte) error {
	if !basename(name) || len(p) > MaxPayload {
		return failure(InvalidState, "object name or size", nil)
	}
	return nil
}
func (s *Session) replace(name string, p []byte) error {
	if err := s.objectArgs(name, p); err != nil {
		return err
	}
	// Existing records must also satisfy descriptor protection; never repair them.
	fd, err := s.open(s.records, name, syscall.O_RDONLY, 0, false)
	if err == nil {
		_, err = s.check(fd, false)
		ce := s.closeFD(fd)
		err = errors.Join(err, ce)
	}
	if err != nil && ClassOf(err) != MissingState {
		return err
	}
	if s.counter == ^uint64(0) {
		return failure(InvalidState, "temporary counter exhausted", nil)
	}
	s.counter++
	tmp := fmt.Sprintf(".record-tmp-%s-%016x", s.nonce, s.counter)
	fd, err = s.open(s.records, tmp, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL, 0600, true)
	if err != nil {
		return err
	}
	if _, err = s.check(fd, false); err != nil {
		return err
	}
	if err = s.fullWrite(fd, p, 0); err != nil {
		return err
	}
	if err = s.syncFD(fd); err != nil {
		return err
	}
	if err = s.e.calls.rename(s.records, tmp, s.records, name); err != nil {
		return poison(failure(StorageWriteFailure, "rename", err))
	}
	if err = s.syncFD(s.records); err != nil {
		return err
	}
	return s.closeFD(fd)
}
func (s *Session) createInput(name string, p []byte) error {
	if err := s.objectArgs(name, p); err != nil {
		return err
	}
	fd, err := s.open(s.inputs, name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL, 0600, true)
	if err != nil {
		return err
	}
	if _, err = s.check(fd, false); err != nil {
		return err
	}
	if err = s.fullWrite(fd, p, 0); err != nil {
		return err
	}
	if err = s.syncFD(fd); err != nil {
		return err
	}
	if err = s.syncFD(s.inputs); err != nil {
		return err
	}
	return s.closeFD(fd)
}
func (s *Session) readInput(name string, digest [32]byte) ([]byte, error) {
	return s.readInputBudget(name, digest, nil)
}

func (s *Session) readInputBudget(name string, digest [32]byte, budget *Budget) ([]byte, error) {
	if !basename(name) {
		return nil, failure(InvalidState, "input name", nil)
	}
	fd, err := s.open(s.inputs, name, syscall.O_RDONLY, 0, false)
	if err != nil {
		return nil, err
	}
	st, err := s.check(fd, false)
	if err != nil {
		return nil, err
	}
	if st.Size < 0 || st.Size > MaxPayload {
		return nil, failure(Corruption, "input size", nil)
	}
	if budget != nil {
		if err = budget.Charge(uint64(st.Size)); err != nil {
			return nil, err
		}
	}
	b := make([]byte, int(st.Size))
	n, err := readExact(func(p []byte, off int64) (int, error) { return s.e.calls.read(fd, p, off) }, b, 0)
	if err != nil {
		return nil, err
	}
	after, err := s.check(fd, false)
	if err != nil {
		return nil, err
	}
	if n != len(b) || after.Size != st.Size || sha256.Sum256(b) != digest {
		return nil, failure(Corruption, "input digest or size", nil)
	}
	if err = s.closeFD(fd); err != nil {
		return nil, err
	}
	return b, nil
}

// ReplaceRecord must follow the committed intent; schema/ordering authority is
// the caller's duty. Success includes directory fsync after atomic replacement.
func (s *Session) ReplaceRecord(name string, p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return s.fail(err)
	}
	if err := s.replace(name, p); err != nil {
		return s.fail(err)
	}
	return nil
}

// CreateInput durably creates a final name once, before an acceptance Append.
func (s *Session) CreateInput(name string, p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return s.fail(err)
	}
	if err := s.createInput(name, p); err != nil {
		return s.fail(err)
	}
	return nil
}

// ReadInput verifies a caller-supplied committed digest before returning bytes.
func (s *Session) ReadInput(name string, digest [32]byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return nil, s.fail(err)
	}
	b, err := s.readInput(name, digest)
	if err != nil {
		return nil, s.fail(err)
	}
	return b, nil
}

// Recovery is a temporary local-only capability. Its operations latch failures
// and Session.fail structurally defers teardown until the consumer returns,
// so ownership outlives callbacks even when a helper requests failure teardown.
type Recovery struct {
	s                    *Session
	Budget               *Budget
	active, materialized bool
	err                  error
}

func (r *Recovery) result(err error) error {
	if r.s.callbackFailure != nil {
		return joinFailure(r.s.callbackFailure, r.err, err)
	}
	if r.err != nil {
		return r.err
	}
	if r.Budget.failed {
		return failure(ConsumerFailure, "budget exceeded", nil)
	}
	if err != nil {
		return failure(ConsumerFailure, "consumer", err)
	}
	return nil
}
func (r *Recovery) ReplaceRecord(name string, p []byte) error {
	if !r.active {
		return failure(InvalidState, "expired recovery", nil)
	}
	if r.err != nil {
		return r.err
	}
	if r.Budget.failed {
		r.err = failure(ConsumerFailure, "budget exceeded", nil)
		return r.err
	}
	r.materialized = true
	r.err = r.s.replace(name, p)
	return r.err
}
func (r *Recovery) ReadInput(name string, digest [32]byte) ([]byte, error) {
	if !r.active {
		return nil, failure(InvalidState, "expired recovery", nil)
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.Budget.failed {
		r.err = failure(ConsumerFailure, "budget exceeded", nil)
		return nil, r.err
	}
	b, err := r.s.readInputBudget(name, digest, r.Budget)
	r.err = err
	return b, err
}
