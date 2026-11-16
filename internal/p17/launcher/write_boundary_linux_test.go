//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

type faultDialer struct {
	base   keymaker.Dialer
	mode   string
	armed  atomic.Bool
	writes atomic.Int32
}
type faultConn struct {
	net.Conn
	d *faultDialer
}

func (d *faultDialer) Dial(ctx context.Context) (net.Conn, error) {
	c, e := d.base.Dial(ctx)
	if e != nil {
		return nil, e
	}
	return faultConn{c, d}, nil
}
func (c faultConn) Write(p []byte) (int, error) {
	if !c.d.armed.Load() {
		return c.Conn.Write(p)
	}
	c.d.writes.Add(1)
	if c.d.mode == "partial" {
		n, _ := c.Conn.Write(p[:1])
		return n, io.ErrUnexpectedEOF
	}
	return 0, io.ErrUnexpectedEOF
}

type armWire struct {
	fakes.KeymakerWire
	d *faultDialer
}

func (w armWire) Hello(c net.Conn, b time.Duration) error {
	err := w.KeymakerWire.Hello(c, b)
	w.d.armed.Store(true)
	return err
}

type unencodableWire struct{ fakes.KeymakerWire }

func (unencodableWire) Encode(keymaker.Request) ([]byte, error) { return nil, errStoreRefused }

// Claim: local bind encoding refusal is definite run_origin_refused with no record or bind and a returned lease, beside encodable input.
func TestBindEncodingRefusal(t *testing.T) {
	e := newEnv(t)
	e.deps.KeymakerWire = unencodableWire{}
	_, err := e.build().Begin(context.Background(), "unencodable")
	if !codes.Is(err, codes.OriginRefused) || !errors.Is(err, errStoreRefused) || e.quar.Held(cuid) {
		t.Fatalf("encoding classification: %v", err)
	}
	noTrace(t, e)
	e = newEnv(t)
	_, err = e.build().Begin(context.Background(), "control")
	must(t, err)
}

// Claim: New refuses a missing shared quarantine; an explicitly shared quarantine composes.
func TestNewRequiresSharedQuarantine(t *testing.T) {
	e := newEnv(t)
	e.deps.Records = e.store
	if _, err := launcher.New(e.cfg, e.deps); err == nil {
		t.Fatal("nil quarantine accepted")
	}
	e.deps.Quarantine = e.quar
	if _, err := launcher.New(e.cfg, e.deps); err != nil {
		t.Fatalf("shared control: %v", err)
	}
}

// Claim: only never-called bind writes become unsent; one-byte and zero-reported-byte failed writes stay binding/HELD with no fresh-connection retry.
func TestBindWriteBoundary(t *testing.T) {
	for _, mode := range []string{"slow", "partial", "zero", "fast"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			if mode == "slow" {
				gated(e).create = func(*runrecord.Record) error { time.Sleep(2100 * time.Millisecond); return nil }
			}
			var d *faultDialer
			if mode == "partial" || mode == "zero" {
				d = &faultDialer{base: e.kmDialer, mode: mode}
				e.deps.Keymaker = d
				e.deps.KeymakerWire = armWire{d: d}
			}
			l := e.build()
			r, err := l.Begin(context.Background(), mode)
			keys := e.store.Keys()
			if len(keys) != 1 {
				t.Fatalf("records %v: %v", keys, err)
			}
			rec := e.record(keys[0])
			switch mode {
			case "slow":
				if !codes.Is(err, codes.BindUnavailable) || rec.State != runrecord.Unsent || e.quar.Held(cuid) || e.km.BindBytes() != 0 {
					t.Fatalf("never called: %v state=%s bytes=%d", err, rec.State, e.km.BindBytes())
				}
				old := rec.PreparedID
				g := e.records.(*gateStore)
				g.create = nil
				r, err = l.Begin(context.Background(), "retry")
				must(t, err)
				if e.record(r.Key).PreparedID == old || len(e.km.Binds()) != 1 {
					t.Fatal("unsent did not restart at fresh H1")
				}
				_, b, err := fakes.ParseBind(e.km.Binds()[0])
				must(t, err)
				if string(b.Origin) != string(fakes.ValidOrigin()) {
					t.Fatal("fresh bind origin changed")
				}
			case "partial", "zero":
				v, x := codes.Exit(err, 0, false)
				if !codes.Is(err, codes.BindUnknown) || x != nil || v.Status != 17 || rec.State != runrecord.Binding || !e.quar.Held(cuid) || d.writes.Load() != 1 || e.kmDialer.Dials() != 1 || r != nil {
					t.Fatalf("attempted write became unsent: %v state=%s writes=%d", err, rec.State, d.writes.Load())
				}
				if mode == "partial" {
					eventually(t, "one bind byte", func() bool { return e.km.BindBytes() == 1 })
				} else if e.km.BindBytes() != 0 {
					t.Fatal("zero-byte fault sent bytes")
				}
			case "fast":
				must(t, err)
				if rec.State != runrecord.Bound || r == nil {
					t.Fatal("fast control failed")
				}
			}
		})
	}
}

