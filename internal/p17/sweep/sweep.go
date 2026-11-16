// Package sweep implements the D5a zero-process proof and the D5b kill-by-uid
// loop around the privilege-drop helper, plus the lease quarantine decision.
//
// The proof enumerates ALL processes under the procfs root and compares the
// real, effective, saved and filesystem uid of each with R_i. Any pid that
// cannot be read counts as not proved. Killing goes through an injected
// Killer that receives only the uid, never a pid: the helper drops every id
// to R_i and calls kill(-1, SIGKILL). The default Killer refuses by name.
package sweep

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
)

// Enumeration is one D5a scan.
type Enumeration struct {
	UID        uint32
	Scanned    int
	Matched    []int
	Unreadable []int
	Err        error
}

// Proved reports zero processes under the uid with every pid readable.
func (e Enumeration) Proved() bool {
	return e.Err == nil && len(e.Unreadable) == 0 && len(e.Matched) == 0
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Enumerate scans every numeric directory under procRoot.
func Enumerate(procRoot string, uid uint32) Enumeration {
	e := Enumeration{UID: uid}
	if procRoot == "" {
		procRoot = "/proc"
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		e.Err = errors.Join(errors.New("sweep: enumeration failed"), err)
		return e
	}
	for _, ent := range entries {
		if !allDigits(ent.Name()) {
			continue
		}
		pid, err := strconv.Atoi(ent.Name())
		if err != nil || pid <= 0 {
			continue
		}
		e.Scanned++
		uids, err := incarnation.ReadUIDs(procRoot, pid)
		if err != nil {
			if errors.Is(err, incarnation.ErrGone) {
				// Exited between readdir and read: it is not a process any more.
				e.Scanned--
				continue
			}
			e.Unreadable = append(e.Unreadable, pid)
			continue
		}
		if uids[0] == uid || uids[1] == uid || uids[2] == uid || uids[3] == uid {
			e.Matched = append(e.Matched, pid)
		}
	}
	sort.Ints(e.Matched)
	sort.Ints(e.Unreadable)
	return e
}

// Killer is the helper's sweep mode. It receives only the uid.
type Killer interface {
	KillAll(ctx context.Context, uid uint32) error
}

// HelperPin names the helper the default Killer waits for.
const HelperPin = "privilege-drop helper (r2.6 PENDING item 3, sweep mode)"

// UnpinnedKiller refuses by name.
type UnpinnedKiller struct{}

func (UnpinnedKiller) KillAll(context.Context, uint32) error {
	return codes.Unpinned("uid sweep killer", HelperPin)
}

// Budget bounds the kill loop.
type Budget struct {
	// Attempts is the maximum number of kill rounds.
	Attempts int
	// Pause is the wait between a kill round and the next enumeration.
	Pause time.Duration
}

// DefaultBudget is a bounded default: five rounds, 50 ms apart.
var DefaultBudget = Budget{Attempts: 5, Pause: 50 * time.Millisecond}

// ErrNotProved means the budget ended without a zero-process proof.
var ErrNotProved = errors.New("sweep: zero-process proof not established")

// Outcome is the result of Sweep.
type Outcome struct {
	Final Enumeration
	Kills int
	Err   error
}

// Sweep enumerates, kills by uid, and repeats within the budget until the
// enumeration proves zero. It never signals a pid by number.
func Sweep(ctx context.Context, procRoot string, uid uint32, k Killer, b Budget) Outcome {
	if ctx == nil {
		ctx = context.Background()
	}
	if b.Attempts <= 0 {
		b.Attempts = DefaultBudget.Attempts
	}
	if b.Pause < 0 {
		b.Pause = 0
	}
	var out Outcome
	for attempt := 0; ; attempt++ {
		out.Final = Enumerate(procRoot, uid)
		if out.Final.Proved() {
			return out
		}
		if out.Final.Err != nil {
			out.Err = out.Final.Err
			return out
		}
		if attempt >= b.Attempts {
			out.Err = ErrNotProved
			return out
		}
		if k == nil {
			out.Err = errors.New("sweep: no killer")
			return out
		}
		if err := k.KillAll(ctx, uid); err != nil {
			out.Err = err
			return out
		}
		out.Kills++
		if b.Pause > 0 {
			t := time.NewTimer(b.Pause)
			select {
			case <-ctx.Done():
				t.Stop()
				out.Err = ctx.Err()
				return out
			case <-t.C:
			}
		}
	}
}

// Archival is release seam (a) of the R_i release gate (book ch. 07, Core
// G1): work-product archival evidence for one run. The producer of that
// evidence belongs to other slices; the default here refuses by name.
type Archival interface {
	Archived(runKey string) (bool, error)
}

// OperatorRelease is release seam (b): an explicit operator release of the
// uid for one run, recorded by operator recovery. The default refuses.
type OperatorRelease interface {
	Released(runKey string, uid uint32) (bool, error)
}

// Pins the two release seams wait for.
const (
	ArchivalPin        = "work-product archival producer (S1/S8; book ch. 07, Core G1)"
	OperatorReleasePin = "operator release record (operator recovery, r2.6 H6)"
)

// UnpinnedArchival refuses by name: archival status is unknown.
type UnpinnedArchival struct{}

func (UnpinnedArchival) Archived(string) (bool, error) {
	return false, codes.Unpinned("work-product archival evidence", ArchivalPin)
}

// UnpinnedOperatorRelease refuses by name: no operator release exists.
type UnpinnedOperatorRelease struct{}

func (UnpinnedOperatorRelease) Released(string, uint32) (bool, error) {
	return false, codes.Unpinned("operator release", OperatorReleasePin)
}

// StaticArchival is a fixed table keyed by run key, for tests and for
// deployments that inject an already-read evidence table. A missing key is
// "not archived", not an error.
type StaticArchival map[string]bool

func (s StaticArchival) Archived(runKey string) (bool, error) { return s[runKey], nil }

// StaticOperatorRelease is a fixed table keyed by run key; the uid must match
// the recorded one.
type StaticOperatorRelease map[string]uint32

func (s StaticOperatorRelease) Released(runKey string, uid uint32) (bool, error) {
	u, ok := s[runKey]
	return ok && u == uid, nil
}

// ReleaseGate is the R_i release decision: the D5a zero-process proof AND one
// of (a) archival evidence with an acknowledged end, or (b) an explicit
// operator release. A nil seam is the refusing default.
type ReleaseGate struct {
	Archival Archival
	Operator OperatorRelease
}

// ReleaseDecision records every input of one gate evaluation.
type ReleaseDecision struct {
	RunKey           string
	UID              uint32
	Proved           bool
	EndAcknowledged  bool
	Archived         bool
	ArchivalErr      error
	OperatorReleased bool
	OperatorErr      error
	Release          bool
	Reason           string
}

// ReleaseAllowed is the pure decision: proof AND (operator release OR
// (acknowledged end AND archival)). Unknown evidence is passed as false.
func ReleaseAllowed(e Enumeration, endAcknowledged, archived, operatorReleased bool) bool {
	return e.Proved() && (operatorReleased || (endAcknowledged && archived))
}

// Decide evaluates the gate for one run. A missing, unreadable or unknown
// archival or release status counts as absent and keeps the uid quarantined.
func (g ReleaseGate) Decide(runKey string, uid uint32, endAcknowledged bool, e Enumeration) ReleaseDecision {
	d := ReleaseDecision{RunKey: runKey, UID: uid, EndAcknowledged: endAcknowledged}
	d.Proved = e.UID == uid && e.Proved()
	a := g.Archival
	if a == nil {
		a = UnpinnedArchival{}
	}
	o := g.Operator
	if o == nil {
		o = UnpinnedOperatorRelease{}
	}
	d.Archived, d.ArchivalErr = a.Archived(runKey)
	if d.ArchivalErr != nil {
		d.Archived = false
	}
	d.OperatorReleased, d.OperatorErr = o.Released(runKey, uid)
	if d.OperatorErr != nil {
		d.OperatorReleased = false
	}
	switch {
	case e.UID != uid:
		d.Reason = "enumeration is for another uid"
	case !e.Proved():
		d.Reason = "zero-process proof not established"
	case d.OperatorReleased:
		d.Reason = "operator release"
	case !endAcknowledged:
		d.Reason = "end not acknowledged and no operator release"
	case !d.Archived:
		d.Reason = "no archival evidence and no operator release"
	default:
		d.Reason = "acknowledged end with archival evidence"
	}
	d.Release = d.Proved && ReleaseAllowed(e, endAcknowledged, d.Archived, d.OperatorReleased)
	return d
}

// Quarantine tracks R_i uids that may not be leased. A uid enters on lease
// and leaves only through Release when the gate allows it.
type Quarantine struct {
	Gate ReleaseGate

	mu   sync.Mutex
	held map[uint32]bool
}

// ErrQuarantined refuses a lease of a held uid, and reports a refused release.
var ErrQuarantined = errors.New("sweep: uid quarantined")

// Hold marks the uid as leased/quarantined.
func (q *Quarantine) Hold(uid uint32) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.held == nil {
		q.held = make(map[uint32]bool)
	}
	q.held[uid] = true
}

// Held reports whether the uid is quarantined.
func (q *Quarantine) Held(uid uint32) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.held[uid]
}

// Lease refuses a held uid and otherwise holds it for the new run.
func (q *Quarantine) Lease(uid uint32) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.held[uid] {
		return ErrQuarantined
	}
	if q.held == nil {
		q.held = make(map[uint32]bool)
	}
	q.held[uid] = true
	return nil
}

// Release frees the uid only when the gate allows it for this run; otherwise
// the uid stays held and ErrQuarantined is returned with the decision.
func (q *Quarantine) Release(runKey string, uid uint32, endAcknowledged bool, e Enumeration) (ReleaseDecision, error) {
	d := q.Gate.Decide(runKey, uid, endAcknowledged, e)
	q.mu.Lock()
	defer q.mu.Unlock()
	if !d.Release {
		if q.held == nil {
			q.held = make(map[uint32]bool)
		}
		q.held[uid] = true
		return d, ErrQuarantined
	}
	delete(q.held, uid)
	return d, nil
}
