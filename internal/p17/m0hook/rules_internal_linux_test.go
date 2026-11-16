//go:build linux && (amd64 || arm64)

package m0hook

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
)

type recordEvidence struct {
	state     runrecord.State
	uncertain bool
}

func (r recordEvidence) RecordState() runrecord.State { return r.state }
func (r recordEvidence) RecordUncertain() bool        { return r.uncertain }

// Claim: removing only uncertainty-first or exit-duty-first makes a matching terminal state/code unsafe; certain matching outcomes are the adjacent controls.
func TestBeginMappingPriority(t *testing.T) {
	for _, state := range []runrecord.State{runrecord.Unsent, runrecord.Refused} {
		for _, mode := range []string{"certain", "uncertain", "fatal"} {
			t.Run(fmt.Sprintf("%s-%s", state, mode), func(t *testing.T) {
				code := codes.BindUnavailable
				if state == runrecord.Refused {
					code = codes.BindRefused
				}
				var cause error = errors.New("fixture terminal cause")
				if mode == "fatal" {
					cause = errors.Join(cause, journal.ErrSyncFailure)
				}
				err := codes.Refuse(code, "fixture terminal outcome", cause)
				h := &Hooked{}
				e := &effect{a: Attempt{Err: err}}
				got := h.beginFailure(e, err, recordEvidence{state, mode != "certain"})
				if mode == "certain" {
					if got != nil || e.held {
						t.Fatalf("certain control held: %v", got)
					}
					return
				}
				if mode == "fatal" {
					if !m0.MustExit(got) || !errors.Is(got, ErrMustExit) || !h.ExitRequired() || !e.held {
						t.Fatalf("exit duty lost before record mapping: %v", got)
					}
				} else if !codes.Is(got, codes.BindUnknown) || !e.held || !errors.Is(got, cause) {
					t.Fatalf("uncertainty lost before terminal mapping: %v", got)
				}
			})
		}
	}
}

// Claim: terminal state without its proper code and terminal code without its state are HELD unknown, while matching pairs alone are no-starts.
func TestBeginStateCodeAgreement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state runrecord.State
		code  codes.Code
		held  bool
	}{
		{"unsent", runrecord.Unsent, codes.BindUnavailable, false},
		{"refused", runrecord.Refused, codes.BindRefused, false},
		{"unsent-wrong-code", runrecord.Unsent, codes.BindRefused, true},
		{"refused-wrong-code", runrecord.Refused, codes.BindUnavailable, true},
		{"unavailable-without-unsent", runrecord.Binding, codes.BindUnavailable, true},
		{"refused-without-refused", runrecord.Bound, codes.BindRefused, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Hooked{}
			e := &effect{}
			err := h.beginFailure(e, codes.Refuse(tc.code, "fixture outcome", nil), recordEvidence{tc.state, false})
			if e.held != tc.held || (err != nil) != tc.held || codes.Is(err, codes.BindUnknown) != tc.held {
				t.Fatalf("state/code disagreement laundered: held=%v err=%v", e.held, err)
			}
		})
	}
}

type callbackInner struct {
	stop  func(context.Context, m0.StopRequest) (m0.StopReceipt, error)
	start func(context.Context, m0.StartRequest, m0.CommitGate) (m0.StartReceipt, error)
}

func (s callbackInner) Start(c context.Context, r m0.StartRequest, g m0.CommitGate) (m0.StartReceipt, error) {
	return s.start(c, r, g)
}
func (s callbackInner) Stop(c context.Context, r m0.StopRequest) (m0.StopReceipt, error) {
	return s.stop(c, r)
}
func (s callbackInner) Observe(context.Context, m0.EffectQuery) (m0.Observation, error) {
	return m0.ObservationPresent, nil
}