// Claim: a pre-commit unsent refusal leaves binding/HELD with its cause; a successful update returns the unused lease.
func TestUnsentUpdateDurability(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(map[bool]string{true: "refused", false: "accepted"}[fail], func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			g.create = func(*runrecord.Record) error { time.Sleep(1600 * time.Millisecond); return nil }
			if fail {
				g.set(refuseState(runrecord.Unsent))
			}
			_, err := e.build().Begin(context.Background(), "late")
			rec := e.record(e.store.Keys()[0])
			if fail {
				if !codes.Is(err, codes.BindUnknown) || !errors.Is(err, errStoreRefused) || rec.State != runrecord.Binding || !e.quar.Held(cuid) {
					t.Fatalf("%v %+v", err, rec)
				}
			} else if !codes.Is(err, codes.BindUnavailable) || rec.State != runrecord.Unsent || e.quar.Held(cuid) {
				t.Fatalf("control: %v %+v", err, rec)
			}
		})
	}
}

// Claim: corrupt, unreadable and unknown-version ledgers refuse every pool uid by name; a valid ledger permits a fresh uid.
func TestCorruptQuarantineHoldsPool(t *testing.T) {
	for _, mode := range []string{"corrupt", "version", "unreadable", "valid"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			must(t, e.quar.Close())
			e.quar = &sweep.Quarantine{}
			e.cfg.Installation = sweep.Established
			path := filepath.Join(e.jcfg.Directory, sweep.QuarantineName)
			data := []byte(`{"version":1,"entries":{}}`)
			if mode == "corrupt" {
				data = []byte("{")
			}
			if mode == "version" {
				data = []byte(`{"version":99,"entries":{}}`)
			}
			must(t, os.WriteFile(path, data, 0600))
			if mode == "unreadable" {
				if os.Geteuid() == 0 {
					// Root bypasses mode 0000: O_NOFOLLOW gives a deterministic open error.
					must(t, os.Remove(path))
					must(t, os.Symlink(sweep.QuarantineName, path))
				} else {
					must(t, os.Chmod(path, 0000))
				}
			}
			l := e.build()
			reports := l.Restart(context.Background())
			if mode == "valid" {
				_, err := l.Begin(context.Background(), "control")
				must(t, err)
				return
			}
			if len(reports) != 1 || reports[0].Err == nil {
				t.Fatalf("bad ledger accepted: %+v", reports)
			}
			if mode == "unreadable" {
				want := error(os.ErrPermission)
				if os.Geteuid() == 0 {
					want = syscall.ELOOP
				}
				if !errors.Is(reports[0].Err, want) {
					t.Fatalf("read-error branch not exercised: %v", reports[0].Err)
				}
			}
			for _, uid := range []uint32{cuid, cuid + 99} {
				if !e.quar.Held(uid) || e.quar.Lease(uid, "candidate") == nil {
					t.Fatalf("bad ledger did not hold uid %d", uid)
				}
			}
			_, err := l.Launcher.Begin(context.Background(), event, principalFor(cuid))
			if !codes.Is(err, codes.IsolationUnqualified) {
				t.Fatalf("Begin after bad Restart: %v", err)
			}
			if d, _ := e.srcDialer.Dials(); d != 0 || e.kmDialer.Dials() != 0 {
				t.Fatal("bad ledger allowed contact")
			}
		})
	}
}

// Claim: RetryEnd for a terminal old run cannot sweep or release its uid after a real new run leases it; an uncontested end releases.
func TestStaleEndLeavesNewHolderUntouched(t *testing.T) {
	e := newEnv(t)
	r := endReady(t, e)
	e.exportEvidence[r.Key] = sweep.Exported
	first, err := r.End(context.Background(), 0, true, nil)
	must(t, err)
	if !first.UIDReleased {
		t.Fatal("old run did not release before reuse")
	}
	current, err := e.l.Begin(context.Background(), "new-record")
	must(t, err)
	must(t, current.Spawn(e.spawnChild(778)))
	before := len(e.killer.UIDs())
	out, err := e.l.RetryEnd(context.Background(), r.Key)
	if !errors.Is(err, launcher.ErrNotBound) {
		t.Fatalf("terminal retry: %v", err)
	}
	if out.Sweep.Final.Proved() || out.UIDReleased || len(e.killer.UIDs()) != before || gone(t, e, "778") || !e.quar.Held(cuid) || e.record(current.Key).State != runrecord.Bound {
		t.Fatalf("stale end touched new uid: %+v", out)
	}
	// Separate qualified end/release positive control.
	e = newEnv(t)
	r = endReady(t, e)
	e.exportEvidence[r.Key] = sweep.Exported
	out, err = r.End(context.Background(), 0, true, nil)
	must(t, err)
	if !out.UIDReleased {
		t.Fatal("owned control did not release")
	}
}

