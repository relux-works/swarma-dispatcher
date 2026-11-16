//go:build linux && (amd64 || arm64)

package m0hook_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/hygiene"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/m0hook"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
)

type eventFunc func(m0.StartRequest) (runorigin.EventRef, error)

func (f eventFunc) Event(r m0.StartRequest) (runorigin.EventRef, error) { return f(r) }

type interfaceFunc func(m0.StartRequest) (hygiene.Launch, error)

func (f interfaceFunc) Launch(r m0.StartRequest) (hygiene.Launch, error) { return f(r) }

type pausedReleaseStore struct {
	*gateStore
	pause func()
}

func (s *pausedReleaseStore) Update(k string, f func(*runrecord.Record) error) (*runrecord.Record, error) {
	r, err := s.gateStore.Update(k, f)
	if err == nil && r.InputReleased {
		s.pause()
	}
	return r, err
}

type pausedChildStore struct {
	*gateStore
	pause func()
}

func (s *pausedChildStore) Update(k string, f func(*runrecord.Record) error) (*runrecord.Record, error) {
	r, err := s.gateStore.Update(k, f)
	if err == nil && r.Child != nil && !r.InputReleased {
		s.pause()
	}
	return r, err
}

type pausedBoundStore struct {
	*gateStore
	pause func()
}

func (s pausedBoundStore) Update(k string, f func(*runrecord.Record) error) (*runrecord.Record, error) {
	r, err := s.gateStore.Update(k, f)
	if err == nil && r.State == runrecord.Bound && r.Child == nil {
		s.pause()
	}
	return r, err
}

// Claim: a Stop-latched exit duty during Begin forbids spawn and commitment, retains the bound run and performs no later state I/O; an unlatching Stop permits the same start.
func TestInFlightBeginLatch(t *testing.T) {
	for _, latched := range []bool{false, true} {
		t.Run(fmt.Sprint(latched), func(t *testing.T) {
			e := newEnv(t)
			entered, release := make(chan struct{}), make(chan struct{})
			g := gated(e)
			e.records = &pausedBoundStore{g, func() { close(entered); <-release }}
			d := e.hookDeps()
			interfaceCalls := 0
			d.Hygiene = interfaceFunc(func(m0.StartRequest) (hygiene.Launch, error) {
				interfaceCalls++
				return e.iface.launch, nil
			})
			h, err := m0hook.New(context.Background(), d)
			must(t, err)
			type result struct {
				r   m0.StartReceipt
				err error
			}
			done := make(chan result, 1)
			go func() {
				r, err := h.Start(context.Background(), startReq("effect", 30001), e.gate)
				done <- result{r, err}
			}()
			<-entered
			if latched {
				e.inner.stopErr = journal.ErrSyncFailure
			}
			q := stopReq("other-stop", 30001)
			q.ExecutionID = "other-execution"
			_, err = h.Stop(context.Background(), q)
			if m0.MustExit(err) != latched {
				t.Fatalf("Stop latch: %v", err)
			}
			close(release)
			out := <-done
			starts, _ := e.inner.counts()
			if latched {
				if out.r.Committed || !m0.MustExit(out.err) || !errors.Is(out.err, m0hook.ErrMustExit) || starts != 0 {
					t.Fatalf("latched Begin spawned or committed: receipt=%+v err=%v starts=%d", out.r, out.err, starts)
				}
				if interfaceCalls != 0 {
					t.Fatal("latched Begin reached interface")
				}
				a, _ := h.Last("effect")
				if a.Ended || a.Key == "" || e.only().State != runrecord.Bound || e.only().Child != nil || e.only().InputReleased || len(e.km.Ends()) != 0 || !e.quar.Held(30001) {
					t.Fatalf("latched Begin did state I/O: %+v", a)
				}
			} else if out.err != nil || !out.r.Committed || starts != 1 || interfaceCalls != 1 {
				t.Fatalf("unlatched control: %+v %v starts=%d", out.r, out.err, starts)
			}
		})
	}
}

