//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

var errStoreRefused = errors.New("fixture: store refuses this transition")

// gateStore is the env's real journal-backed store behind a gate: refuse
// decides, per transition, whether Update fails (with the returned error)
// before anything is committed; create does the same for Create. It follows
// env.store across a reopen.
type gateStore struct {
	e *env

	mu       sync.Mutex
	refuse   func(next *runrecord.Record) error
	create   func(r *runrecord.Record) error
	refusals int
}

func (g *gateStore) set(refuse func(next *runrecord.Record) error) {
	g.mu.Lock()
	g.refuse = refuse
	g.mu.Unlock()
}
func (g *gateStore) Create(r *runrecord.Record) error {
	g.mu.Lock()
	create := g.create
	g.mu.Unlock()
	if create != nil {
		if err := create(r); err != nil {
			return err
		}
	}
	return g.e.store.Create(r)
}
func (g *gateStore) Update(key string, mutate func(*runrecord.Record) error) (*runrecord.Record, error) {
	return g.e.store.Update(key, func(r *runrecord.Record) error {
		if err := mutate(r); err != nil {
			return err
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.refuse != nil {
			if err := g.refuse(r); err != nil {
				g.refusals++
				return err
			}
		}
		return nil
	})
}
func (g *gateStore) Get(key string) (*runrecord.Record, bool) { return g.e.store.Get(key) }
func (g *gateStore) StateDirectory() string                   { return g.e.store.StateDirectory() }
func (g *gateStore) Keys() []string                           { return g.e.store.Keys() }
func (g *gateStore) Fenced(key string) bool                   { return g.e.store.Fenced(key) }

func gated(e *env) *gateStore {
	g := &gateStore{e: e}
	e.records = g
	return g
}

func refuseState(s runrecord.State) func(*runrecord.Record) error {
	return func(next *runrecord.Record) error {
		if next.State == s {
			return errStoreRefused
		}
		return nil
	}
}

// eventually waits for a condition that depends on a fake's goroutine.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not reached: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func gone(t *testing.T, e *env, pid string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(e.proc.Root, pid))
	return errors.Is(err, os.ErrNotExist)
}

// Claim (H5, "if the ending intent cannot be persisted: send no end, return
// run_end_unknown with the cause; teardown, km.Close, the uid sweep and the
// quarantine evaluation run on every return path"): with a store that
// refuses the bound -> ending update, End returns run_end_unknown (exit 17,
// child_status in the detail, the store's error as cause), sends NO end
// frame, still calls teardown, closes the Keymaker connection, kills by uid
// (the released child is gone), evaluates the gate with ack=false (uid stays
// quarantined although export evidence exists) and leaves the record bound.
// Positive control beside it: the same gated store without the refusal ends
// with an exact ack, record ended, uid released.
func TestEndIntentRefused(t *testing.T) {
	e := newEnv(t)
	g := gated(e)
	run := endReady(t, e)
	e.exportEvidence[run.Key] = sweep.Exported
	g.set(refuseState(runrecord.Ending))
	torn := 0
	out, err := run.End(context.Background(), 3, true, func() error { torn++; return nil })
	if !codes.Is(err, codes.EndUnknown) || !errors.Is(err, errStoreRefused) {
		t.Fatalf("%v", err)
	}
	if out.Verdict.Status != 17 || out.Verdict.Code != codes.EndUnknown || out.Verdict.Detail["child_status"] != "3" || out.Verdict.Detail["run_id"] != run.RunID {
		t.Fatalf("verdict %+v", out.Verdict)
	}
	if len(e.km.Ends()) != 0 {
		t.Fatalf("an end was sent without a durable intent: %d", len(e.km.Ends()))
	}
	if torn != 1 {
		t.Fatalf("teardown calls %d", torn)
	}
	eventually(t, "keymaker connection closed", func() bool { return e.km.Closed() == 1 })
	if uids := e.killer.UIDs(); len(uids) != 1 || uids[0] != cuid || !gone(t, e, "777") {
		t.Fatalf("uid sweep did not run: %v", uids)
	}
	if out.Acknowledged || out.UIDReleased || out.Release.EndAcknowledged || !out.Release.Proved || !e.quar.Held(cuid) || !errors.Is(out.RecordErr, errStoreRefused) {
		t.Fatalf("outcome %+v held=%v", out, e.quar.Held(cuid))
	}
	if rec := e.record(run.Key); rec.State != runrecord.Bound || rec.EndRequestID != "" {
		t.Fatalf("record %+v", rec)
	}
	if err := run.ReleaseInput(func() error { return nil }); !errors.Is(err, launcher.ErrFenced) && !errors.Is(err, launcher.ErrNotBound) {
		t.Fatalf("run not fenced after End: %v", err)
	}

	// Positive control: same fixture, no refusal.
	e = newEnv(t)
	gated(e)
	run = endReady(t, e)
	e.exportEvidence[run.Key] = sweep.Exported
	out, err = run.End(context.Background(), 3, true, nil)
	must(t, err)
	if !out.Acknowledged || !out.UIDReleased || out.Verdict.Status != 3 || len(e.km.Ends()) != 1 || e.record(run.Key).State != runrecord.Ended {
		t.Fatalf("positive control %+v", out)
	}
}

