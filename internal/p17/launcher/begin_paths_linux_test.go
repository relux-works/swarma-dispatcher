//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// noTrace asserts the post-failure state of a Begin that must leave nothing
// behind: no record, no bind frame, and no helper (sweep) invocation.
func noTrace(t *testing.T, e *env) {
	t.Helper()
	if len(e.store.Keys()) != 0 || len(e.km.Binds()) != 0 || len(e.killer.UIDs()) != 0 {
		t.Fatalf("records=%v binds=%d kills=%v", e.store.Keys(), len(e.km.Binds()), e.killer.UIDs())
	}
}

// Claim (pre-record lease return): with isolation.Fixed, a
// transient Source failure (prepare dependency error; validate lost on every
// restart) and a transient Keymaker connect failure each fail retryable
// (exit 1) with no record, and each returns the lease after the zero-process
// proof: the uid is not held, and the next Begin with the SAME fixed uid
// binds.
func TestLeaseReturned(t *testing.T) {
	for name, c := range map[string]struct {
		fail, heal func(*env)
		want       codes.Code
	}{
		"prepare":  {func(e *env) { e.src.PrepareMode = "dependency" }, func(e *env) { e.src.SetPrepareMode("") }, codes.OriginUnavailable},
		"validate": {func(e *env) { e.src.ValidateMode = "close" }, func(e *env) { e.src.SetValidateMode("") }, codes.OriginUnavailable},
		"connect":  {func(e *env) { e.kmDialer.Fail = errors.New("synthetic connect failure") }, func(e *env) { e.kmDialer.Fail = nil }, codes.BindUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			c.fail(e)
			l := e.build()
			_, err := l.Begin(context.Background(), "input-1")
			v, x := codes.Exit(err, 0, false)
			if !codes.Is(err, c.want) || x != nil || v.Status != 1 || v.Detail["lease"] != "" {
				t.Fatalf("%v %+v %v", err, v, x)
			}
			noTrace(t, e)
			if e.quar.Held(cuid) {
				t.Fatal("the pre-record lease was not returned")
			}
			c.heal(e)
			run, err := l.Begin(context.Background(), "input-2")
			must(t, err)
			if run.ChildUID != cuid || e.record(run.Key).State != runrecord.Bound || e.record(run.Key).ChildUID != cuid || !e.quar.Held(cuid) {
				t.Fatalf("retry with the same fixed uid: %+v", e.record(run.Key))
			}
		})
	}
}

// Claim (D5a, the zero-process proof is never skipped): on a
// pre-record failure with a process planted under the leased uid in the
// fixture proc tree (saved uid only), or with an unqualified procfs view
// (hidepid=2), the uid is NOT returned: the Begin error keeps its retryable
// code and exit 1 and reports the quarantine in its detail, no helper is
// invoked, and the next Begin with isolation.Fixed refuses
// run_isolation_unqualified before any further Source contact. Positive
// control: TestLeaseReturned, the same
// failures with a clean qualified tree.
func TestLeaseQuarantined(t *testing.T) {
	for name, c := range map[string]struct {
		plant  func(*testing.T, *env)
		reason string
	}{
		"process": {func(t *testing.T, e *env) { must(t, e.proc.Add(555, [4]uint32{0, 0, cuid, 0}, 9)) }, "process found under the uid"},
		"view":    {func(t *testing.T, e *env) { must(t, e.proc.SetMount("proc", "/", "rw,hidepid=2")) }, "enumeration failed or procfs view not qualified"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			c.plant(t, e)
			e.src.PrepareMode = "dependency"
			l := e.build()
			_, err := l.Begin(context.Background(), "input-1")
			v, x := codes.Exit(err, 0, false)
			if !codes.Is(err, codes.OriginUnavailable) || x != nil || v.Status != 1 {
				t.Fatalf("the original failure code changed: %v %+v %v", err, v, x)
			}
			if v.Detail["lease"] != "quarantined" || v.Detail["lease_uid"] != "2001" || v.Detail["lease_reason"] != c.reason {
				t.Fatalf("quarantine not reported in the detail: %+v", v.Detail)
			}
			noTrace(t, e)
			if !e.quar.Held(cuid) {
				t.Fatal("uid returned without the zero-process proof")
			}
			e.src.SetPrepareMode("")
			dialsBefore, _ := e.srcDialer.Dials()
			_, err = l.Begin(context.Background(), "input-2")
			if !codes.Is(err, codes.IsolationUnqualified) || !errors.Is(err, sweep.ErrQuarantined) {
				t.Fatalf("next Begin with the fixed uid: %v", err)
			}
			if v, x := codes.Exit(err, 0, false); x != nil || v.Status != 16 {
				t.Fatalf("exit %+v %v", v, x)
			}
			if d, _ := e.srcDialer.Dials(); d != dialsBefore || e.kmDialer.Dials() != 0 {
				t.Fatal("contact after the quarantine refusal")
			}
		})
	}
}

