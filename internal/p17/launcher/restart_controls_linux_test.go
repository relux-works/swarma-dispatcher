//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

func holdRecord(q *sweep.Quarantine, uid uint32, key string) {
	switch q := any(q).(type) {
	case interface{ Hold(uint32, ...string) error }:
		_ = q.Hold(uid, key)
	case interface{ Hold(uint32) }:
		q.Hold(uid)
	}
}
func leaseRecord(q *sweep.Quarantine, uid uint32, key string) error {
	switch q := any(q).(type) {
	case interface{ Lease(uint32, ...string) error }:
		return q.Lease(uid, key)
	case interface{ Lease(uint32) error }:
		return q.Lease(uid)
	}
	return errors.New("unsupported lease API")
}

func freshProcess(t *testing.T, e *env) {
	t.Helper()
	if c, ok := any(e.quar).(interface{ Close() error }); ok {
		must(t, c.Close())
	}
	must(t, e.store.Close())
	e.quar = &sweep.Quarantine{}
	e.cfg.Installation = sweep.Established
	must(t, e.quar.Start(e.jcfg.Directory, sweep.Established))
	var err error
	if len(e.store.Keys()) == 0 {
		e.store, err = runrecord.Initialize(e.jcfg)
	} else {
		e.store, err = runrecord.Open(e.jcfg, e.quar)
	}
	must(t, err)
	t.Cleanup(func() { _ = e.store.Close() })
}

// Claim: a fresh launcher and quarantine retain pre-record process/helper holds; a clean failure returns its lease across restart.
func TestDurablePreRecordLease(t *testing.T) {
	for _, mode := range []string{"process", "helper", "clean"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			e.src.PrepareMode = "dependency"
			if mode == "process" {
				must(t, e.proc.Add(555, [4]uint32{0, 0, cuid, 0}, 9))
			}
			calls := 0
			e.deps.Nonce = func() (string, error) {
				calls++
				if mode == "helper" && calls == 2 {
					entry, _ := e.quar.Entry(cuid)
					must(t, e.quar.MarkHelper(cuid, entry.Holder))
					must(t, e.killer.KillAll(context.Background(), cuid))
				}
				return e.nonce()
			}
			_, err := e.build().Begin(context.Background(), "first")
			if !codes.Is(err, codes.OriginUnavailable) {
				t.Fatalf("first failure: %v", err)
			}
			freshProcess(t, e)
			e.src.SetPrepareMode("")
			l := e.build()
			l.Restart(context.Background())
			before, _ := e.srcDialer.Dials()
			run, err := l.Begin(context.Background(), "second")
			if mode == "clean" {
				must(t, err)
				if run == nil {
					t.Fatal("clean restart did not bind")
				}
				return
			}
			after, _ := e.srcDialer.Dials()
			if !codes.Is(err, codes.IsolationUnqualified) || run != nil || after != before || e.kmDialer.Dials() != 0 || !e.quar.Held(cuid) {
				t.Fatalf("pre-record hold lost across restart: %v source=%d/%d held=%v", err, before, after, e.quar.Held(cuid))
			}
		})
	}
}

// Claim: a nil Keymaker dialer is a named definite unpinned refusal, beside a working dialer control.
func TestNilKeymakerDialer(t *testing.T) {
	e := newEnv(t)
	e.deps.Keymaker = nil
	_, err := e.build().Begin(context.Background(), "nil")
	if !codes.IsUnpinned(err) {
		t.Fatalf("nil dialer not named unpinned: %v", err)
	}
	if v, x := codes.Exit(err, 0, false); x != nil || v.Status != 16 {
		t.Fatalf("nil dialer not definite: %+v %v", v, x)
	}
	noTrace(t, e)
	e = newEnv(t)
	_, err = e.build().Begin(context.Background(), "control")
	must(t, err)
}

// Claim: an operator release cannot lease over an ending record; after the old end is acknowledged the uid can be leased.
func TestEndingRecordBlocksReuse(t *testing.T) {
	e := newEnv(t)
	e.km.EndMode = "close"
	run := endReady(t, e)
	e.operator[run.Key] = cuid
	out, err := run.End(context.Background(), 0, true, nil)
	if !codes.Is(err, codes.EndUnknown) || out.UIDReleased || e.record(run.Key).State != runrecord.Ending {
		t.Fatalf("setup %+v %v", out, err)
	}
	dials := e.kmDialer.Dials()
	before, _ := e.srcDialer.Dials()
	_, err = e.l.Launcher.Begin(context.Background(), event, principalFor(cuid))
	after, _ := e.srcDialer.Dials()
	if !codes.Is(err, codes.IsolationUnqualified) || dials != e.kmDialer.Dials() || before != after {
		t.Fatalf("leased over ending record: %v", err)
	}
	e.km.SetEndMode("")
	// An unacknowledged end retains its hold, so RetryEnd still owns its sweep.
	_, err = e.l.RetryEnd(context.Background(), run.Key)
	must(t, err)
	_, err = e.l.Begin(context.Background(), "after-ended")
	must(t, err)
}