// Claim (H5, the later durable steps): when the store refuses to record
// teardown, or refuses the ending -> ended update after an exact ack was
// read, End still ran teardown and the uid sweep, returns run_end_unknown
// with the store's error, keeps the uid quarantined (the gate saw ack=false
// because the ack is not durable) and leaves the record ending; once the
// store accepts updates again RetryEnd replays the exact end, the record is
// ended and the uid released (the positive control).
func TestEndAckNotDurable(t *testing.T) {
	for name, refuse := range map[string]func(*runrecord.Record) error{
		"teardown": func(next *runrecord.Record) error {
			if next.TeardownDone {
				return errStoreRefused
			}
			return nil
		},
		"ended": refuseState(runrecord.Ended),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			run := endReady(t, e)
			e.exportEvidence[run.Key] = sweep.Exported
			g.set(refuse)
			torn := false
			out, err := run.End(context.Background(), 0, true, func() error { torn = true; return nil })
			if !codes.Is(err, codes.EndUnknown) || !errors.Is(err, errStoreRefused) || out.Verdict.Status != 17 {
				t.Fatalf("%v %+v", err, out.Verdict)
			}
			if !torn || len(e.killer.UIDs()) != 1 || !gone(t, e, "777") || len(e.km.Ends()) != 1 {
				t.Fatalf("torn=%v kills=%v ends=%d", torn, e.killer.UIDs(), len(e.km.Ends()))
			}
			if out.Acknowledged || out.UIDReleased || !e.quar.Held(cuid) || e.record(run.Key).State != runrecord.Ending {
				t.Fatalf("outcome %+v state=%s", out, e.record(run.Key).State)
			}
			g.set(nil)
			out, err = e.l.RetryEnd(context.Background(), run.Key)
			must(t, err)
			if !out.Acknowledged || !out.UIDReleased || e.quar.Held(cuid) || e.record(run.Key).State != runrecord.Ended || len(e.km.Ends()) != 2 {
				t.Fatalf("retry %+v", out)
			}
		})
	}
}

// orphan plants a bound, input-released record of a dead launcher with a
// live child, as a killed launcher leaves it.
func orphan(t *testing.T, e *env, key string, uid uint32, pid int) {
	t.Helper()
	dead := &incarnation.Incarnation{PID: 4242, UID: luid, StartTicks: 1, BootID: "boot-1"}
	must(t, e.store.Create(&runrecord.Record{Version: runrecord.Version, Kind: runrecord.Kind, Key: key, State: runrecord.Binding,
		LauncherUID: luid, LauncherGeneration: "gen-a", StationRegistration: "1", InputPrincipal: "input-1",
		Origin:      fakes.ValidOrigin(),
		LaunchNonce: key, SourceIncarnation: "inc-1", PreparedID: "prep-1", BindRequestID: "bind-" + key, ChildUID: uid, Launcher: dead}))
	_, err := e.store.Update(key, func(r *runrecord.Record) error { r.State = runrecord.Bound; r.RunID = "run-" + key; return nil })
	must(t, err)
	must(t, e.proc.Add(pid, [4]uint32{uid, uid, uid, uid}, 5))
	inc, err := e.proc.Incarnations().Read(pid)
	must(t, err)
	_, err = e.store.Update(key, func(r *runrecord.Record) error { r.Child = &inc; return nil })
	must(t, err)
	_, err = e.store.Update(key, func(r *runrecord.Record) error { r.InputReleased = true; return nil })
	must(t, err)
}

