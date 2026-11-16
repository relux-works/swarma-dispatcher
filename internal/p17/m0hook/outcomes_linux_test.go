//go:build linux && (amd64 || arm64)

package m0hook_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/m0hook"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

type beginStore struct {
	*gateStore
	delay     time.Duration
	createErr error
	fenced    bool
}

func (s *beginStore) Create(r *runrecord.Record) error {
	if s.createErr != nil {
		return s.createErr
	}
	err := s.gateStore.Create(r)
	if err == nil {
		time.Sleep(s.delay)
	}
	return err
}
func (s *beginStore) Fenced(key string) bool { return s.fenced || s.gateStore.Fenced(key) }
func beginGate(e *env) *beginStore {
	s := &beginStore{gateStore: gated(e)}
	e.records = s
	return s
}

// Claim: certain terminal Begin failures retain the actual key and retire; attempted terminal states under uncertainty remain HELD, with fatal duty first (fast bind beside them).
func TestTerminalBeginOutcomes(t *testing.T) {
	for _, name := range []string{"fast", "unsent", "failed", "sync", "refused", "refused-uncertain"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			s := beginGate(e)
			unsent := name == "unsent" || name == "failed" || name == "sync"
			uncertain := name == "failed" || name == "sync" || name == "refused-uncertain"
			if unsent {
				s.delay = 1550 * time.Millisecond
			}
			if name == "refused" || name == "refused-uncertain" {
				e.km.BindMode = "refuse"
			}
			cause := errStoreRefused
			if name == "sync" {
				cause = errors.Join(cause, journal.ErrSyncFailure)
			}
			if uncertain {
				s.set(func(r *runrecord.Record) error {
					if r.State == runrecord.Unsent || r.State == runrecord.Refused {
						return cause
					}
					return nil
				})
			}
			h := e.build()
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			a, ok := h.Last(effectA)
			r := e.only()
			if !ok || a.Key != r.Key || a.Key == r.BindRequestID {
				t.Fatalf("key identity: attempt %+v record key=%s bind id=%s", a, r.Key, r.BindRequestID)
			}
			if name == "fast" {
				must(t, err)
				if !receipt.Committed || r.State != runrecord.Bound {
					t.Fatalf("fast control: %+v %s", receipt, r.State)
				}
				_, err = h.Stop(context.Background(), stopReq("stop-fast", cuid))
				must(t, err)
				return
			}
			if receipt.Committed || a.Ended || e.gate.count() != 0 {
				t.Fatalf("premature child: %+v attempt %+v", receipt, a)
			}
			if starts, _ := e.inner.counts(); starts != 0 {
				t.Fatal("terminal failure reached Inner.Start")
			}
			if unsent && len(e.km.Binds()) != 0 {
				t.Fatal("guard expiry sent a bind")
			}
			if uncertain {
				var record *launcher.BeginRecordError
				if err == nil || !errors.As(err, &record) || !record.RecordUncertain() || !codes.Is(err, codes.BindUnknown) || m0.MustExit(err) != (name == "sync") || !e.quar.Held(cuid) || r.State != runrecord.Binding {
					t.Fatalf("uncertainty laundered: err=%v attempt %+v state=%s", err, a, r.State)
				}
				attempted := runrecord.Unsent
				if name == "refused-uncertain" {
					attempted = runrecord.Refused
				}
				if record.RecordState() != attempted {
					t.Fatalf("attempted state %s", record.RecordState())
				}
			} else {
				must(t, err)
				want := runrecord.Unsent
				code := codes.BindUnavailable
				if name == "refused" {
					want = runrecord.Refused
					code = codes.BindRefused
				}
				if r.State != want || a.Code != code || e.quar.Held(cuid) {
					t.Fatalf("certain terminal: attempt %+v state=%s held=%v", a, r.State, e.quar.Held(cuid))
				}
			}
			_, stopErr := h.Stop(context.Background(), stopReq("stop-terminal", cuid))
			if m0.MustExit(stopErr) != (name == "sync") || len(e.km.Ends()) != 0 || e.only().State != r.State {
				t.Fatalf("terminal Stop: %v ends=%d", stopErr, len(e.km.Ends()))
			}
			if after, _ := h.Last(effectA); after.Ended || after.End != nil {
				t.Fatal("Stop manufactured H5 for a bind-less outcome")
			}
			_, err = h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			if name == "sync" {
				if !m0.MustExit(err) {
					t.Fatal("fatal latch lost")
				}
			} else if !errors.Is(err, m0hook.ErrEffectKnown) {
				t.Fatalf("duplicate %v", err)
			}
			if name == "unsent" {
				// A new effect starts a fresh H1, with a distinct prepared id and record.
				oldPrep := r.PreparedID
				s.delay = 0
				req := startReq("fresh-effect", cuid)
				req.ExecutionID = "exec-fresh"
				next, err := h.Start(context.Background(), req, e.gate)
				must(t, err)
				b, _ := h.Last("fresh-effect")
				fresh := e.record(b.Key)
				if !next.Committed || fresh.PreparedID == oldPrep || len(e.km.Binds()) != 1 {
					t.Fatalf("fresh H1 reused state: %+v old=%s new=%s", next, oldPrep, fresh.PreparedID)
				}
				stop := stopReq("fresh-stop", cuid)
				stop.ExecutionID = req.ExecutionID
				_, err = h.Stop(context.Background(), stop)
				must(t, err)
				e.events.err = errStoreRefused
				for i := 0; i < 256; i++ {
					q := startReq(fmt.Sprintf("later-%d", i), cuid)
					q.ExecutionID = fmt.Sprintf("execution-%d", i)
					_, err = h.Start(context.Background(), q, e.gate)
					must(t, err)
				}
				if _, ok := h.Last(effectA); ok {
					t.Fatal("certain unsent was not retired as a terminal diagnostic")
				}
			}
		})
	}
}

