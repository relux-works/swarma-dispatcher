package sweep

import (
	"errors"
	"reflect"
)

// Installation is the caller's durable installation fact, never a file-existence guess.
type Installation uint8

const (
	Fresh Installation = iota + 1
	Established
)

var (
	ErrInstallation  = errors.New("sweep: explicit fresh or established installation required")
	ErrNotStarted    = errors.New("sweep: quarantine not started; all uids held")
	ErrRestarted     = errors.New("sweep: startup recovery already attempted")
	ErrConfiguration = errors.New("sweep: shared record store or quarantine configuration refused")
)

// Configure installs the record guard and release seams under the quarantine lock.
// All launchers share one comparable store identity. After recovery starts the
// first configuration is immutable; subsequent launchers share its readiness.
func (q *Quarantine) Configure(store any, gate ReleaseGate, guard func(uint32, string) bool, facts ...Installation) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(facts) > 0 && (len(facts) != 1 || (facts[0] != Fresh && facts[0] != Established) || (q.started && q.installation != facts[0])) {
		return ErrInstallation
	}
	if store == nil || !reflect.TypeOf(store).Comparable() {
		return ErrConfiguration
	}
	if q.scope != nil && q.scope != store {
		return ErrConfiguration
	}
	if q.recovery {
		return nil
	}
	q.scope = store
	q.guard = guard
	if q.gate.WorkProductExport == nil {
		q.gate.WorkProductExport = gate.WorkProductExport
	}
	if q.gate.Operator == nil {
		q.gate.Operator = gate.Operator
	}
	return nil
}

// Recover serializes H6 once for all launchers sharing this quarantine. A lock
// contention in Start occurs before this call and can be retried. No second H6
// can race a Begin or repeat end work; a refused second call preserves readiness.
func (q *Quarantine) Recover(recover func() error) error {
	q.mu.Lock()
	if !q.started || q.blocked != nil {
		err := q.blocked
		q.mu.Unlock()
		return errors.Join(ErrNotStarted, err)
	}
	if q.recovery {
		q.mu.Unlock()
		return ErrRestarted
	}
	q.recovery = true
	q.mu.Unlock()
	err := recover()
	q.mu.Lock()
	defer q.mu.Unlock()
	if err != nil && q.blocked == nil {
		// Failed startup is never readiness; retain its exit duty for later calls.
		q.blocked = err
	}
	if err == nil && q.blocked == nil {
		q.ready = true
	}
	return errors.Join(err, q.blocked)
}

// Ready is shared process readiness, not per-Launcher readiness.
func (q *Quarantine) Ready() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.ready && q.blocked == nil
}

// OwnsDirectory is the lock token checked before an established record store
// acquires its journal lock. Close invalidates it without releasing any lease.
func (q *Quarantine) OwnsDirectory(directory string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lock != nil && q.blocked == nil && q.directory == directory
}

// Entries returns a detached snapshot for H6's record-less lease recovery.
func (q *Quarantine) Entries() []Entry {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := make([]Entry, 0, len(q.held))
	for _, e := range q.held {
		entries = append(entries, e)
	}
	return entries
}

// Decide observes the immutable release configuration under the same lock as mutation.
func (q *Quarantine) Decide(key string, uid uint32, ack bool, e Enumeration) ReleaseDecision {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.gate.Decide(key, uid, ack, e)
}

func (q *Quarantine) keyedLocked(keys []string) error {
	if q.blocked != nil {
		return q.blocked
	}
	if !q.started {
		return ErrNotStarted
	}
	if len(keys) != 1 || keys[0] == "" {
		return ErrQuarantined
	}
	return nil
}