// Claim: a record write outside the post-hello guard writes no bind, returns unsent and the uid, then uses a fresh prepare; a fast write binds.
func TestBindStartGuard(t *testing.T) {
	e := newEnv(t)
	g := gated(e)
	g.create = func(*runrecord.Record) error { time.Sleep(2100 * time.Millisecond); return nil }
	l := e.build()
	_, err := l.Begin(context.Background(), "slow")
	keys := e.store.Keys()
	if !codes.Is(err, codes.BindUnavailable) || len(keys) != 1 || string(e.record(keys[0]).State) != "unsent" || e.quar.Held(cuid) || len(e.km.Binds()) != 0 {
		t.Fatalf("late Create not unsent: %v keys=%v held=%v binds=%d", err, keys, e.quar.Held(cuid), len(e.km.Binds()))
	}
	old := e.record(keys[0]).PreparedID
	if e.km.BindBytes() != 0 {
		t.Fatal("late Create sent bind bytes")
	}
	g.create = nil
	r, err := l.Begin(context.Background(), "fast")
	must(t, err)
	if e.record(r.Key).PreparedID == old {
		t.Fatal("prepared ID reused")
	}
	for _, b := range e.km.Binds() {
		if len(b) == 0 {
			t.Fatal("empty bind")
		}
	}
	if len(e.src.ValidatedIDs()) != 2 || len(e.km.Binds()) != 1 {
		t.Fatal("not a fresh Source prepare")
	}
}

// Claim: Begin and RetryEnd before Restart refuse without downstream contact; Restart then permits Begin.
func TestRestartRequired(t *testing.T) {
	e := newEnv(t)
	l := e.build()
	_, err := l.Launcher.Begin(context.Background(), event, principalFor(cuid))
	if !codes.Is(err, codes.IsolationUnqualified) {
		t.Fatalf("Begin before Restart: %v", err)
	}
	_, err = l.RetryEnd(context.Background(), "missing")
	if !codes.Is(err, codes.IsolationUnqualified) {
		t.Fatalf("RetryEnd before Restart: %v", err)
	}
	if d, _ := e.srcDialer.Dials(); d != 0 || e.kmDialer.Dials() != 0 {
		t.Fatal("contact before Restart")
	}
	l.Restart(context.Background())
	_, err = l.Begin(context.Background(), "control")
	must(t, err)
}

// Claim: canonical positive root generations bind; noncanonical roots refuse before contact; child generations remain opaque.
func TestLauncherGenerationPolicy(t *testing.T) {
	for _, gen := range []string{"01", "0", "abc", "18446744073709551616", "1"} {
		t.Run(gen, func(t *testing.T) {
			e := newEnv(t)
			l := e.build()
			l.Restart(context.Background())
			p := principalFor(cuid)
			p.Generation = gen
			_, err := l.Launcher.Begin(context.Background(), event, p)
			if gen == "1" {
				must(t, err)
				return
			}
			if !codes.Is(err, codes.OriginRefused) {
				t.Fatalf("invalid root generation: %v", err)
			}
			if d, _ := e.srcDialer.Dials(); d != 0 || e.kmDialer.Dials() != 0 {
				t.Fatal("contact for invalid generation")
			}
		})
	}
	e := newEnv(t)
	e.placement = &seqPlacement{next: cuid}
	l := e.build()
	parent, err := l.Begin(context.Background(), "parent")
	must(t, err)
	p := principalFor(cuid + 1)
	p.Generation = "世代-α"
	_, err = l.Launcher.BeginChild(context.Background(), parent, p)
	must(t, err)
	_, got, err := parseLastBind(e)
	must(t, err)
	if got != p.Generation || !bytes.Contains(e.km.Binds()[1], []byte(p.Generation)) {
		t.Fatalf("child generation changed on wire: %q", got)
	}
}

func parseLastBind(e *env) (string, string, error) {
	b := e.km.Binds()
	if len(b) == 0 {
		return "", "", errors.New("no bind")
	}
	id, req, err := fakes.ParseBind(b[len(b)-1])
	return id, req.InputPrincipal.Generation, err
}

// Claim: an operator proof cannot release a live record's uid, which cannot be re-leased; a different uid still binds.
func TestLiveRecordLeaseGuard(t *testing.T) {
	e := newEnv(t)
	r, err := e.build().Begin(context.Background(), "old")
	must(t, err)
	// Exercise the pool's record guard directly, without relying on restart holds.
	proof := sweep.Enumerate(e.proc.View(), cuid)
	e.operator[r.Key] = cuid
	d, err := e.quar.Release(r.Key, cuid, false, proof)
	if !errors.Is(err, sweep.ErrQuarantined) || d.Release || d.Reason != "conflicting non-terminal record" {
		t.Fatalf("live record released: %+v %v", d, err)
	}
	if err = leaseRecord(e.quar, cuid, "new"); !errors.Is(err, sweep.ErrQuarantined) {
		t.Fatalf("record guard bypassed: %v", err)
	}
	e.placement = isolation.Fixed(cuid + 1)
	_, err = e.build().Begin(context.Background(), "other-uid")
	must(t, err)
}
