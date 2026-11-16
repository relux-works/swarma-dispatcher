package runrecord

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

// transitionKind is the only frame kind in the journal: one complete record
// after one real transition. There is no synthetic genesis frame; sequence 1
// is the first real record transition (spec/journal.md: "No synthetic
// genesis or semantic authority is invented").
const transitionKind = "p17-run-transition/1"

type frame struct {
	V      int     `json:"v"`
	Kind   string  `json:"kind"`
	Record *Record `json:"record,omitempty"`
}

func decodeStrict(p []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("runrecord: trailing bytes")
	}
	return nil
}

// RecordName is the record file basename for a key.
func RecordName(key string) string { return "run-" + key }

var (
	// ErrExists refuses a second Create for the same key.
	ErrExists = errors.New("runrecord: record exists")
	// ErrMissing refuses an Update of an unknown key.
	ErrMissing = errors.New("runrecord: no such record")
	// ErrClosed refuses use after Close.
	ErrClosed = errors.New("runrecord: store closed")
	// ErrReplay reports a journal frame the store cannot accept.
	ErrReplay = errors.New("runrecord: replay rejected")
	// ErrCommit marks a failure at or after the journal append of a
	// transition (including the initializing first append). The transition
	// may or may not be durable: callers must treat the record state as
	// uncertain, never as "no record".
	ErrCommit = errors.New("runrecord: transition commit failed; durability uncertain")
)

// Store owns one journal session and the in-memory view of its records.
type Store struct {
	mu      sync.Mutex
	s       *journal.Session
	records map[string]*Record
	fenced  map[string]bool
	closed  bool
	// recovering is true only inside OpenEstablished callbacks.
	recovering bool
	// pending is true for a fresh store whose journal does not exist yet:
	// the first real record transition initializes it. cfg is kept for that.
	pending bool
	cfg     journal.Config
	// initErr is the sticky failure of that one initialization; it is never
	// retried in this store.
	initErr error
}

func newStore() *Store {
	return &Store{records: make(map[string]*Record), fenced: make(map[string]bool)}
}

// Initialize prepares a fresh store for installation-established fresh
// state. It writes nothing: the journal is initialized by the first real
// record transition (the first Create), whose frame becomes sequence 1, as
// spec/journal.md requires of a journal's first payload. Until then no
// journal file and no ownership lock exist, and a restart must choose
// Initialize again; Open refuses a store that never committed a record.
// Configuration errors surface at that first Create. A failed initialization
// is never retried by this store.
func Initialize(cfg journal.Config) (*Store, error) {
	st := newStore()
	st.pending = true
	st.cfg = cfg
	return st, nil
}

// Open replays an established store. Every loaded record is fenced.
func Open(cfg journal.Config) (*Store, error) {
	st := newStore()
	st.recovering = true
	s, err := journal.OpenEstablished(cfg, st)
	st.recovering = false
	if err != nil {
		return nil, err
	}
	st.s = s
	for k := range st.records {
		st.fenced[k] = true
	}
	return st, nil
}

// Consume implements journal.Consumer. Every frame, sequence 1 included, is
// one complete record after a real transition. Replay applies the same
// transition rules as live writes, so a torn or foreign frame (a synthetic
// genesis frame included) ends recovery instead of being adopted.
func (st *Store) Consume(_ uint64, payload []byte, r *journal.Recovery) error {
	if !st.recovering {
		return errors.Join(ErrReplay, errors.New("runrecord: consume outside recovery"))
	}
	if err := r.Budget.Charge(uint64(len(payload)) + 1024); err != nil {
		return err
	}
	var f frame
	if err := decodeStrict(payload, &f); err != nil {
		return errors.Join(ErrReplay, err)
	}
	if f.V != Version || f.Kind != transitionKind || f.Record == nil {
		return errors.Join(ErrReplay, errors.New("runrecord: transition frame"))
	}
	rec := f.Record
	old, ok := st.records[rec.Key]
	if !ok {
		if rec.State != Binding {
			return errors.Join(ErrReplay, errors.New("runrecord: first frame of a run is not binding"))
		}
		if err := rec.Validate(); err != nil {
			return errors.Join(ErrReplay, err)
		}
	} else if err := CheckTransition(old, rec); err != nil {
		return errors.Join(ErrReplay, err)
	}
	st.records[rec.Key] = rec
	return nil
}