// Claim (H6, the restart path: "endOrphan sweeps even when its Update
// fails"): at restart, with a store that refuses the bound -> ending update,
// the bound orphan of a dead launcher gets NO end frame but its uid is still
// swept (the orphan process is killed), the report carries run_end_unknown
// with the store's error, the record stays bound and the uid quarantined;
// an ending record whose teardown cannot be recorded is still swept too.
// Positive control: the next restart with an accepting store ends both
// (exact acks), and with export evidence both uids are released.
func TestRestartStoreRefuses(t *testing.T) {
	e := newEnv(t)
	orphan(t, e, "orphan", 2004, 400)
	orphan(t, e, "ending", 2005, 500)
	_, err := e.store.Update("ending", func(r *runrecord.Record) error {
		r.State = runrecord.Ending
		r.EndRequestID = "end-ending"
		r.EndRequestBody = endBody(t, "end-ending", "run-ending")
		return nil
	})
	must(t, err)
	e.exportEvidence["orphan"] = sweep.Exported
	e.exportEvidence["ending"] = sweep.Exported
	e.reopen()
	g := gated(e)
	g.set(func(next *runrecord.Record) error {
		if next.State == runrecord.Ending && (next.Key == "orphan" || next.TeardownDone) {
			return errStoreRefused
		}
		return nil
	})
	l := e.build()
	got := map[string]launcher.Report{}
	for _, r := range l.Restart(context.Background()) {
		got[r.Key] = r
	}
	rep := got["orphan"]
	if rep.Action != runrecord.ActionEndThenSweep || rep.Code != codes.EndUnknown || !codes.Is(rep.Err, codes.EndUnknown) || !errors.Is(rep.Err, errStoreRefused) || rep.Outcome == nil {
		t.Fatalf("orphan report %+v", rep)
	}
	if v, x := codes.Exit(rep.Err, 0, false); x != nil || v.Status != 17 {
		t.Fatalf("orphan exit %+v %v", v, x)
	}
	if rep.Outcome.Acknowledged || rep.Outcome.UIDReleased || rep.Outcome.Sweep.Kills != 1 || !rep.Outcome.Sweep.Final.Proved() {
		t.Fatalf("orphan outcome %+v", rep.Outcome)
	}
	rep = got["ending"]
	if rep.Action != runrecord.ActionRetryEnd || !codes.Is(rep.Err, codes.EndUnknown) || !errors.Is(rep.Err, errStoreRefused) || rep.Outcome == nil || rep.Outcome.Acknowledged || rep.Outcome.UIDReleased || rep.Outcome.Sweep.Kills != 1 {
		t.Fatalf("ending report %+v", rep)
	}
	killed := map[uint32]bool{}
	for _, u := range e.killer.UIDs() {
		killed[u] = true
	}
	if !killed[2004] || !killed[2005] || !gone(t, e, "400") || !gone(t, e, "500") {
		t.Fatalf("orphans not swept: %v", e.killer.UIDs())
	}
	// No end for the orphan (no durable intent); exactly the stored end for "ending".
	if ends := e.km.Ends(); len(ends) != 1 || string(ends[0]) != string(e.record("ending").EndRequestBody) {
		t.Fatalf("ends %d", len(ends))
	}
	if e.record("orphan").State != runrecord.Bound || e.record("ending").State != runrecord.Ending || !e.quar.Held(2004) || !e.quar.Held(2005) {
		t.Fatalf("states %s %s", e.record("orphan").State, e.record("ending").State)
	}
	// Positive control: a fresh process recovers once the store accepts updates.
	g.set(nil)
	e.reopen()
	l = e.build()
	for _, r := range l.Restart(context.Background()) {
		if r.Err != nil || r.Outcome == nil || !r.Outcome.Acknowledged || !r.Outcome.UIDReleased {
			t.Fatalf("control restart %s: %+v", r.Key, r)
		}
	}
	if e.record("orphan").State != runrecord.Ended || e.record("ending").State != runrecord.Ended || e.quar.Held(2004) || e.quar.Held(2005) {
		t.Fatal("control restart did not end and release")
	}
}

