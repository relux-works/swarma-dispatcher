// Package isolation implements H0 of the P17 hook interface: before any
// Source or Keymaker contact, decide whether the agent child R_i can be
// placed apart from the launcher L.
//
// The decision comes from an injected Placement. The default placement is
// the privilege-drop helper of r2.6 PENDING item 3, which this repository
// does not ship; it therefore refuses by name. Enforcing never falls back to
// a non-enforcing launch here.
package isolation

import (
	"errors"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

// Placement answers which uid the child of this launch will run under. It
// must not contact Source or Keymaker; it is consulted before either.
type Placement interface {
	// ChildUID returns the uid leased to R_i for one run. An error means no
	// qualifying placement exists.
	ChildUID(launcherUID uint32) (uint32, error)
}

// Decision is a qualified placement: distinct uids for L and R_i.
type Decision struct {
	LauncherUID uint32
	ChildUID    uint32
}

// HelperPin names the privilege-drop helper this placement waits for.
const HelperPin = "privilege-drop helper (r2.6 PENDING item 3, spawn mode)"

// Unpinned is the default placement: no helper, no placement, refuse.
type Unpinned struct{}

func (Unpinned) ChildUID(uint32) (uint32, error) {
	return 0, codes.Unpinned("isolation placement", HelperPin)
}

// Check runs H0. It refuses run_isolation_unqualified when the placement is
// absent, fails, or returns the launcher's own uid. It has no side effects.
func Check(p Placement, launcherUID uint32) (Decision, error) {
	if p == nil {
		return Decision{}, codes.Refuse(codes.IsolationUnqualified, "no placement", nil)
	}
	child, err := p.ChildUID(launcherUID)
	if err != nil {
		return Decision{}, codes.Refuse(codes.IsolationUnqualified, "placement", err)
	}
	if child == launcherUID {
		return Decision{}, codes.Refuse(codes.IsolationUnqualified, "child uid equals launcher uid", nil).With("uid", itoa(child))
	}
	return Decision{LauncherUID: launcherUID, ChildUID: child}, nil
}

// Fixed is a placement that always returns one uid. It is for deployments
// and tests that have already leased a uid; it does not lease anything.
type Fixed uint32

func (f Fixed) ChildUID(uint32) (uint32, error) { return uint32(f), nil }

// ErrNoLease is returned by placements that have no free uid.
var ErrNoLease = errors.New("isolation: no uid lease available")

func itoa(u uint32) string {
	if u == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}