// Claim (pre-record lease return needs BOTH conditions; the control beside
// the planted-process negative): a lease for which the privilege-drop helper
// WAS invoked (sweep mode, while this Begin held the lease) is NOT returned
// on a pre-record failure although the D5a enumeration shows ZERO processes:
// the Begin error keeps its retryable code and names the helper in its
// detail, the uid is quarantined, and the next Begin with the same
// isolation.Fixed uid refuses run_isolation_unqualified. Positive control
// ("early"): the same helper invocation BEFORE the lease was taken does not
// count against it; helper never invoked for this lease and zero processes,
// the uid returns and the next Begin binds under it.
func TestLeaseQuarantinedAfterHelper(t *testing.T) {
	for _, during := range []bool{true, false} {
		name := "early"
		if during {
			name = "during"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.src.PrepareMode = "dependency"
			calls := 0
			e.deps.Nonce = func() (string, error) {
				calls++
				if during && calls == 2 {
					// Test Killer while this pre-record lease is held; no older run.
					must(t, e.quar.MarkHelper(cuid))
					must(t, e.killer.KillAll(context.Background(), cuid))
				}
				return e.nonce()
			}
			l := e.build()
			if !during {
				must(t, e.killer.KillAll(context.Background(), cuid))
			}
			_, err := l.Begin(context.Background(), "input-1")
			v, x := codes.Exit(err, 0, false)
			if !codes.Is(err, codes.OriginUnavailable) || x != nil || v.Status != 1 {
				t.Fatalf("the original failure code changed: %v %+v %v", err, v, x)
			}
			if uids := e.killer.UIDs(); len(uids) != 1 || uids[0] != cuid {
				t.Fatalf("helper invocations %v", uids)
			}
			if proof := sweep.Enumerate(e.proc.View(), cuid); !proof.Proved() {
				t.Fatalf("setup: the enumeration must show zero processes: %+v", proof)
			}
			if len(e.store.Keys()) != 0 || len(e.km.Binds()) != 0 {
				t.Fatalf("records=%v binds=%d", e.store.Keys(), len(e.km.Binds()))
			}
			e.src.SetPrepareMode("")
			run, err := l.Begin(context.Background(), "input-2")
			if !during {
				// Positive control: the lease was returned and is leased again.
				if v.Detail["lease"] != "" {
					t.Fatalf("control: lease reported quarantined: %+v", v.Detail)
				}
				must(t, err)
				if run.ChildUID != cuid || e.record(run.Key).State != runrecord.Bound {
					t.Fatalf("control: %+v", e.record(run.Key))
				}
				return
			}
			if v.Detail["lease"] != "quarantined" || v.Detail["lease_uid"] != "2001" || v.Detail["lease_reason"] != "privilege-drop helper was invoked for this lease" {
				t.Fatalf("quarantine not reported in the detail: %+v", v.Detail)
			}
			if !codes.Is(err, codes.IsolationUnqualified) || !errors.Is(err, sweep.ErrQuarantined) || run != nil || !e.quar.Held(cuid) {
				t.Fatalf("next Begin with the fixed uid: %v", err)
			}
		})
	}
}

