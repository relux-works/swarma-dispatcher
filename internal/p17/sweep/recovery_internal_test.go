package sweep

import (
	"errors"
	"testing"
)

// Claim: failed recovery retains its error, blocks the whole pool and cannot publish readiness; successful recovery publishes readiness and permits a lease.
func TestRecoveryFailureBlocksPool(t *testing.T) {
	for _, fail := range []bool{true, false} {
		q := privateQuarantine(t, ReleaseGate{}, nil)
		err := q.Recover(func() error {
			if fail {
				return ErrNotDurable
			}
			return nil
		})
		if fail {
			if !errors.Is(err, ErrNotDurable) || !errors.Is(q.Error(), ErrNotDurable) || q.Ready() || !q.Held(7) || q.Lease(7, "next") == nil {
				t.Fatalf("startup error lost: %v current=%v", err, q.Error())
			}
		} else if err != nil || !q.Ready() || q.Lease(7, "control") != nil {
			t.Fatalf("successful control: %v", err)
		}
	}
}
