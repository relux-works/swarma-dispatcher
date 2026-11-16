//go:build linux && (amd64 || arm64)

package runrecord_test

import (
	"errors"
	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
	"sync"
	"testing"
)

var stateOwners sync.Map

func startOwner(t *testing.T, d string) {
	t.Helper()
	q := &sweep.Quarantine{}
	must(t, q.Start(d, sweep.Fresh))
	stateOwners.Store(d, q)
	t.Cleanup(func() { _ = q.Close(); stateOwners.Delete(d) })
}
func ownerFor(c journal.Config) runrecord.StateOwner {
	q, _ := stateOwners.Load(c.Directory)
	return q.(*sweep.Quarantine)
}

// Claim: Open refuses before taking a journal lock without prior launcher ownership; the same history opens with its live owner token.
func TestOpenRequiresLifetimeOwner(t *testing.T) {
	c, st := open(t)
	must(t, st.Create(base("owned")))
	must(t, st.Close())
	if _, err := runrecord.Open(c); !errors.Is(err, runrecord.ErrConfig) {
		t.Fatalf("unowned open: %v", err)
	}
	restored, err := runrecord.Open(c, ownerFor(c))
	must(t, err)
	defer restored.Close()
}
