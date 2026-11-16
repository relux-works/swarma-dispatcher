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

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Claim: durable unsent/refused and uncertain terminal updates expose the independent record key/state without losing their code or sentinel; pre-record failures expose no key.
func TestBeginRecordIdentity(t *testing.T) {
	for _, mode := range []string{"unsent", "refused", "unsent-uncertain", "refused-uncertain", "pre-record"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			state := runrecord.Unsent
			want := codes.BindUnavailable
			sentinel := keymaker.ErrUnsent
			switch mode {
			case "unsent", "unsent-uncertain":
				g.create = func(*runrecord.Record) error { time.Sleep(1600 * time.Millisecond); return nil }
			case "refused", "refused-uncertain":
				e.km.BindMode = "refuse"
				state = runrecord.Refused
				want = codes.BindRefused
				sentinel = keymaker.ErrRefused
			case "pre-record":
				e.src.PrepareMode = "dependency"
				want = codes.OriginUnavailable
			}
			uncertain := mode == "unsent-uncertain" || mode == "refused-uncertain"
			if uncertain {
				g.set(refuseState(state))
				want = codes.BindUnknown
			}
			run, err := e.build().Begin(context.Background(), "outcome")
			if run != nil || !codes.Is(err, want) {
				t.Fatalf("outcome: run=%v err=%v", run, err)
			}
			var identity *launcher.BeginRecordError
			if mode == "pre-record" {
				if errors.As(err, &identity) || len(e.store.Keys()) != 0 || e.quar.Held(cuid) {
					t.Fatalf("pre-record identity: %v", err)
				}
				return
			}
			if !errors.As(err, &identity) || identity.RecordKey() == "" || identity.RecordState() != state || identity.RecordUncertain() != uncertain {
				t.Fatalf("identity: %v", err)
			}
			rec := e.record(identity.RecordKey())
			if identity.RecordKey() == rec.BindRequestID || len(e.store.Keys()) != 1 || e.quar.Held(cuid) != uncertain {
				t.Fatalf("record/lease identity: %+v %v", rec, err)
			}
			if uncertain {
				if !errors.Is(err, errStoreRefused) || rec.State != runrecord.Binding {
					t.Fatalf("uncertain cause: %+v %v", rec, err)
				}
			} else if !errors.Is(err, sentinel) || rec.State != state {
				t.Fatalf("terminal cause: %+v %v", rec, err)
			}
			verdict, exitErr := codes.Exit(err, 0, false)
			must(t, exitErr)
			status := 16
			if state == runrecord.Unsent {
				status = 1
			}
			if uncertain {
				status = 17
			}
			if verdict.Status != status {
				t.Fatalf("status: %+v", verdict)
			}
		})
	}
}

// Claim: a committed unsent frame plus materialization failure is HELD/unknown now, but replay restores true unsent and returns the uid on proof; successful materialization is immediately terminal.
func TestUnsentCommitReplay(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(map[bool]string{true: "failed", false: "control"}[fail], func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			g.create = func(*runrecord.Record) error { time.Sleep(1600 * time.Millisecond); return nil }
			var planted string
			if fail {
				g.set(func(next *runrecord.Record) error {
					if next.State == runrecord.Unsent {
						planted = filepath.Join(e.jcfg.Directory, e.jcfg.Records, runrecord.RecordName(next.Key))
						must(t, os.Remove(planted))
						must(t, os.Mkdir(planted, 0700))
					}
					return nil
				})
			}
			run, err := e.build().Begin(context.Background(), "never-written")
			var identity *launcher.BeginRecordError
			if run != nil || !errors.As(err, &identity) || identity.RecordState() != runrecord.Unsent || identity.RecordUncertain() != fail {
				t.Fatalf("identity: %v", err)
			}
			rec := e.record(identity.RecordKey())
			want := codes.BindUnavailable
			if fail {
				want = codes.BindUnknown
			}
			if !codes.Is(err, want) || !errors.Is(err, keymaker.ErrUnsent) || e.quar.Held(cuid) != fail || rec.State != runrecord.Unsent || len(e.km.Binds()) != 0 {
				t.Fatalf("current process: %+v %v held=%v", rec, err, e.quar.Held(cuid))
			}
			if fail {
				if !runrecord.Uncertain(err) {
					t.Fatalf("not a commit error: %v", err)
				}
				// Closing the failed session repeats its original materialization error.
				closeErr := e.store.Close()
				if closeErr == nil || !errors.Is(err, closeErr) {
					t.Fatalf("close lost original failure: %v", closeErr)
				}
				must(t, os.Remove(planted))
			}
			e.reopen()
			e.records = nil
			reports := e.build().Restart(context.Background())
			if len(reports) != 1 || reports[0].State != runrecord.Unsent || reports[0].Action != runrecord.ActionNeverAdopt || e.quar.Held(cuid) || e.record(rec.Key).State != runrecord.Unsent || len(e.km.Ends()) != 0 {
				t.Fatalf("replay: %+v", reports)
			}
		})
	}
}