// Claim (H2 order "connect and hello, then the durable binding record, then
// the bind frame"): when the binding record is created the Keymaker hello has
// already been answered and no bind frame has been written; the bind frame
// follows. Claim (hello failure): a Keymaker that closes on hello, or
// refuses it, fails Begin with run_bind_unavailable (retryable, exit 1), no
// record, no bind frame and the lease returned; the next attempt starts from
// a fresh H1 and binds with a new prepared_id, never the failed attempt's.
// Control kept beside it: a failure AFTER the bind frame is written (the
// peer closes on bind) stays HELD, run_bind_unknown, record binding.
func TestHelloOrder(t *testing.T) {
	e := newEnv(t)
	g := gated(e)
	var hellosAtCreate, bindsAtCreate int
	g.create = func(*runrecord.Record) error {
		hellosAtCreate, bindsAtCreate = e.km.Hellos(), len(e.km.Binds())
		return nil
	}
	run, err := e.build().Begin(context.Background(), "input-1")
	must(t, err)
	if hellosAtCreate != 1 || bindsAtCreate != 0 || len(e.km.Binds()) != 1 || e.record(run.Key).State != runrecord.Bound {
		t.Fatalf("order: hellos at create %d, binds at create %d, binds after %d", hellosAtCreate, bindsAtCreate, len(e.km.Binds()))
	}

	for _, mode := range []string{"close", "refuse", "silent"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			e.km.HelloMode = mode
			l := e.build()
			_, err := l.Begin(context.Background(), "input-1")
			v, x := codes.Exit(err, 0, false)
			if !codes.Is(err, codes.BindUnavailable) || x != nil || v.Status != 1 {
				t.Fatalf("%v %+v %v", err, v, x)
			}
			noTrace(t, e)
			if e.km.Hellos() != 1 || e.kmDialer.Dials() != 1 || e.quar.Held(cuid) {
				t.Fatalf("hellos=%d dials=%d held=%v", e.km.Hellos(), e.kmDialer.Dials(), e.quar.Held(cuid))
			}
			first := e.src.ValidatedIDs()
			if len(first) != 1 {
				t.Fatalf("validated %v", first)
			}
			e.km.SetHelloMode("")
			run, err := l.Begin(context.Background(), "input-2")
			must(t, err)
			ids := e.src.ValidatedIDs()
			_, prepares, _, conns := e.src.Counts()
			rec := e.record(run.Key)
			if len(ids) != 2 || ids[1] == first[0] || rec.PreparedID != ids[1] || rec.PreparedID == first[0] || prepares != 2 || conns != 2 {
				t.Fatalf("not a fresh H1: ids=%v record=%s prepares=%d conns=%d", ids, rec.PreparedID, prepares, conns)
			}
			if rec.State != runrecord.Bound || rec.ChildUID != cuid || len(e.km.Binds()) != 1 || len(e.store.Keys()) != 1 {
				t.Fatalf("record %+v binds=%d", rec, len(e.km.Binds()))
			}
		})
	}

	// Control: a failure after the bind frame was written stays HELD.
	e = newEnv(t)
	e.km.BindMode = "close"
	_, err = e.build().Begin(context.Background(), "input-1")
	keys := e.store.Keys()
	if !codes.Is(err, codes.BindUnknown) || len(keys) != 1 || e.record(keys[0]).State != runrecord.Binding || len(e.km.Binds()) != 1 || !e.quar.Held(cuid) {
		t.Fatalf("failure after the bind frame: %v keys=%v", err, keys)
	}
}

// ownerOrigin is the origin member of the corpus vector owner-grammar-only.
func ownerOrigin() []byte {
	return []byte(`{"command_id":"` + strings.Repeat("d", 64) + `","event_id":"","grant_id":"","kind":"owner_command","mode":"","tip_hash":""}`)
}