// Claim: known and ambiguous candidates are all locked before Inner.Stop; only run-owning candidates count, and a unique held candidate retains its exact refusal.
func TestAmbiguousStop(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprint(ambiguous), func(t *testing.T) {
			h := &Hooked{l: &launcher.Launcher{}, restarted: true, effects: make(map[string]*effect)}
			held := codes.Refuse(codes.BindUnknown, "fixture held bind", nil)
			a := &effect{id: "a", execution: "execution", done: true, held: true, a: Attempt{Err: held}}
			b := &effect{id: "b", execution: "execution", done: true, held: ambiguous, a: Attempt{Err: held}}
			h.effects["a"], h.effects["b"] = a, b
			h.inner = callbackInner{stop: func(context.Context, m0.StopRequest) (m0.StopReceipt, error) {
				for _, e := range []*effect{a, b} {
					if e.mu.TryLock() {
						t.Error("Inner.Stop reached an unlocked candidate")
						e.mu.Unlock()
						e.mu.Lock()
					}
				}
				return m0.StopReceipt{Absent: true, Evidence: []byte("absence")}, nil
			}}
			r, err := h.Stop(context.Background(), m0.StopRequest{EffectID: "stop", ExecutionID: "execution"})
			if !r.Absent || string(r.Evidence) != "absence" || errors.Is(err, ErrStopAmbiguous) != ambiguous {
				t.Fatalf("ambiguity result: %+v %v", r, err)
			}
			if ambiguous {
				if !errors.Is(a.a.Err, ErrStopAmbiguous) || !errors.Is(b.a.Err, ErrStopAmbiguous) {
					t.Fatal("ambiguity evidence missing")
				}
			} else if !errors.Is(err, held) {
				t.Fatalf("unique held refusal lost: %v", err)
			}
		})
	}
}

// Claim: same-run context-preserving Start, Stop and Observe callbacks refuse ErrReentrant instead of waiting for their own lock; foreign observations pass through.
func TestReentrantCalls(t *testing.T) {
	h := &Hooked{l: &launcher.Launcher{}, restarted: true, effects: make(map[string]*effect)}
	e := &effect{id: "effect", execution: "execution", done: true, held: true, a: Attempt{Err: codes.Refuse(codes.BindUnknown, "held", nil)}}
	h.effects[e.id] = e
	h.inner = callbackInner{stop: func(ctx context.Context, _ m0.StopRequest) (m0.StopReceipt, error) {
		_, a := h.Start(ctx, m0.StartRequest{EffectID: "effect", ExecutionID: "execution"}, nil)
		_, b := h.Stop(ctx, m0.StopRequest{EffectID: "stop-again", ExecutionID: "execution"})
		_, c := h.Observe(ctx, m0.EffectQuery{EffectID: "effect"})
		for _, err := range []error{a, b, c} {
			if !errors.Is(err, ErrReentrant) {
				t.Fatalf("reentrant call did not refuse: %v", err)
			}
		}
		observation, err := h.Observe(ctx, m0.EffectQuery{EffectID: "foreign"})
		if err != nil || observation != m0.ObservationPresent {
			t.Fatalf("foreign control: %s %v", observation, err)
		}
		return m0.StopReceipt{Absent: true}, nil
	}}
	_, err := h.Stop(context.Background(), m0.StopRequest{EffectID: "stop", ExecutionID: "execution"})
	if !codes.Is(err, codes.BindUnknown) {
		t.Fatalf("outer stop: %v", err)
	}
}

// Claim: the context marker recognizes same-run identities and rejects foreign runs or wiring instances, without relying on a deadlock timeout.
func TestReentrantGuard(t *testing.T) {
	h := &Hooked{}
	e := &effect{id: "effect", execution: "execution", worker: m0.Worker{UID: 30001, Generation: "1"}}
	ctx := context.WithValue(context.Background(), callKey{}, activeCall{h, []*effect{e}})
	if !h.reentrant(ctx, "effect", "", m0.Worker{}) || !h.reentrant(ctx, "stop", "execution", m0.Worker{UID: 30001, Generation: "1"}) {
		t.Fatal("same-run context not recognized")
	}
	if h.reentrant(ctx, "foreign", "foreign", m0.Worker{UID: 30001, Generation: "1"}) || (&Hooked{}).reentrant(ctx, "effect", "", m0.Worker{}) {
		t.Fatal("foreign context recognized as reentrant")
	}
}
