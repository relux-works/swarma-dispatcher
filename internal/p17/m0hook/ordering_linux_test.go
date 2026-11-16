//go:build linux && (amd64 || arm64)

package m0hook_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/m0hook"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

type waitingEvents struct{ entered, release chan struct{} }

func (s waitingEvents) Event(m0.StartRequest) (runorigin.EventRef, error) {
	close(s.entered)
	<-s.release
	return fixtureEvent, nil
}

// Claim: a known Stop cannot reach Inner.Stop while its claimed Start is still running; after release both finish and the one run is ended.
func TestStopWaitsForKnownStart(t *testing.T) {
	e := newEnv(t)
	d := e.hookDeps()
	s := waitingEvents{make(chan struct{}), make(chan struct{})}
	d.Events = s
	h, err := m0hook.New(context.Background(), d)
	must(t, err)
	waiting := make(chan struct{})
	m0hook.OnEffectWait(h, func() { close(waiting) })
	var startReturned atomic.Bool
	e.inner.atStop = func() {
		if !startReturned.Load() {
			t.Error("Inner.Stop overtook a known Start")
		}
	}
	startDone := make(chan error, 1)
	go func() { _, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate); startDone <- err }()
	<-s.entered
	stopDone := make(chan error, 1)
	go func() {
		_, err := h.Stop(context.Background(), stopReq("stop", cuid))
		stopDone <- err
	}()
	// A contended acquisition, not a scheduling hint or elapsed interval.
	<-waiting
	startReturned.Store(true)
	close(s.release)
	must(t, <-startDone)
	must(t, <-stopDone)
	if e.only().State != runrecord.Ended || len(e.km.Ends()) != 1 {
		t.Fatalf("final state=%s ends=%d", e.only().State, len(e.km.Ends()))
	}
}

// Claim: Stop always calls Inner, but a prior latch or a fatal inner Stop forbids H5 and preserves the fatal class; a plain inner error still ends (adjacent control).
func TestStopFatalPolicy(t *testing.T) {
	for _, name := range []string{"ordinary", "latched", "inner-sync"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			h := e.build()
			_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			must(t, err)
			var writes atomic.Int32
			if name == "latched" {
				e.events.err = journal.ErrSyncFailure
				_, err = h.Start(context.Background(), startReq("other", cuid), e.gate)
				if !m0.MustExit(err) {
					t.Fatal("failed to latch")
				}
			}
			if name == "inner-sync" {
				e.inner.stopErr = errors.Join(errStoreRefused, journal.ErrSyncFailure)
			} else {
				e.inner.stopErr = errStoreRefused
			}
			g.set(func(*runrecord.Record) error { writes.Add(1); return nil })
			receipt, err := h.Stop(context.Background(), stopReq("stop", cuid))
			fatal := name != "ordinary"
			if !receipt.Absent || !errors.Is(err, errStoreRefused) || m0.MustExit(err) != fatal || h.ExitRequired() != fatal {
				t.Fatalf("receipt %+v error %v latch=%v", receipt, err, h.ExitRequired())
			}
			if _, stops := e.inner.counts(); stops != 1 {
				t.Fatalf("inner stops=%d", stops)
			}
			if fatal {
				if writes.Load() != 0 || len(e.km.Ends()) != 0 || e.only().State != runrecord.Bound {
					t.Fatalf("state I/O after latch: writes=%d ends=%d state=%s", writes.Load(), len(e.km.Ends()), e.only().State)
				}
			} else if writes.Load() == 0 || len(e.km.Ends()) != 1 || e.only().State != runrecord.Ended {
				t.Fatal("ordinary Stop skipped H5")
			}
		})
	}
}