// Claim: restart removes only the current terminal lease, not a historical record selected by lexical order; helper marks still prevent an unused return.
func TestRestartSelectsCurrentLease(t *testing.T) {
	for _, helper := range []bool{true, false} {
		t.Run(map[bool]string{true: "helper", false: "clean"}[helper], func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			e.km.BindMode = "refuse"
			l := e.build()
			_, err := l.Begin(context.Background(), "old")
			if !codes.Is(err, codes.BindRefused) || e.quar.Held(cuid) {
				t.Fatalf("old refusal: %v", err)
			}
			must(t, e.proc.Add(777, [4]uint32{0, 0, cuid, 0}, 42))
			if helper {
				g.create = func(r *runrecord.Record) error {
					must(t, e.quar.MarkHelper(cuid, r.Key))
					return e.killer.KillAll(context.Background(), cuid)
				}
			}
			_, err = l.Begin(context.Background(), "current")
			if !codes.Is(err, codes.BindRefused) || !e.quar.Held(cuid) {
				t.Fatalf("current refusal: %v", err)
			}
			must(t, e.proc.Remove(777))
			e.reopen()
			e.records = nil
			e.build().Restart(context.Background())
			if e.quar.Held(cuid) != helper {
				t.Fatalf("restart chose historical holder or lost helper: held=%t helper=%t", e.quar.Held(cuid), helper)
			}
		})
	}
	t.Run("ended", func(t *testing.T) {
		e := newEnv(t)
		n := 100
		e.deps.Nonce = func() (string, error) { n--; return fmt.Sprintf("n-%03d", n), nil }
		old := endReady(t, e)
		e.exportEvidence[old.Key] = sweep.Exported
		_, err := old.End(context.Background(), 0, true, nil)
		must(t, err)
		e.km.SetRunID("run-current")
		current, err := e.l.Begin(context.Background(), "current")
		must(t, err)
		out, err := current.End(context.Background(), 0, true, nil)
		must(t, err)
		if out.UIDReleased {
			t.Fatal("setup lacks retained current lease")
		}
		e.exportEvidence[current.Key] = sweep.Exported
		if current.Key >= old.Key {
			t.Fatal("setup is not reverse lexical order")
		}
		e.reopen()
		e.build().Restart(context.Background())
		if e.quar.Held(cuid) {
			t.Fatal("restart released a historical ended key instead of current holder")
		}
	})
}

// Claim: unsent restart returns only helper-never-invoked/zero-proof leases; binding restart holds and never sends end.
func TestUnsentRestartRule(t *testing.T) {
	for _, mode := range []string{"clean", "helper", "process", "binding"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			g := gated(e)
			g.create = func(*runrecord.Record) error { time.Sleep(1600 * time.Millisecond); return nil }
			_, err := e.build().Begin(context.Background(), "unsent")
			if !codes.Is(err, codes.BindUnavailable) {
				t.Fatal(err)
			}
			key := e.store.Keys()[0]
			if mode == "binding" {
				// Independent binding record models death before the terminal update.
				e.placement = isolation.Fixed(cuid + 1)
				g.create = nil
				e.km.SetBindMode("close")
				_, err = e.build().Begin(context.Background(), "binding")
				if !codes.Is(err, codes.BindUnknown) {
					t.Fatal(err)
				}
			}
			if mode == "helper" {
				must(t, e.quar.Hold(cuid, key))
				must(t, e.quar.MarkHelper(cuid, key))
			}
			if mode == "process" {
				must(t, e.proc.Add(778, [4]uint32{0, 0, cuid, 0}, 42))
			}
			e.reopen()
			e.records = nil
			reports := e.build().Restart(context.Background())
			if len(e.km.Ends()) != 0 {
				t.Fatal("terminal or binding record caused end")
			}
			if mode == "clean" && e.quar.Held(cuid) {
				t.Fatal("unsent restart did not return clean uid")
			}
			if (mode == "helper" || mode == "process") && !e.quar.Held(cuid) {
				t.Fatal("unsent skipped pre-record rule")
			}
			if mode == "binding" && !e.quar.Held(cuid+1) {
				t.Fatalf("binding not held: %+v", reports)
			}
		})
	}
}
