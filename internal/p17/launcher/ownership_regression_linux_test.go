//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"context"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Claim: ownership contention before recovery is nonfatal and retryable; after the owner closes, Restart takes real ownership and Begin binds.
func TestRestartContentionRetry(t *testing.T) {
	e := newEnv(t)
	owner := e.quar
	defer owner.Close()
	e.quar = &sweep.Quarantine{}
	e.cfg.Installation = sweep.Established
	l := e.build()
	reports := l.Restart(context.Background())
	if len(reports) != 1 || !codes.Is(reports[0].Err, codes.IsolationUnqualified) || launcher.MustExit(reports[0].Err) || e.quar.OwnsDirectory(e.jcfg.Directory) {
		t.Fatalf("contention: %+v", reports)
	}
	must(t, owner.Close())
	reports = l.Restart(context.Background())
	if len(reports) != 0 || !e.quar.OwnsDirectory(e.jcfg.Directory) {
		t.Fatalf("ownership retry: %+v", reports)
	}
	_, err := l.Launcher.Begin(context.Background(), event, principalFor(cuid))
	must(t, err)
}