// Claim (durable failure classification in Begin): (1) Keymaker
// bound the run but the bound update is refused: run_bind_unknown, exit 17,
// with the store's error as cause and run_id in the detail; the record stays
// binding and the uid quarantined. (2) The binding record is refused before
// any append: run_bind_unavailable with record=none and bind_sent=false, zero
// bind frames, no record, and the unused lease is returned. (3) The binding
// commit fails with durability uncertain: run_bind_unknown with
// record=uncertain, zero bind frames, and the uid stays quarantined. None is
// an unclassified error. Positive control: the same gated store without a
// refusal binds.
func TestBeginDurability(t *testing.T) {
	exit17 := func(t *testing.T, err error) codes.Verdict {
		t.Helper()
		v, x := codes.Exit(err, 0, false)
		if !codes.Is(err, codes.BindUnknown) || x != nil || v.Status != 17 {
			t.Fatalf("%v %+v %v", err, v, x)
		}
		return v
	}
	t.Run("bound", func(t *testing.T) {
		e := newEnv(t)
		gated(e).set(refuseState(runrecord.Bound))
		run, err := e.build().Begin(context.Background(), "input-1")
		v := exit17(t, err)
		keys := e.store.Keys()
		if run != nil || !errors.Is(err, errStoreRefused) || v.Detail["run_id"] != "run-1" || len(keys) != 1 || v.Detail["key"] != keys[0] {
			t.Fatalf("%v %+v", err, v)
		}
		if e.record(keys[0]).State != runrecord.Binding || !e.quar.Held(cuid) || len(e.km.Binds()) != 1 {
			t.Fatalf("state %s held=%v binds=%d", e.record(keys[0]).State, e.quar.Held(cuid), len(e.km.Binds()))
		}
	})
	t.Run("create", func(t *testing.T) {
		e := newEnv(t)
		gated(e).create = func(*runrecord.Record) error { return errStoreRefused }
		_, err := e.build().Begin(context.Background(), "input-1")
		v, x := codes.Exit(err, 0, false)
		if !codes.Is(err, codes.BindUnavailable) || x != nil || v.Status != 1 {
			t.Fatalf("clean Create: %v %+v %v", err, v, x)
		}
		if !errors.Is(err, errStoreRefused) || v.Detail["record"] != "none" || v.Detail["bind_sent"] != "false" || v.Detail["lease"] != "" {
			t.Fatalf("%v %+v", err, v)
		}
		if len(e.store.Keys()) != 0 || len(e.km.Binds()) != 0 || e.km.Hellos() != 1 || e.quar.Held(cuid) {
			t.Fatalf("keys=%v binds=%d hellos=%d held=%v", e.store.Keys(), len(e.km.Binds()), e.km.Hellos(), e.quar.Held(cuid))
		}
	})
	t.Run("commit", func(t *testing.T) {
		e := newEnv(t)
		gated(e).create = func(*runrecord.Record) error { return errors.Join(runrecord.ErrCommit, errStoreRefused) }
		_, err := e.build().Begin(context.Background(), "input-1")
		v := exit17(t, err)
		if v.Detail["record"] != "uncertain" || v.Detail["bind_sent"] != "false" || len(e.km.Binds()) != 0 || !e.quar.Held(cuid) {
			t.Fatalf("%v %+v held=%v", err, v, e.quar.Held(cuid))
		}
	})
	t.Run("control", func(t *testing.T) {
		e := newEnv(t)
		gated(e)
		run, err := e.build().Begin(context.Background(), "input-1")
		must(t, err)
		if e.record(run.Key).State != runrecord.Bound {
			t.Fatal("control did not bind")
		}
	})
}

// Claim (D4, "an acknowledged end with childKnown=false is classified per
// codes.go, never a zero Verdict with nil error"): End with an exact ack but
// an unknown child status returns codes.ErrUnclassified with the record
// ended and the outcome acknowledged; the neighbour with a known child
// status returns nil and that status.
func TestEndAckUnknownChild(t *testing.T) {
	e := newEnv(t)
	run := endReady(t, e)
	out, err := run.End(context.Background(), 0, false, nil)
	if !errors.Is(err, codes.ErrUnclassified) || err == nil {
		t.Fatalf("%v", err)
	}
	if !out.Acknowledged || e.record(run.Key).State != runrecord.Ended || out.Verdict.Class != "" || out.Verdict.Code != "" {
		t.Fatalf("outcome %+v", out)
	}
	e = newEnv(t)
	run = endReady(t, e)
	out, err = run.End(context.Background(), 5, true, nil)
	if err != nil || out.Verdict.Status != 5 || !out.Acknowledged {
		t.Fatalf("neighbour: %v %+v", err, out)
	}
}

// Claim (journal sync failure requires exit): when the failed durable step
// wraps the journal's sync failure, the launcher error satisfies
// launcher.MustExit, the outcome says MustExit and the verdict detail carries
// must_exit=true, for End and for Begin; the neighbour with an ordinary store
// refusal carries none of them.
func TestMustExitSurfaced(t *testing.T) {
	syncFailure := errors.Join(errStoreRefused, journal.ErrSyncFailure)
	for name, c := range map[string]struct {
		cause error
		want  bool
	}{"sync": {syncFailure, true}, "ordinary": {errStoreRefused, false}} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			run := endReady(t, e)
			g.set(func(next *runrecord.Record) error {
				if next.State == runrecord.Ending {
					return c.cause
				}
				return nil
			})
			out, err := run.End(context.Background(), 0, true, nil)
			if !codes.Is(err, codes.EndUnknown) || launcher.MustExit(err) != c.want || out.MustExit != c.want || (out.Verdict.Detail["must_exit"] == "true") != c.want {
				t.Fatalf("end: %v %+v", err, out)
			}
			e = newEnv(t)
			gated(e).set(func(next *runrecord.Record) error {
				if next.State == runrecord.Bound {
					return c.cause
				}
				return nil
			})
			_, err = e.build().Begin(context.Background(), "input-1")
			v, x := codes.Exit(err, 0, false)
			if !codes.Is(err, codes.BindUnknown) || x != nil || launcher.MustExit(err) != c.want || (v.Detail["must_exit"] == "true") != c.want {
				t.Fatalf("begin: %v %+v", err, v)
			}
		})
	}
}