// Claim (admission origin): when the Source fixture returns
// the corpus owner_command origin from validate, Begin refuses
// run_origin_refused (exit 16) with zero Keymaker dials and no record, and
// the unused lease is returned. Positive control beside it: the same fixture
// returning the admission origin binds with one Keymaker dial.
func TestOwnerCommandOriginRefused(t *testing.T) {
	e := newEnv(t)
	e.src.Origin = ownerOrigin()
	_, err := e.build().Begin(context.Background(), "input-1")
	v, x := codes.Exit(err, 0, false)
	if !codes.Is(err, codes.OriginRefused) || x != nil || v.Status != 16 {
		t.Fatalf("%v %+v %v", err, v, x)
	}
	noTrace(t, e)
	if e.kmDialer.Dials() != 0 || e.quar.Held(cuid) {
		t.Fatalf("keymaker dials=%d held=%v", e.kmDialer.Dials(), e.quar.Held(cuid))
	}
	if _, _, validates, _ := e.src.Counts(); validates != 1 {
		t.Fatal("the origin was not refused at validate")
	}
	e = newEnv(t)
	e.src.Origin = fakes.ValidOrigin()
	run, err := e.build().Begin(context.Background(), "input-1")
	must(t, err)
	if e.kmDialer.Dials() != 1 || e.record(run.Key).State != runrecord.Bound {
		t.Fatal("admission control did not bind")
	}
}

// Claim (station binding): a Source result without the identity_registration_number
// binding is refused run_origin_refused for the (non-pinned) test Wire too,
// and so is a binding that differs from the station: zero Keymaker dials, no
// record, lease returned. Positive control: the binding equal to the station
// binds.
func TestRegistrationBindingRequired(t *testing.T) {
	for name, mut := range map[string]func(*env){
		"missing":   func(e *env) { e.src.OmitRegistration = true },
		"different": func(e *env) { e.src.Bindings = map[string]string{"identity_registration_number": "2"} },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			mut(e)
			_, err := e.build().Begin(context.Background(), "input-1")
			v, x := codes.Exit(err, 0, false)
			if !codes.Is(err, codes.OriginRefused) || x != nil || v.Status != 16 || v.Detail["binding"] != "identity_registration_number" {
				t.Fatalf("%v %+v %v", err, v, x)
			}
			noTrace(t, e)
			if e.kmDialer.Dials() != 0 || e.quar.Held(cuid) {
				t.Fatalf("keymaker dials=%d held=%v", e.kmDialer.Dials(), e.quar.Held(cuid))
			}
		})
	}
	e := newEnv(t)
	e.src.Bindings = map[string]string{"identity_registration_number": "1"}
	run, err := e.build().Begin(context.Background(), "input-1")
	must(t, err)
	if e.record(run.Key).State != runrecord.Bound {
		t.Fatal("control did not bind")
	}
}

// Claim (H0: the input principal's uid MUST equal the leased
// R_i): Begin with an input principal uid (22001) other than the leased uid
// (2001) refuses run_isolation_unqualified (exit 16) before any Source or
// Keymaker contact, writes no record and returns the lease; BeginChild
// refuses the same mismatch. The equal-uid neighbour binds, root and child.
func TestInputUIDIsLeasedUID(t *testing.T) {
	e := newEnv(t)
	l := e.build()
	l.Restart(context.Background())
	_, err := l.Launcher.Begin(context.Background(), event, principalFor(22001))
	v, x := codes.Exit(err, 0, false)
	if !codes.Is(err, codes.IsolationUnqualified) || x != nil || v.Status != 16 || v.Detail["uid"] != "2001" || v.Detail["input_uid"] != "22001" {
		t.Fatalf("%v %+v %v", err, v, x)
	}
	noTrace(t, e)
	if d, _ := e.srcDialer.Dials(); d != 0 || e.kmDialer.Dials() != 0 || e.quar.Held(cuid) {
		t.Fatalf("source dials=%d keymaker dials=%d held=%v", d, e.kmDialer.Dials(), e.quar.Held(cuid))
	}
	// Equal-uid neighbour, same launcher, same fixed uid.
	parent, err := l.Launcher.Begin(context.Background(), event, principalFor(cuid))
	must(t, err)
	if e.record(parent.Key).State != runrecord.Bound || e.record(parent.Key).ChildUID != cuid {
		t.Fatal("equal uid did not bind")
	}

	// Child run: the same rule.
	e = newEnv(t)
	seq := &seqPlacement{next: cuid}
	e.placement = seq
	l = e.build()
	parent, err = l.Begin(context.Background(), "input-1")
	must(t, err)
	binds, dials := len(e.km.Binds()), e.kmDialer.Dials()
	_, err = l.Launcher.BeginChild(context.Background(), parent, principalFor(22002))
	if !codes.Is(err, codes.IsolationUnqualified) || len(e.km.Binds()) != binds || e.kmDialer.Dials() != dials || len(e.store.Keys()) != 1 || e.quar.Held(cuid+1) {
		t.Fatalf("child mismatch: %v", err)
	}
	e.km.SetRunID("run-child")
	child, err := l.Launcher.BeginChild(context.Background(), parent, principalFor(seq.peek()))
	must(t, err)
	if rec := e.record(child.Key); rec.State != runrecord.Bound || rec.ChildUID != cuid+2 {
		t.Fatalf("equal-uid child: %+v", rec)
	}
}

