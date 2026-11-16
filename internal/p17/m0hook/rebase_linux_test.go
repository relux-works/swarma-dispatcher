//go:build linux && (amd64 || arm64)

package m0hook_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/m0hook"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
)

// Claim: Stop matches the original execution and worker, not its distinct effect ID; foreign or missing identities leave H5 untouched (matching control beside them).
func TestStopMatchesOriginalExecution(t *testing.T) {
	e := newEnv(t)
	h := e.build()
	_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	must(t, err)
	for _, name := range []string{"execution", "uid", "generation", "missing-execution"} {
		t.Run(name, func(t *testing.T) {
			req := stopReq(effectA, cuid)
			switch name {
			case "execution":
				req.ExecutionID = "another-execution"
			case "uid":
				req.Worker.UID++
			case "generation":
				req.Worker.Generation = "2"
			case "missing-execution":
				req.ExecutionID = ""
			}
			_, err := h.Stop(context.Background(), req)
			must(t, err)
			if e.only().State != runrecord.Bound || len(e.km.Ends()) != 0 {
				t.Fatal("a foreign or missing identity ended the original run")
			}
		})
	}
	// M0's stop effect ID is different from its start effect ID.
	req := stopReq("stop-a", cuid)
	receipt, err := h.Stop(context.Background(), req)
	must(t, err)
	if !receipt.Absent || e.only().State != runrecord.Ended || len(e.km.Ends()) != 1 {
		t.Fatalf("matching stop: receipt %+v record %+v ends=%d", receipt, e.only(), len(e.km.Ends()))
	}
	if got := e.inner.stops[len(e.inner.stops)-1]; got.EffectID != req.EffectID || got.ExecutionID != req.ExecutionID {
		t.Fatalf("inner stop identity changed: %+v", got)
	}
}

// helloErrorWire injects a cause before any bind or record, retaining the real test codec otherwise.
type helloErrorWire struct {
	fakes.KeymakerWire
	cause error
}

func (w helloErrorWire) Hello(net.Conn, time.Duration) error { return w.cause }

// Claim: MustExit takes precedence over a retryable Begin code, while the ordinary hello failure remains a nil-error refusal (adjacent control).
func TestFatalPrecedesPreBoundClassification(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		name, cause := "ordinary", errStoreRefused
		if fatal {
			name, cause = "sync", errors.Join(cause, journal.ErrSyncFailure)
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.deps.KeymakerWire = helloErrorWire{cause: cause}
			h := e.build()
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			if receipt.Committed || (err != nil) != fatal || m0.MustExit(err) != fatal || h.ExitRequired() != fatal {
				t.Fatalf("receipt %+v error %v exit=%v", receipt, err, h.ExitRequired())
			}
			if code, ok := h.Code(effectA); !ok || code != codes.BindUnavailable {
				t.Fatalf("code %q ok=%v", code, ok)
			}
			if starts, _ := e.inner.counts(); starts != 0 || e.gate.count() != 0 || len(e.store.Keys()) != 0 || len(e.km.Binds()) != 0 {
				t.Fatal("pre-bound failure reached the child or created a record")
			}
		})
	}
}

// Claim: EndOutcome.MustExit is surfaced even when End's decisive error has no sync sentinel; ordinary record errors are the adjacent control.
func TestEndOutcomeFlagReachesFatalClass(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		name, cause := "ordinary", errStoreRefused
		if fatal {
			name, cause = "sync", errors.Join(cause, journal.ErrSyncFailure)
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			h := e.build()
			_, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			must(t, err)
			g.set(func(next *runrecord.Record) error {
				if next.State == runrecord.Ending {
					return errStoreRefused
				}
				if next.TeardownDone {
					return cause
				}
				return nil
			})
			receipt, err := h.Stop(context.Background(), stopReq(effectA, cuid))
			a, _ := h.Last(effectA)
			if !receipt.Absent || err == nil || m0.MustExit(err) != fatal || h.ExitRequired() != fatal || errors.Is(err, m0hook.ErrMustExit) != fatal {
				t.Fatalf("receipt %+v error %v exit=%v", receipt, err, h.ExitRequired())
			}
			if a.End == nil || a.End.MustExit != fatal || m0.MustExit(a.EndErr) || !errors.Is(a.End.RecordErr, cause) || len(e.km.Ends()) != 0 {
				t.Fatalf("outcome %+v decisive error %v ends=%d", a.End, a.EndErr, len(e.km.Ends()))
			}
		})
	}
}

// Claim: H3 persistence errors stop the inner child; ordinary errors end once, while fatal errors latch without further launcher state I/O (adjacent controls).
func TestH3PersistenceFailures(t *testing.T) {
	for _, stage := range []string{"child", "release"} {
		for _, fatal := range []bool{false, true} {
			name, cause := "ordinary", errStoreRefused
			if fatal {
				name, cause = "sync", errors.Join(cause, journal.ErrSyncFailure)
			}
			t.Run(stage+"-"+name, func(t *testing.T) {
				e := newEnv(t)
				g := gated(e)
				g.set(func(next *runrecord.Record) error {
					if next.State == runrecord.Bound && ((stage == "child" && next.Child != nil) || (stage == "release" && next.InputReleased)) {
						return cause
					}
					return nil
				})
				h := e.build()
				receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
				if !receipt.Committed || err == nil || !errors.Is(err, cause) || m0.MustExit(err) != fatal || h.ExitRequired() != fatal {
					t.Fatalf("receipt %+v error %v exit=%v", receipt, err, h.ExitRequired())
				}
				first, _ := h.Last(effectA)
				wantState, ends := runrecord.Ended, 1
				if fatal {
					wantState, ends = runrecord.Bound, 0
				}
				if starts, stops := e.inner.counts(); starts != 1 || stops != 1 || first.Ended == fatal || len(e.km.Ends()) != ends || e.only().State != wantState {
					t.Fatalf("starts=%d stops=%d attempt %+v state=%s", starts, stops, first, e.only().State)
				}
				_, err = h.Stop(context.Background(), stopReq("stop-a", cuid))
				if m0.MustExit(err) != fatal || (err != nil) != fatal {
					t.Fatalf("later Stop: %v", err)
				}
				second, _ := h.Last(effectA)
				if second.End != first.End || len(e.km.Ends()) != ends {
					t.Fatal("a failed start was ended a second time")
				}
			})
		}
	}
}

// Claim: native HELD details survive the decorator unchanged and release no input; control: TestBoundRunStartsOnceAndStopsEnded.
func TestHeldNativeDetailsRetained(t *testing.T) {
	e := newEnv(t)
	e.km.BindMode = "busy"
	h := e.build()
	receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
	var refusal *codes.Refusal
	if receipt.Committed || !codes.Is(err, codes.BindUnknown) || !errors.As(err, &refusal) || refusal.Detail["native_code"] != "busy" || refusal.Detail["native_retryable"] != "true" {
		t.Fatalf("receipt %+v error %v", receipt, err)
	}
	a, _ := h.Last(effectA)
	if a.Err != err || e.only().State != runrecord.Binding || e.gate.count() != 0 {
		t.Fatalf("attempt %+v record %+v commits=%d", a, e.only(), e.gate.count())
	}
}