// Claim: empty Start identities refuse before any claim/contact; empty Stop effect still calls Inner before refusing without H5 (valid starts/stops beside them).
func TestRequiredIdentities(t *testing.T) {
	for _, field := range []string{"effect", "execution", "valid"} {
		t.Run(field, func(t *testing.T) {
			e := newEnv(t)
			h := e.build()
			r := startReq(effectA, cuid)
			want := m0hook.ErrEffectRequired
			if field == "effect" {
				r.EffectID = ""
			} else if field == "execution" {
				r.ExecutionID = ""
				want = m0hook.ErrExecutionRequired
			}
			receipt, err := h.Start(context.Background(), r, e.gate)
			if field != "valid" {
				if !errors.Is(err, want) || receipt.Committed || e.events.count() != 0 || len(e.store.Keys()) != 0 {
					t.Fatalf("invalid identity reached gates: %+v %v", receipt, err)
				}
				return
			}
			must(t, err)
			if !receipt.Committed {
				t.Fatal("valid identity refused")
			}
			q := stopReq("", cuid)
			_, err = h.Stop(context.Background(), q)
			if !errors.Is(err, m0hook.ErrEffectRequired) || len(e.km.Ends()) != 0 || e.only().State != runrecord.Bound {
				t.Fatalf("empty Stop: %v", err)
			}
			if _, stops := e.inner.counts(); stops != 1 {
				t.Fatal("empty Stop was refused before Inner.Stop")
			}
			_, err = h.Stop(context.Background(), stopReq("valid-stop", cuid))
			must(t, err)
			if e.only().State != runrecord.Ended {
				t.Fatal("valid Stop skipped H5")
			}
		})
	}
}

// Claim: a run-less terminal refusal cannot make a unique live run ambiguous; the live run alone is the adjacent control.
func TestRunlessStopCandidate(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprint(ambiguous), func(t *testing.T) {
			e := newEnv(t)
			h := e.build()
			_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			must(t, err)
			if ambiguous {
				e.events.err = errStoreRefused
				_, err = h.Start(context.Background(), startReq("effect-b", cuid), e.gate)
				must(t, err)
			}
			_, err = h.Stop(context.Background(), stopReq("stop", cuid))
			must(t, err)
			if len(e.km.Ends()) != 1 || e.only().State != runrecord.Ended {
				t.Fatal("run-less refusal blocked the unique run's end")
			}
		})
	}
}

// Claim: diagnostic eviction never clears process-life dedup; after 257 distinct terminal effects, a replay cannot bind or call Inner again (the 257th effect binds).
func TestEvictionNeverRebinds(t *testing.T) {
	e := newEnv(t)
	h := e.build()
	_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	must(t, err)
	first, _ := h.Last(effectA)
	e.export[first.Key] = sweep.Exported
	_, err = h.Stop(context.Background(), stopReq("stop-first", cuid))
	must(t, err)
	e.events.err = errStoreRefused
	for i := 0; i < 255; i++ {
		r := startReq(fmt.Sprintf("refusal-%d", i), cuid)
		r.ExecutionID = fmt.Sprintf("exec-%d", i+10)
		_, err = h.Start(context.Background(), r, e.gate)
		must(t, err)
	}
	e.events.err = nil
	r := startReq("distinct-last", cuid)
	r.ExecutionID = "exec-last"
	receipt, err := h.Start(context.Background(), r, e.gate)
	must(t, err)
	if !receipt.Committed {
		t.Fatal("257th distinct effect did not bind")
	}
	q := stopReq("stop-last", cuid)
	q.ExecutionID = r.ExecutionID
	_, err = h.Stop(context.Background(), q)
	must(t, err)
	if _, ok := h.Last(effectA); ok {
		t.Fatal("terminal diagnostic was not evicted")
	}
	binds, events := len(e.km.Binds()), e.events.count()
	starts, _ := e.inner.counts()
	_, err = h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	if !errors.Is(err, m0hook.ErrEffectKnown) {
		t.Fatalf("evicted effect replay: %v", err)
	}
	now, _ := e.inner.counts()
	if len(e.km.Binds()) != binds || now != starts || e.events.count() != events {
		t.Fatal("evicted effect began again")
	}
}