type encodeFailureWire struct{ fakes.KeymakerWire }

func (encodeFailureWire) Encode(r keymaker.Request) ([]byte, error) {
	if r.Method == keymaker.MethodBind {
		return nil, errStoreRefused
	}
	return (fakes.KeymakerWire{}).Encode(r)
}

type pinFailure struct{ calls int }

func (d *pinFailure) Dial(context.Context) (net.Conn, error) {
	d.calls++
	return nil, codes.Refuse(codes.BindRefused, "fixture endpoint check", keymaker.ErrEndpointPin).With("check", "mode pin")
}

// Claim: clean Create, encoding, endpoint-pin and transport failures keep their distinct typed no-start codes without records or retries; a bound start is beside them.
func TestBeginRefusalMappings(t *testing.T) {
	for _, name := range []string{"bound", "create", "encode", "pin", "transport", "nil"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			s := beginGate(e)
			want := codes.BindUnavailable
			pin := &pinFailure{}
			switch name {
			case "create":
				s.createErr = errStoreRefused
			case "encode":
				e.deps.KeymakerWire = encodeFailureWire{}
				want = codes.OriginRefused
			case "pin":
				e.deps.Keymaker = pin
				want = codes.BindRefused
			case "transport":
				e.kmDialer.Fail = errStoreRefused
			case "nil":
				e.deps.Keymaker = nil
			}
			h := e.build()
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			must(t, err)
			if name == "bound" {
				if !receipt.Committed {
					t.Fatal("bound control refused")
				}
				return
			}
			a, _ := h.Last(effectA)
			if receipt.Committed || a.Key != "" || len(e.store.Keys()) != 0 || len(e.km.Binds()) != 0 || e.gate.count() != 0 || e.quar.Held(cuid) {
				t.Fatalf("not a clean no-start: %+v %+v", receipt, a)
			}
			if name == "nil" {
				if !codes.IsUnpinned(a.Err) {
					t.Fatalf("nil dialer: %v", a.Err)
				}
			} else if a.Code != want {
				t.Fatalf("code %s want %s", a.Code, want)
			}
			if name == "pin" {
				var refusal *codes.Refusal
				if pin.calls != 1 || !errors.As(a.Err, &refusal) || refusal.Detail["check"] != "mode pin" {
					t.Fatalf("pin verdict lost: %+v calls=%d", a, pin.calls)
				}
			}
			dials, _ := e.srcDialer.Dials()
			expected := 1
			if name == "nil" {
				expected = 0
			}
			if dials != expected {
				t.Fatalf("Source contact %d want %d", dials, expected)
			}
		})
	}
}

