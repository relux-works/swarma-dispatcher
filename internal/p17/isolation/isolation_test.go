package isolation

import (
	"errors"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

type failing struct{ err error }

func (f failing) ChildUID(uint32) (uint32, error) { return 0, f.err }

// Claim (H0, D1): a placement that gives R_i a uid distinct from L qualifies;
// the decision carries both uids.
func TestCheckQualified(t *testing.T) {
	d, err := Check(Fixed(2001), 1000)
	if err != nil || d.LauncherUID != 1000 || d.ChildUID != 2001 {
		t.Fatalf("%+v %v", d, err)
	}
}

// Claim (r2.6 Controls: "With R_i equal to L, enforcing refuses"): a placement
// that returns the launcher's own uid refuses run_isolation_unqualified.
func TestCheckSameUIDRefuses(t *testing.T) {
	_, err := Check(Fixed(1000), 1000)
	if !codes.Is(err, codes.IsolationUnqualified) {
		t.Fatalf("got %v", err)
	}
}

// Claim (H0): the default placement refuses by name, waiting for the
// privilege-drop helper, and Check turns that into run_isolation_unqualified
// while keeping the unpinned cause visible. A nil placement also refuses.
func TestCheckUnpinnedAndNil(t *testing.T) {
	_, err := Check(Unpinned{}, 1000)
	if !codes.Is(err, codes.IsolationUnqualified) || !codes.IsUnpinned(err) {
		t.Fatalf("got %v", err)
	}
	_, err = Check(nil, 1000)
	if !codes.Is(err, codes.IsolationUnqualified) {
		t.Fatalf("nil placement: %v", err)
	}
}

// Claim (H0): a placement failure of any kind is run_isolation_unqualified
// with the cause retained; it is never a different code.
func TestCheckPlacementFailure(t *testing.T) {
	_, err := Check(failing{ErrNoLease}, 1000)
	if !codes.Is(err, codes.IsolationUnqualified) || !errors.Is(err, ErrNoLease) {
		t.Fatalf("got %v", err)
	}
}
