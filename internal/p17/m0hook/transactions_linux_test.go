//go:build linux && (amd64 || arm64)

package m0hook_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/m0hook"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
)

// Claim: Stop forced between claim and the Start lock fences all event/client contact with ErrStopped; a Start allowed to lock first commits and ends normally.
func TestClaimedStopFencesStart(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			e := newEnv(t)
			h := e.build()
			claimed, release := make(chan struct{}), make(chan struct{})
			m0hook.AfterClaim(h, func() { close(claimed); <-release })
			type result struct {
				r   m0.StartReceipt
				err error
			}
			done := make(chan result, 1)
			go func() {
				r, err := h.Start(context.Background(), startReq("effect", 30001), e.gate)
				done <- result{r, err}
			}()
			<-claimed
			if stopped {
				r, err := h.Stop(context.Background(), stopReq("stop", 30001))
				must(t, err)
				if !r.Absent {
					t.Fatal("early Stop receipt lost")
				}
			}
			close(release)
			out := <-done
			if stopped {
				if !errors.Is(out.err, m0hook.ErrStopped) || out.r.Committed || e.events.count() != 0 || e.kmDialer.Dials() != 0 || len(e.store.Keys()) != 0 {
					t.Fatalf("stopped claim contacted a seam: %+v %v", out.r, out.err)
				}
				starts, stops := e.inner.counts()
				if starts != 0 || stops != 1 {
					t.Fatalf("early stop counts: %d %d", starts, stops)
				}
			} else {
				must(t, out.err)
				if !out.r.Committed {
					t.Fatal("control failed to commit")
				}
				_, err := h.Stop(context.Background(), stopReq("stop", 30001))
				must(t, err)
				if e.only().State != runrecord.Ended {
					t.Fatal("control failed to end")
				}
			}
		})
	}
}

// Claim: a second Stop forced to wait for a first lost End returns the same unknown refusal without another End; acknowledged End returns nil for both.
func TestConcurrentHeldStops(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			if lost {
				e.km.EndMode = "close"
			}
			h := e.build()
			_, err := h.Start(context.Background(), startReq("effect", 30001), e.gate)
			must(t, err)
			entered, release := make(chan struct{}), make(chan struct{})
			g.set(func(r *runrecord.Record) error {
				if r.State == runrecord.Ending && !r.TeardownDone {
					close(entered)
					<-release
				}
				return nil
			})
			done := make(chan error, 2)
			go func() { _, err := h.Stop(context.Background(), stopReq("first-stop", 30001)); done <- err }()
			<-entered
			waiting := make(chan struct{})
			m0hook.OnEffectWait(h, func() { close(waiting) })
			go func() { _, err := h.Stop(context.Background(), stopReq("second-stop", 30001)); done <- err }()
			<-waiting
			close(release)
			for i := 0; i < 2; i++ {
				err := <-done
				if (err == nil) == lost || codes.Is(err, codes.EndUnknown) != lost {
					t.Fatalf("concurrent Stop lost retained refusal: %v", err)
				}
			}
			if len(e.km.Ends()) != 1 {
				t.Fatalf("concurrent stops retried End: %d", len(e.km.Ends()))
			}
		})
	}
}

// Claim: sync-carrying Inner.Start and incarnation errors directly latch fatal duty without H5; their ordinary neighbours end once.
func TestChildSeamFatalDuty(t *testing.T) {
	for _, stage := range []string{"inner", "incarnation"} {
		for _, fatal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-%v", stage, fatal), func(t *testing.T) {
				e := newEnv(t)
				cause := errStoreRefused
				if fatal {
					cause = errors.Join(cause, journal.ErrSyncFailure)
				}
				if stage == "inner" {
					e.inner.startErr = cause
				} else {
					e.reader.err = cause
				}
				h := e.build()
				_, err := h.Start(context.Background(), startReq("effect", 30001), e.gate)
				if !errors.Is(err, cause) || m0.MustExit(err) != fatal || errors.Is(err, m0hook.ErrMustExit) != fatal || h.ExitRequired() != fatal {
					t.Fatalf("child seam fatal duty: %v", err)
				}
				ends := 1
				state := runrecord.Ended
				if fatal {
					ends = 0
					state = runrecord.Bound
				}
				if len(e.km.Ends()) != ends || e.only().State != state {
					t.Fatal("child seam performed forbidden H5 or skipped ordinary cleanup")
				}
			})
		}
	}
}