// Claim: lost End on every clean-refusal path is returned, never a nil-error refusal; acknowledged End is the adjacent control for each path.
func TestCleanRefusalEndFailures(t *testing.T) {
	for _, stage := range []string{"hygiene", "not-committed", "spawn"} {
		for _, lost := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-%v", stage, lost), func(t *testing.T) {
				e := newEnv(t)
				s := beginGate(e)
				switch stage {
				case "hygiene":
					e.iface.err = errStoreRefused
				case "not-committed":
					e.inner.uncommitted = true
				case "spawn":
					s.fenced = true
				}
				if lost {
					e.km.EndMode = "close"
				}
				h := e.build()
				receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
				a, _ := h.Last(effectA)
				if receipt.Committed || (err != nil) != lost || codes.Is(err, codes.EndUnknown) != lost || !a.Ended || a.End == nil || a.End.Acknowledged == lost || a.ChildStatusKnown {
					t.Fatalf("receipt %+v error %v attempt %+v", receipt, err, a)
				}
				want := runrecord.Ended
				if lost {
					want = runrecord.Ending
				}
				if e.only().State != want || len(e.km.Ends()) != 1 {
					t.Fatalf("state %s ends=%d", e.only().State, len(e.km.Ends()))
				}
			})
		}
	}
}

type stopRequestFunc func(m0.StartRequest) (m0.StopRequest, error)

func (f stopRequestFunc) StopRequest(r m0.StartRequest) (m0.StopRequest, error) { return f(r) }

// Claim: emergency stops validate the full request and retain receipt/error evidence; unpinned or malformed requests send nothing, and confirmed absence is the adjacent control.
func TestEmergencyStopIdentity(t *testing.T) {
	for _, name := range []string{"valid", "inner-error", "unconfirmed", "unpinned", "empty", "start-id", "execution", "worker", "event", "detection", "obligations", "source", "duplicate-source", "expiry", "negative-event", "negative-detection", "ceiling"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.reader.err = errStoreRefused
			d := e.hookDeps()
			stopErr := errors.New("fixture inner stop failure")
			if name == "unpinned" {
				d.Stops = nil
			} else {
				d.Stops = stopRequestFunc(func(r m0.StartRequest) (m0.StopRequest, error) {
					q, _ := (stopRequestsFake{}).StopRequest(r)
					switch name {
					case "empty":
						q.EffectID = ""
					case "start-id":
						q.EffectID = r.EffectID
					case "execution":
						q.ExecutionID = "foreign"
					case "worker":
						q.Worker.UID = 30002
					case "event":
						q.Event = 1700000099
					case "detection":
						q.Detection = 1700000102
					case "obligations":
						q.Obligations = nil
					case "source":
						q.Obligations[0].Source = "foreign"
					case "duplicate-source":
						q.Obligations = append(q.Obligations, q.Obligations[0])
					case "expiry":
						q.Obligations[0].Source = "expiry"
					case "negative-event":
						q.Obligations[0].Event = -1
						q.Event = -1
						q.Ceiling = 0
					case "negative-detection":
						q.Obligations[0].Detection = -1
						q.Detection = -1
						q.Ceiling = 0
					case "ceiling":
						q.Ceiling = 1700000200
					}
					return q, nil
				})
			}
			if name == "inner-error" {
				e.inner.stopErr = stopErr
			}
			if name == "unconfirmed" {
				e.inner.stopReceipt = &m0.StopReceipt{Absent: false, Evidence: []byte("not confirmed")}
			}
			h, err := m0hook.New(context.Background(), d)
			must(t, err)
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			a, _ := h.Last(effectA)
			attempted := name == "valid" || name == "inner-error" || name == "unconfirmed"
			if !receipt.Committed || !errors.Is(err, m0hook.ErrUnproved) || a.InnerStopAttempted != attempted || !a.Ended || e.only().State != runrecord.Ended {
				t.Fatalf("receipt %+v error %v attempt %+v", receipt, err, a)
			}
			_, stops := e.inner.counts()
			expected := 0
			if attempted {
				expected = 1
			}
			if stops != expected {
				t.Fatalf("stops=%d want %d", stops, expected)
			}
			switch name {
			case "valid":
				if a.InnerStopErr != nil || e.inner.stops[0].EffectID != "stop-"+effectA || !a.InnerStopReceipt.Absent || string(a.InnerStopReceipt.Evidence) != "synthetic fenced absence" {
					t.Fatalf("valid identity: %+v", a)
				}
			case "unconfirmed":
				if !errors.Is(err, m0hook.ErrStopUnconfirmed) || !errors.Is(a.InnerStopErr, m0hook.ErrStopUnconfirmed) || a.InnerStopReceipt.Absent || string(a.InnerStopReceipt.Evidence) != "not confirmed" {
					t.Fatal("unconfirmed emergency receipt lost")
				}
			case "inner-error":
				if !errors.Is(err, stopErr) || !errors.Is(a.InnerStopErr, stopErr) {
					t.Fatal("inner stop error discarded")
				}
			case "unpinned":
				if !codes.IsUnpinned(err) || !codes.IsUnpinned(a.InnerStopErr) {
					t.Fatal("unpinned stop did not refuse by name")
				}
			default:
				if !errors.Is(err, m0hook.ErrStopIdentity) || !errors.Is(a.InnerStopErr, m0hook.ErrStopIdentity) {
					t.Fatal("malformed stop accepted")
				}
			}
		})
	}
}