// Finish implements journal.Consumer: materialize every record.
func (st *Store) Finish(r *journal.Recovery) error {
	keys := st.keysLocked()
	for _, k := range keys {
		b, err := json.Marshal(st.records[k])
		if err != nil {
			return err
		}
		if err := r.ReplaceRecord(RecordName(k), b); err != nil {
			return err
		}
	}
	return nil
}

func (st *Store) keysLocked() []string {
	keys := make([]string, 0, len(st.records))
	for k := range st.records {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// usableLocked reports whether the store can take a transition.
func (st *Store) usableLocked() error {
	if st.closed {
		return ErrClosed
	}
	if st.initErr != nil {
		return errors.Join(ErrClosed, st.initErr)
	}
	if st.s == nil && !st.pending {
		return ErrClosed
	}
	return nil
}

// commit appends the transition frame, then materializes the record. On a
// fresh store the first transition frame initializes the journal as its
// sequence 1. A committed append is applied in memory even if
// materialization fails; the journal session is then closed and every later
// call fails. Every failure from the append on carries ErrCommit.
func (st *Store) commit(rec *Record) error {
	p, err := json.Marshal(frame{V: Version, Kind: transitionKind, Record: rec})
	if err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if st.pending {
		s, err := journal.Initialize(st.cfg, p)
		st.pending = false
		if err != nil {
			st.initErr = err
			return errors.Join(ErrCommit, err)
		}
		st.s = s
	} else if _, err := st.s.Append(p); err != nil {
		return errors.Join(ErrCommit, err)
	}
	st.records[rec.Key] = rec
	if err := st.s.ReplaceRecord(RecordName(rec.Key), b); err != nil {
		return errors.Join(ErrCommit, err)
	}
	return nil
}

// Create writes a new record, which must be in state binding.
func (st *Store) Create(r *Record) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.usableLocked(); err != nil {
		return err
	}
	if r == nil {
		return invalid("nil record")
	}
	rec := r.Clone()
	if rec.State != Binding {
		return invalid("create requires state binding")
	}
	if err := rec.Validate(); err != nil {
		return err
	}
	if _, ok := st.records[rec.Key]; ok {
		return ErrExists
	}
	return st.commit(rec)
}

// Update applies mutate to a copy of the record and commits it if the
// transition is allowed. The stored record is untouched on refusal.
func (st *Store) Update(key string, mutate func(*Record) error) (*Record, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.usableLocked(); err != nil {
		return nil, err
	}
	old, ok := st.records[key]
	if !ok {
		return nil, ErrMissing
	}
	next := old.Clone()
	if err := mutate(next); err != nil {
		return nil, err
	}
	if err := CheckTransition(old, next); err != nil {
		return nil, err
	}
	if err := st.commit(next); err != nil {
		return nil, err
	}
	return next.Clone(), nil
}

// Get returns a copy of the record.
func (st *Store) Get(key string) (*Record, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	r, ok := st.records[key]
	if !ok {
		return nil, false
	}
	return r.Clone(), true
}

// Keys returns every record key, sorted.
func (st *Store) Keys() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.keysLocked()
}

// Fenced reports whether the record was loaded at restart. Nothing in v1
// unfences a record.
func (st *Store) Fenced(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.fenced[key]
}

// State returns the journal session state. A fresh store that has not yet
// committed its first record has no session and reports the zero State.
func (st *Store) State() journal.State {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pending && !st.closed {
		return journal.State{}
	}
	if st.s == nil {
		return journal.State{Closed: true}
	}
	return st.s.State()
}

// Close ends the journal session.
func (st *Store) Close() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return nil
	}
	st.closed = true
	if st.s == nil {
		return nil
	}
	return st.s.Close()
}

// Uncertain reports whether err left a transition's durability uncertain.
func Uncertain(err error) bool { return errors.Is(err, ErrCommit) }

// MustExit reports the journal's process-exit duty after a sync failure.
func MustExit(err error) bool { return journal.MustExit(err) }