// Claim: held bind and lost End refusals survive the second and third Stop with the inner receipt only as evidence; acknowledged End returns nil every time.
func TestRetainedStops(t *testing.T) {
	for _, mode := range []string{"ack", "bind", "end"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			if mode == "bind" {
				e.km.BindMode = "close"
			}
			if mode == "end" {
				e.km.EndMode = "close"
			}
			h := e.build()
			_, err := h.Start(context.Background(), startReq("effect", 30001), e.gate)
			if mode == "bind" {
				if !codes.Is(err, codes.BindUnknown) {
					t.Fatalf("bind refusal: %v", err)
				}
			} else {
				must(t, err)
			}
			for i := 1; i <= 3; i++ {
				if i == 2 && mode != "ack" {
					e.inner.stopErr = codes.Refuse(codes.OriginRefused, "fixture inner refusal", nil)
				}
				if i == 3 && mode != "ack" {
					e.inner.stopErr = journal.ErrSyncFailure
				}
				r, err := h.Stop(context.Background(), stopReq("stop", 30001))
				if !r.Absent || string(r.Evidence) != "synthetic fenced absence" {
					t.Fatalf("Stop %d receipt: %+v", i, r)
				}
				if mode == "ack" {
					must(t, err)
				} else {
					want := codes.EndUnknown
					if mode == "bind" {
						want = codes.BindUnknown
					}
					if !codes.Is(err, want) {
						t.Fatalf("Stop %d lost retained refusal: %v want %s", i, err, want)
					}
					if m0.MustExit(err) != (i == 3) || errors.Is(err, m0hook.ErrMustExit) != (i == 3) {
						t.Fatalf("Stop %d lost fatal evidence: %v", i, err)
					}
					if cl, ok := want.Class(); !ok || cl != codes.Unknown {
						t.Fatal("retained refusal must be unknown")
					}
				}
			}
			ends := 1
			state := runrecord.Ended
			if mode == "bind" {
				ends = 0
				state = runrecord.Binding
			}
			if mode == "end" {
				state = runrecord.Ending
			}
			if len(e.km.Ends()) != ends || e.only().State != state {
				t.Fatalf("repeated Stop retried state: ends=%d state=%s", len(e.km.Ends()), e.only().State)
			}
		})
	}
}

// Claim: a concurrent Stop latch between event, interface or incarnation seams prevents the next effect step; each step has an unlatching control.
func TestInFlightStepLatch(t *testing.T) {
	for _, step := range []string{"event-boundary", "event", "interface", "inner-boundary", "incarnation-boundary", "incarnation", "child-record", "release-record"} {
		for _, latched := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-%v", step, latched), func(t *testing.T) {
				e := newEnv(t)
				entered, release := make(chan struct{}), make(chan struct{})
				pause := func() { close(entered); <-release }
				if step == "child-record" {
					e.records = &pausedChildStore{gated(e), pause}
				}
				if step == "release-record" {
					e.records = &pausedReleaseStore{gated(e), pause}
				}
				d := e.hookDeps()
				readCalls := 0
				d.Incarnations = incarnationFunc(func() (incarnation.Incarnation, error) {
					readCalls++
					return e.reader.Child(m0.StartRequest{}, m0.StartReceipt{})
				})
				switch step {
				case "event":
					d.Events = eventFunc(func(m0.StartRequest) (runorigin.EventRef, error) { pause(); return fixtureEvent, nil })
				case "interface":
					d.Hygiene = interfaceFunc(func(m0.StartRequest) (hygiene.Launch, error) { pause(); return e.iface.launch, nil })
				case "incarnation":
					d.Incarnations = incarnationFunc(func() (incarnation.Incarnation, error) {
						readCalls++
						inc, err := e.reader.Child(m0.StartRequest{}, m0.StartReceipt{})
						pause()
						return inc, err
					})
				}
				h, err := m0hook.New(context.Background(), d)
				must(t, err)
				m0hook.BeforeStep(h, func(s string) {
					if step == "event-boundary" && s == "event" || step == "inner-boundary" && s == "inner" || step == "incarnation-boundary" && s == "incarnation" {
						pause()
					}
				})
				done := make(chan error, 1)
				go func() { _, err := h.Start(context.Background(), startReq("effect", 30001), e.gate); done <- err }()
				<-entered
				if latched {
					e.inner.stopErr = journal.ErrSyncFailure
				}
				q := stopReq("other-stop", 30001)
				q.ExecutionID = "other"
				_, err = h.Stop(context.Background(), q)
				if m0.MustExit(err) != latched {
					t.Fatalf("Stop latch: %v", err)
				}
				close(release)
				err = <-done
				if m0.MustExit(err) != latched || errors.Is(err, m0hook.ErrMustExit) != latched {
					t.Fatalf("step %s latch=%v err=%v", step, latched, err)
				}
				if latched {
					starts, _ := e.inner.counts()
					if step == "event" || step == "event-boundary" {
						if e.kmDialer.Dials() != 0 || len(e.store.Keys()) != 0 || step == "event-boundary" && e.events.count() != 0 {
							t.Fatal("latched event reached Begin")
						}
					} else {
						if len(e.km.Ends()) != 0 || e.only().State != runrecord.Bound || (e.only().InputReleased && step != "release-record") {
							t.Fatal("latched step performed later state I/O")
						}
						if step == "interface" && e.quar.HelperInvoked(30001) {
							t.Fatal("latched interface marked helper")
						}
						if (step == "interface" || step == "inner-boundary") && starts != 0 {
							t.Fatal("latched interface reached spawn")
						}
						if step == "incarnation-boundary" && readCalls != 0 {
							t.Fatal("latched inner start reached incarnation seam")
						}
						if step == "incarnation" && e.only().Child != nil {
							t.Fatal("latched incarnation persisted child")
						}
					}
				} else {
					must(t, err)
					if !e.only().InputReleased {
						t.Fatal("control did not release")
					}
				}
			})
		}
	}
}