type incarnationFunc func() (incarnation.Incarnation, error)

func (f incarnationFunc) Child(m0.StartRequest, m0.StartReceipt) (incarnation.Incarnation, error) {
	return f()
}

// Claim: a valid incarnation under another uid is unproved before child persistence, triggers emergency stop and H5; the matching uid is beside it.
func TestIncarnationUIDGuard(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			e := newEnv(t)
			d := e.hookDeps()
			d.Incarnations = incarnationFunc(func() (incarnation.Incarnation, error) {
				v, err := e.reader.Child(m0.StartRequest{}, m0.StartReceipt{})
				if wrong {
					v.UID++
				}
				return v, err
			})
			h, err := m0hook.New(context.Background(), d)
			must(t, err)
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			if !receipt.Committed || errors.Is(err, m0hook.ErrUnproved) != wrong {
				t.Fatalf("receipt %+v error %v", receipt, err)
			}
			if wrong {
				if e.only().Child != nil || e.only().State != runrecord.Ended {
					t.Fatal("wrong child persisted")
				}
			} else {
				must(t, err)
				if e.only().Child == nil || e.only().State != runrecord.Bound {
					t.Fatal("matching child refused")
				}
			}
		})
	}
}

// Claim: event and child-interface sync errors latch before nil-error mapping, with no subsequent launcher state I/O; ordinary refusals are adjacent controls.
func TestSeamFatalPrecedence(t *testing.T) {
	for _, stage := range []string{"event", "interface"} {
		for _, fatal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-%v", stage, fatal), func(t *testing.T) {
				e := newEnv(t)
				cause := errStoreRefused
				if fatal {
					cause = errors.Join(cause, journal.ErrSyncFailure)
				}
				if stage == "event" {
					e.events.err = cause
				} else {
					e.iface.err = cause
				}
				h := e.build()
				receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
				if receipt.Committed || (err != nil) != fatal || m0.MustExit(err) != fatal || h.ExitRequired() != fatal {
					t.Fatalf("receipt %+v error %v latch=%v", receipt, err, h.ExitRequired())
				}
				a, _ := h.Last(effectA)
				if !errors.Is(a.Err, cause) {
					t.Fatal("seam cause lost")
				}
				ends := 0
				if stage == "interface" && !fatal {
					ends = 1
				}
				if len(e.km.Ends()) != ends {
					t.Fatalf("ends=%d want %d", len(e.km.Ends()), ends)
				}
				srcBefore, _ := e.srcDialer.Dials()
				if stage == "event" && (srcBefore != 0 || e.kmDialer.Dials() != 0) {
					t.Fatal("event refusal contacted a dependency")
				}
				if fatal {
					_, err = h.Start(context.Background(), startReq("later", cuid), e.gate)
					if !m0.MustExit(err) {
						t.Fatal("later Start ignored latch")
					}
					srcAfter, _ := e.srcDialer.Dials()
					if srcAfter != srcBefore {
						t.Fatal("later Start contacted Source")
					}
				}
			})
		}
	}
}