// Claim: shared launchers require one recovery, refuse subsequent recovery without touching live runs, and share successful readiness; concurrent New calls do not race End.
func TestSharedRecoveryOnce(t *testing.T) {
	e := newEnv(t)
	e.placement = &seqPlacement{next: cuid}
	a := e.build()
	b := e.build()
	if _, err := b.Launcher.Begin(context.Background(), event, principalFor(cuid)); !codes.Is(err, codes.IsolationUnqualified) {
		t.Fatalf("cold second launcher: %v", err)
	}
	if n, _ := e.srcDialer.Dials(); n != 0 {
		t.Fatal("cold launcher contacted Source")
	}
	if reports := a.Restart(context.Background()); len(reports) != 0 {
		t.Fatalf("first recovery: %+v", reports)
	}
	first, err := a.Launcher.Begin(context.Background(), event, principalFor(cuid))
	must(t, err)
	for _, l := range []*fixtureLauncher{a, b} {
		reports := l.Restart(context.Background())
		if len(reports) != 1 || !errors.Is(reports[0].Err, sweep.ErrRestarted) || !codes.Is(reports[0].Err, codes.IsolationUnqualified) {
			t.Fatalf("repeated recovery: %+v", reports)
		}
	}
	if e.record(first.Key).State != runrecord.Bound || len(e.km.Ends()) != 0 || len(e.killer.UIDs()) != 0 {
		t.Fatal("repeated recovery touched a live run")
	}
	second, err := b.Launcher.Begin(context.Background(), event, principalFor(cuid+1))
	must(t, err)
	if second.ChildUID != cuid+1 || len(e.km.Binds()) != 2 {
		t.Fatal("successful recovery not shared")
	}
	e.exportEvidence[first.Key] = sweep.Exported
	var wg sync.WaitGroup
	construction := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			if _, err := launcher.New(e.cfg, e.deps); err != nil {
				construction <- err
				return
			}
		}
		construction <- nil
	}()
	out, err := first.End(context.Background(), 0, true, nil)
	wg.Wait()
	must(t, <-construction)
	must(t, err)
	if !out.UIDReleased {
		t.Fatalf("uncontested end: %+v", out)
	}
}

type directorylessRecords struct{ launcher.RecordStore }

