//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"context"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Claim: later New cannot replace the first missing release seams after recovery; configuring evidence before recovery permits release.
func TestConfigurationFrozen(t *testing.T) {
	for _, late := range []bool{true, false} {
		t.Run(map[bool]string{true: "late", false: "control"}[late], func(t *testing.T) {
			e := newEnv(t)
			if late {
				e.deps.WorkProductExport = nil
				e.deps.OperatorRelease = nil
			}
			run := endReady(t, e)
			e.exportEvidence[run.Key] = sweep.Exported
			if late {
				deps := e.deps
				deps.WorkProductExport = e.exportEvidence
				deps.OperatorRelease = e.operator
				_, err := launcher.New(e.cfg, deps)
				must(t, err)
			}
			out, err := run.End(context.Background(), 0, true, nil)
			must(t, err)
			if out.UIDReleased == late || e.quar.Held(cuid) != late {
				t.Fatalf("configuration replaced: %+v", out)
			}
			if late && !codes.IsUnpinned(out.Release.WorkProductExportErr) {
				t.Fatalf("first unpinned seam lost: %+v", out.Release)
			}
		})
	}
}

// Claim: New and first recovery serialize shared configuration without races; once recovered the same launcher binds normally.
func TestConcurrentConfigurationRecovery(t *testing.T) {
	e := newEnv(t)
	l := e.build()
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		for range 50 {
			if _, err := launcher.New(e.cfg, e.deps); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	close(start)
	reports := l.Restart(context.Background())
	must(t, <-done)
	if len(reports) != 0 {
		t.Fatalf("concurrent recovery: %+v", reports)
	}
	_, err := l.Launcher.Begin(context.Background(), event, principalFor(cuid))
	must(t, err)
}
