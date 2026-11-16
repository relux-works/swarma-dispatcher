//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Claim: fatal quarantine errors survive Close and every readiness refusal without further contact; an ordinary ready launcher binds without exit duty.
func TestFatalReadinessPreservesDuty(t *testing.T) {
	e := newEnv(t)
	l := e.build()
	if reports := l.Restart(context.Background()); len(reports) != 0 {
		t.Fatalf("startup: %+v", reports)
	}
	path := filepath.Join(e.jcfg.Directory, sweep.QuarantineName)
	must(t, os.Remove(path))
	must(t, os.Mkdir(path, 0700))
	_, err := l.Launcher.Begin(context.Background(), event, principalFor(cuid))
	if !launcher.MustExit(err) || !errors.Is(err, sweep.ErrNotDurable) {
		t.Fatalf("persistence failure: %v", err)
	}
	must(t, e.quar.Close())
	for _, retry := range []bool{false, true} {
		if retry {
			_, err = l.RetryEnd(context.Background(), "unread-key")
		} else {
			_, err = l.Launcher.Begin(context.Background(), event, principalFor(cuid))
		}
		if !launcher.MustExit(err) || !errors.Is(err, sweep.ErrNotDurable) || !codes.Is(err, codes.IsolationUnqualified) {
			t.Fatalf("fatal readiness lost: %v", err)
		}
	}
	if n, _ := e.srcDialer.Dials(); n != 0 || e.kmDialer.Dials() != 0 {
		t.Fatal("fatal path contacted a peer")
	}
	e = newEnv(t)
	_, err = e.build().Begin(context.Background(), "control")
	must(t, err)
	if launcher.MustExit(err) {
		t.Fatal("ordinary control is fatal")
	}
}

// Claim: fatal recovery stops after its first failed lease return and retains exit duty/readiness closure; successful recovery returns both leases.
func TestFatalRecoveryStops(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(map[bool]string{true: "failed", false: "control"}[fail], func(t *testing.T) {
			e := newEnv(t)
			l := e.build()
			must(t, e.quar.Lease(cuid, "first"))
			must(t, e.quar.Lease(cuid+1, "second"))
			if fail {
				path := filepath.Join(e.jcfg.Directory, sweep.QuarantineName)
				must(t, os.Remove(path))
				must(t, os.Mkdir(path, 0700))
			}
			reports := l.Restart(context.Background())
			if len(reports) != 2 {
				t.Fatalf("recovery did not stop at first fatal error: %+v", reports)
			}
			if fail {
				if !launcher.MustExit(reports[0].Err) || !launcher.MustExit(reports[1].Err) {
					t.Fatalf("recovery duty lost: %+v", reports)
				}
				_, err := l.Launcher.Begin(context.Background(), event, principalFor(cuid))
				if !launcher.MustExit(err) {
					t.Fatalf("later readiness lost duty: %v", err)
				}
				if n, _ := e.srcDialer.Dials(); n != 0 || e.kmDialer.Dials() != 0 {
					t.Fatal("failed recovery contacted a peer")
				}
			} else if !reports[0].UIDReleased || !reports[1].UIDReleased || e.quar.Held(cuid) || e.quar.Held(cuid+1) {
				t.Fatalf("successful recovery: %+v", reports)
			}
		})
	}
}