// Claim (per-run event): Begin takes the retained event per run. Two Begins with two
// different events run concurrently through ONE shared *source.Client using
// the pinned Source adapter (source.Pinned): each prepare reaches Source
// with its own event and its own input principal, both runs bind, and the
// shared client's Event and InputPrincipal fields are still their zero
// values afterwards (the client is never mutated per run). Run under -race.
func TestConcurrentEvents(t *testing.T) {
	e := newEnv(t)
	ps := &fakes.PinnedSource{Registration: "1", Peer: source.Credential{UID: luid, PID: 1}}
	shared := e.deps.Source
	shared.Wire = source.Pinned{}
	shared.Dialer = ps
	var n atomic.Uint64
	e.deps.Nonce = func() (string, error) { return fmt.Sprintf("%032x", n.Add(1)), nil }
	events := []runorigin.EventRef{
		{RoomID: "!room-a:example", EventID: "$event-a"},
		{RoomID: "!room-b:example", EventID: "$event-b"},
	}
	// One launcher per fixed uid, as a caller that already knows each R_i
	// composes them; Source client, store, quarantine and Keymaker are shared.
	var launchers []*launcher.Launcher
	for i := range events {
		e.placement = isolation.Fixed(cuid + uint32(i))
		launchers = append(launchers, e.build().Launcher)
	}
	for _, l := range launchers {
		l.Restart(context.Background())
	}
	var wg sync.WaitGroup
	runs := make([]*launcher.Run, len(events))
	errs := make([]error, len(events))
	start := make(chan struct{})
	for i := range events {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			runs[i], errs[i] = launchers[i].Begin(context.Background(), events[i], principalFor(cuid+uint32(i)))
		}()
	}
	close(start)
	wg.Wait()
	for i := range events {
		must(t, errs[i])
		if rec := e.record(runs[i].Key); rec.State != runrecord.Bound || rec.ChildUID != cuid+uint32(i) {
			t.Fatalf("run %d: %+v", i, rec)
		}
	}
	if errs := ps.Errs(); len(errs) != 0 {
		t.Fatalf("pinned source fixture: %v", errs)
	}
	seen := map[runorigin.EventRef]uint32{}
	for _, p := range ps.Prepares() {
		seen[p.Event] = p.InputPrincipal.UID
	}
	if len(ps.Prepares()) != 2 || seen[events[0]] != cuid || seen[events[1]] != cuid+1 {
		t.Fatalf("events seen by Source: %+v", ps.Prepares())
	}
	if shared.Event != (runorigin.EventRef{}) || shared.InputPrincipal != (runorigin.Principal{}) {
		t.Fatalf("the shared Source client was mutated: %+v %+v", shared.Event, shared.InputPrincipal)
	}
}