func fixtureLauncher(e *env) *incarnation.Incarnation {
	self, err := e.proc.Incarnations().Self()
	must(e.t, err)
	return &self
}

// Claim: a persisted unsent is never adopted or ended and a fresh effect can bind, while binding stays HELD; both restart outcomes use the existing ledger fact.
func TestRestartUnsent(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			e := newEnv(t)
			r := &runrecord.Record{Version: runrecord.Version, Kind: runrecord.Kind, Key: "old", State: runrecord.Binding, LauncherUID: luid, LauncherGeneration: "gen-a", StationRegistration: "1", InputPrincipal: "input", Origin: fakes.ValidOrigin(), LaunchNonce: "old", SourceIncarnation: "inc-1", PreparedID: "prep-old", BindRequestID: "bind-old", ChildUID: cuid, Launcher: fixtureLauncher(e)}
			must(t, e.store.Create(r))
			must(t, e.quar.Hold(cuid, "old"))
			if terminal {
				_, err := e.store.Update("old", func(r *runrecord.Record) error { r.State = runrecord.Unsent; return nil })
				must(t, err)
			}
			e.reopen()
			h, err := m0hook.New(context.Background(), e.hookDeps())
			if h == nil || (err == nil) != terminal {
				t.Fatalf("restart terminal=%v error %v", terminal, err)
			}
			reports := h.Reports()
			if len(reports) != 1 || reports[0].UIDReleased != terminal || len(e.km.Ends()) != 0 {
				t.Fatalf("reports %+v", reports)
			}
			dials, _ := e.srcDialer.Dials()
			if dials != 0 || e.kmDialer.Dials() != 0 {
				t.Fatal("terminal or held recovery contacted dependency")
			}
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			if terminal {
				must(t, err)
				if !receipt.Committed || reports[0].Action != runrecord.ActionNeverAdopt {
					t.Fatal("unsent restart blocked fresh run")
				}
			} else if !errors.Is(err, m0hook.ErrNotRestarted) || receipt.Committed || reports[0].Action != runrecord.ActionHold {
				t.Fatalf("HELD admitted: %+v %v", receipt, err)
			}
		})
	}
}

// Claim: Installation is an explicit validated dependency, not a directory inference; Fresh and Established callers both have working controls.
func TestInstallationDependency(t *testing.T) {
	for _, fact := range []sweep.Installation{0, 99, sweep.Fresh, sweep.Established} {
		t.Run(fmt.Sprint(fact), func(t *testing.T) {
			e := newEnv(t)
			if fact == sweep.Established {
				// A real record establishes the journal independently of the ledger fact.
				r := &runrecord.Record{Version: runrecord.Version, Kind: runrecord.Kind, Key: "old", State: runrecord.Binding, LauncherUID: luid, LauncherGeneration: "gen-a", StationRegistration: "1", InputPrincipal: "input", Origin: fakes.ValidOrigin(), LaunchNonce: "old", SourceIncarnation: "inc-1", PreparedID: "prep-old", BindRequestID: "bind-old", ChildUID: cuid + 1, Launcher: fixtureLauncher(e)}
				must(t, e.store.Create(r))
				_, err := e.store.Update("old", func(r *runrecord.Record) error { r.State = runrecord.Unsent; return nil })
				must(t, err)
				e.reopen()
			}
			d := e.hookDeps()
			d.Installation = fact
			h, err := m0hook.New(context.Background(), d)
			if fact == 0 || fact == 99 {
				if h != nil || !errors.Is(err, m0hook.ErrInstallationRequired) {
					t.Fatalf("invalid fact accepted: %v", err)
				}
				return
			}
			must(t, err)
			receipt, err := h.Start(context.Background(), startReq(effectA, cuid), e.gate)
			must(t, err)
			if !receipt.Committed {
				t.Fatal("explicit valid fact refused")
			}
		})
	}
}
