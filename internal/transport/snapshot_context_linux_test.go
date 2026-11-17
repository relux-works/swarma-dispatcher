//go:build linux

package transport

import (
	"context"
	"errors"
	"testing"
)

// Claim: a process owner's already-canceled startup context reaches the snapshot lifecycle and prevents endpoint work, beside a live context that reaches the same endpoint pin.
func TestSnapshotParentCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		f := snapshotControl(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if canceled {
			cancel()
		}
		pins := 0
		calls := daemonCalls{pin: func(commonConfig) (*directory, error) {
			pins++
			return nil, ErrIdentity
		}}
		var err error
		if canceled {
			// Exercise the public context bridge too. Cancellation must prevent
			// its native endpoint pin, whose fixed path cannot match this fixture.
			err = DaemonFromSnapshotContext(ctx, f.load(t), nil)
		} else {
			err = daemonFromSnapshot(f.load(t), nil, calls, ctx)
		}
		if canceled {
			if err != nil || pins != 0 {
				t.Fatalf("canceled parent entered endpoint work: pins=%d err=%v", pins, err)
			}
		} else if !errors.Is(err, ErrIdentity) || pins != 1 {
			t.Fatalf("live parent did not reach the pin control: pins=%d err=%v", pins, err)
		}
	}
}