// Claim: New refuses missing installation, a mismatched or undiscoverable store directory and a different shared store; matching composition is accepted with zero contact.
func TestCompositionIdentity(t *testing.T) {
	e := newEnv(t)
	cfg := e.cfg
	cfg.Installation = 0
	if _, err := launcher.New(cfg, e.depsWithRecords()); !errors.Is(err, sweep.ErrInstallation) {
		t.Fatalf("missing fact: %v", err)
	}
	cfg = e.cfg
	cfg.StateDirectory = filepath.Join(e.jcfg.Directory, "other")
	if _, err := launcher.New(cfg, e.depsWithRecords()); !errors.Is(err, sweep.ErrConfiguration) {
		t.Fatalf("mismatched directory: %v", err)
	}
	cfg = e.cfg
	cfg.Installation = sweep.Established
	if _, err := launcher.New(cfg, e.depsWithRecords()); !errors.Is(err, sweep.ErrInstallation) {
		t.Fatalf("contradictory fact: %v", err)
	}
	deps := e.depsWithRecords()
	deps.Records = directorylessRecords{e.store}
	if _, err := launcher.New(e.cfg, deps); !errors.Is(err, sweep.ErrConfiguration) {
		t.Fatalf("directoryless decorator: %v", err)
	}
	_, err := launcher.New(e.cfg, e.depsWithRecords())
	must(t, err)
	other := newEnv(t)
	deps = other.depsWithRecords()
	deps.Quarantine = e.quar
	if _, err := launcher.New(other.cfg, deps); !errors.Is(err, sweep.ErrConfiguration) {
		t.Fatalf("different store: %v", err)
	}
	if n, _ := e.srcDialer.Dials(); n != 0 || e.kmDialer.Dials() != 0 {
		t.Fatal("composition check contacted a peer")
	}
}
func (e *env) depsWithRecords() launcher.Deps {
	d := e.deps
	d.Records = e.store
	d.Quarantine = e.quar
	return d
}

// Claim: sticky initialization failure and its later closed-store error both remain unknown/HELD with typed uncertain keys, never clean refusals; the valid store binds.
func TestStickyCreateRemainsUncertain(t *testing.T) {
	e := newEnv(t)
	e.placement = &seqPlacement{next: cuid}
	g := gated(e)
	g.create = func(*runrecord.Record) error {
		g.create = nil
		return os.Remove(filepath.Join(e.jcfg.Directory, e.jcfg.Records))
	}
	l := e.build()
	for _, uid := range []uint32{cuid, cuid + 1} {
		_, err := l.Begin(context.Background(), "closed-store")
		var identity *launcher.BeginRecordError
		if !codes.Is(err, codes.BindUnknown) || !runrecord.Uncertain(err) || !e.quar.Held(uid) || !errors.As(err, &identity) || !identity.RecordUncertain() {
			t.Fatalf("uid=%d error=%v", uid, err)
		}
		if uid == cuid+1 && !errors.Is(err, runrecord.ErrClosed) {
			t.Fatalf("not the sticky tail: %v", err)
		}
	}
	if len(e.km.Binds()) != 0 {
		t.Fatal("closed store sent a bind")
	}
	e = newEnv(t)
	_, err := e.build().Begin(context.Background(), "control")
	must(t, err)
}

// Claim: RetryEnd preserves the typed nil-dialer refusal and the ending hold; a configured fresh dialer acknowledges and releases.
func TestRetryEndNilDialerIsUnpinned(t *testing.T) {
	e := newEnv(t)
	e.km.EndMode = "close"
	run := endReady(t, e)
	_, err := run.End(context.Background(), 0, true, nil)
	if !codes.Is(err, codes.EndUnknown) {
		t.Fatalf("setup: %v", err)
	}
	e.deps.Keymaker = nil
	l := e.build()
	out, err := l.RetryEnd(context.Background(), run.Key)
	if !codes.IsUnpinned(err) || out.UIDReleased || e.record(run.Key).State != runrecord.Ending || !e.quar.Held(cuid) {
		t.Fatalf("nil retry: %+v %v", out, err)
	}
	v, x := codes.Exit(err, 0, false)
	must(t, x)
	if v.Status != 16 {
		t.Fatalf("nil retry status: %+v", v)
	}
	e.deps.Keymaker = e.kmDialer
	e.km.SetEndMode("")
	e.exportEvidence[run.Key] = sweep.Exported
	out, err = e.build().RetryEnd(context.Background(), run.Key)
	must(t, err)
	if !out.UIDReleased {
		t.Fatalf("configured retry: %+v", out)
	}
}
