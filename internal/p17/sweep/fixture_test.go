package sweep_test

import (
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
	"os"
	"testing"
)

func newQuarantine(t *testing.T, gate sweep.ReleaseGate) *sweep.Quarantine {
	t.Helper()
	q := &sweep.Quarantine{}
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	if err := q.Configure(q, gate, nil); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(d, sweep.Fresh); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}
