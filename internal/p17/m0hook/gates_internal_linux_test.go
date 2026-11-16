//go:build linux && (amd64 || arm64)

package m0hook

import (
	"context"
	"errors"
	"testing"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
)

type countedEvents struct{ calls int }

func (s *countedEvents) Event(m0.StartRequest) (runorigin.EventRef, error) {
	s.calls++
	return runorigin.EventRef{}, errors.New("fixture ordinary event refusal")
}

// Claim: the post-claim lock boundary rechecks fatal duty before any seam; the same claimed path runs the event seam without a latch (adjacent control).
func TestPostClaimLatch(t *testing.T) {
	for _, latched := range []bool{false, true} {
		events := &countedEvents{}
		h := &Hooked{l: &launcher.Launcher{}, restarted: true, events: events}
		req := m0.StartRequest{EffectID: "effect", ExecutionID: "execution"}
		e := h.claim(req)
		if latched {
			h.latch(fatalOf(errors.New("fixture fatal flag")))
		}
		_, err := h.startEffect(context.Background(), e, req, nil)
		expected := 1
		if latched {
			expected = 0
		}
		if m0.MustExit(err) != latched || events.calls != expected {
			t.Fatalf("latch=%v err=%v calls=%d", latched, err, events.calls)
		}
	}
}

// Claim: a restart report's outcome-only MustExit flag latches fatal duty even with plain causes; healthy recovery enables Start and ordinary failure does not.
func TestRestartOutcomeFlag(t *testing.T) {
	for _, name := range []string{"healthy", "ordinary", "flag"} {
		t.Run(name, func(t *testing.T) {
			h := &Hooked{l: &launcher.Launcher{}, events: &countedEvents{}}
			var reports []launcher.Report
			if name != "healthy" {
				reports = []launcher.Report{{Err: errors.New("fixture recovery failure"), Outcome: &launcher.EndOutcome{MustExit: name == "flag", RecordErr: errors.New("fixture record failure")}}}
			}
			err := h.acceptRestart(reports)
			if (err == nil) != (name == "healthy") || m0.MustExit(err) != (name == "flag") || h.restarted != (name == "healthy") || h.ExitRequired() != (name == "flag") {
				t.Fatalf("report %s error=%v restarted=%v", name, err, h.restarted)
			}
			_, err = h.Start(context.Background(), m0.StartRequest{EffectID: "effect", ExecutionID: "execution"}, nil)
			switch name {
			case "healthy":
				if err != nil {
					t.Fatalf("healthy Start %v", err)
				}
			case "ordinary":
				if !errors.Is(err, ErrNotRestarted) {
					t.Fatalf("failed recovery Start %v", err)
				}
			case "flag":
				if !m0.MustExit(err) || !errors.Is(err, ErrMustExit) {
					t.Fatalf("flag-only Start %v", err)
				}
			}
		})
	}
}

type stoppedInner struct{ calls int }

func (*stoppedInner) Start(context.Context, m0.StartRequest, m0.CommitGate) (m0.StartReceipt, error) {
	panic("unexpected Start")
}
func (s *stoppedInner) Stop(context.Context, m0.StopRequest) (m0.StopReceipt, error) {
	s.calls++
	return m0.StopReceipt{Absent: true}, nil
}
func (*stoppedInner) Observe(context.Context, m0.EffectQuery) (m0.Observation, error) {
	return m0.ObservationUnknown, nil
}

// Claim: pre-restart Start refuses before the event seam, whereas Stop still calls Inner and returns ErrNotRestarted; healthy recovery is the neighbouring control.
func TestConstructionGuard(t *testing.T) {
	for _, ready := range []bool{false, true} {
		events := &countedEvents{}
		inner := &stoppedInner{}
		h := &Hooked{l: &launcher.Launcher{}, restarted: ready, events: events, inner: inner}
		_, err := h.Start(context.Background(), m0.StartRequest{EffectID: "effect", ExecutionID: "execution"}, nil)
		expected := 0
		if ready {
			expected = 1
		}
		if errors.Is(err, ErrNotRestarted) == ready || events.calls != expected {
			t.Fatalf("ready=%v err=%v events=%d", ready, err, events.calls)
		}
		receipt, err := h.Stop(context.Background(), m0.StopRequest{EffectID: "stop", ExecutionID: "unknown"})
		if !receipt.Absent || inner.calls != 1 || errors.Is(err, ErrNotRestarted) == ready {
			t.Fatalf("Stop ready=%v receipt=%+v err=%v calls=%d", ready, receipt, err, inner.calls)
		}
	}
}
