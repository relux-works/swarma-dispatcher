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

// Frame kinds in the journal.
const (
	genesisKind    = "p17-run-store/1"
	transitionKind = "p17-run-transition/1"
)

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
}

func newStore() *Store {
	return &Store{records: make(map[string]*Record), fenced: make(map[string]bool)}
}

// Initialize creates a fresh store: the genesis frame is sequence 1.
func Initialize(cfg journal.Config) (*Store, error) {
	gen, err := json.Marshal(frame{V: Version, Kind: genesisKind})
	if err != nil {
		return nil, err
	}
	st := newStore()
	s, err := journal.Initialize(cfg, gen)
	if err != nil {
		return nil, err
	}
	st.s = s
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

// Consume implements journal.Consumer. Sequence 1 must be the genesis frame;
// every later frame is one complete record after a transition. Replay applies
// the same transition rules as live writes, so a torn or foreign frame ends
// recovery instead of being adopted.
func (st *Store) Consume(seq uint64, payload []byte, r *journal.Recovery) error {
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
	if seq == 1 {
		if f.V != Version || f.Kind != genesisKind || f.Record != nil {
			return errors.Join(ErrReplay, errors.New("runrecord: genesis frame"))
		}
		return nil
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

// commit appends the transition frame, then materializes the record. A
// committed append is applied in memory even if materialization fails; the
// journal session is then closed and every later call fails.
func (st *Store) commit(rec *Record) error {
	p, err := json.Marshal(frame{V: Version, Kind: transitionKind, Record: rec})
	if err != nil {
		return err
	}
	if _, err := st.s.Append(p); err != nil {
		return err
	}
	st.records[rec.Key] = rec
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return st.s.ReplaceRecord(RecordName(rec.Key), b)
}

// Create writes a new record, which must be in state binding.
func (st *Store) Create(r *Record) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed || st.s == nil {
		return ErrClosed
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
	if st.closed || st.s == nil {
		return nil, ErrClosed
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

// State returns the journal session state.
func (st *Store) State() journal.State {
	st.mu.Lock()
	defer st.mu.Unlock()
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

// MustExit reports the journal's process-exit duty after a sync failure.
func MustExit(err error) bool { return journal.MustExit(err) }
