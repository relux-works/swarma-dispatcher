//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"context"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Claim: after an exact end ACK and zero-process proof, explicit no-working-copy releases the uid; an absent export entry does not.
func TestNoWorkingCopy(t *testing.T) {
	for _, c := range []struct {
		name   string
		status sweep.WorkProductExportResult
		want   bool
	}{
		{"exported", sweep.Exported, true},
		{"no-working-copy", sweep.NoWorkingCopy, true},
		{"zero", "", false},
		{"unknown", "unknown", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			run := endReady(t, e)
			if c.status != "" {
				e.exportEvidence[run.Key] = c.status
			}
			out, err := run.End(context.Background(), 0, true, nil)
			must(t, err)
			if !out.Acknowledged || !out.Release.Proved || out.UIDReleased != c.want || e.quar.Held(cuid) == c.want {
				t.Fatalf("outcome %+v", out)
			}
		})
	}
}
