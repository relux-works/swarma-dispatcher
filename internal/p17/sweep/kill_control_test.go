package sweep_test

import (
	"context"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Claim: hidepid cannot prevent bounded kill rounds, but prevents proof/release; the qualified neighbour kills and proves zero.
func TestUnqualifiedViewStillKills(t *testing.T) {
	for _, hidden := range []bool{true, false} {
		name := "qualified"
		if hidden {
			name = "hidepid"
		}
		t.Run(name, func(t *testing.T) {
			p := tree(t)
			add(t, p, 77, [4]uint32{ri, ri, ri, ri})
			if hidden {
				if err := p.SetMount("proc", "/", "rw,hidepid=2"); err != nil {
					t.Fatal(err)
				}
			}
			k := &fakes.Killer{Tree: p}
			q := newQuarantine(t, sweep.ReleaseGate{})
			if err := q.Hold(ri, "run"); err != nil {
				t.Fatal(err)
			}
			out := q.WithLease(ri, "run", func() sweep.Outcome {
				if err := q.MarkHelper(ri, "run"); err != nil {
					t.Fatal(err)
				}
				return sweep.Sweep(context.Background(), p.View(), ri, k, sweep.Budget{Attempts: 3})
			})
			if !q.HelperInvoked(ri) {
				t.Fatal("sweep did not use its quarantine")
			}
			if hidden {
				if out.Kills != 3 || out.Final.Proved() || out.Err == nil || !q.Held(ri) {
					t.Fatalf("hidden view skipped kills or proved: %+v", out)
				}
			} else if out.Kills != 1 || !out.Final.Proved() || out.Err != nil {
				t.Fatalf("qualified control: %+v", out)
			}
		})
	}
}
