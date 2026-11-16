//go:build linux

package transport

import (
	"context"
	"os"
	"testing"
	"time"
)

// ManagedPrincipalFixture prepares a real pin; only root can prepare another UID's ownership.
func ManagedPrincipalFixture(t *testing.T, uid uint32, real bool) Principal {
	t.Helper()
	d := protectedFixture(t, 0700)
	if real {
		if err := os.Chown(d.path, int(uid), -1); err != nil {
			t.Fatal(err)
		}
	}
	id := d.ids[len(d.ids)-1]
	return Principal{UID: uid, Dir: d.path, Dev: id.dev, Ino: id.ino}
}

// ManagedSocketFixture exposes the existing real serve loop only to external tests.
// observed=0 uses real kernel credentials and unmodified principalPin checks.
func ManagedSocketFixture(t *testing.T, ext Extension, observed uint32) (string, *os.File) {
	t.Helper()
	e, c := fixtureEndpoint(t)
	cfg := serverConfig{commonConfig: c}
	calls := admissionCalls{peerUID, principalPin}
	if observed != 0 {
		calls.peer = func(*os.File) (uint32, error) { return observed, nil }
		calls.pin = func(p principal) (*directory, error) { p.UID = uint32(os.Geteuid()); return principalPin(p) }
		if unmanaged(observed) {
			p := ManagedPrincipalFixture(t, observed, false)
			cfg.Callers = []principal{p}
		}
	}
	pins, err := managedCallers(cfg, ext, calls.pin)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := &shutdown{cancel: cancel}
	done := make(chan error, 1)
	go func() { done <- serveAdmitted(ctx, e, cfg, stop, ext, pins, calls) }()
	t.Cleanup(func() {
		stop.request(nil)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("serve did not stop")
		}
	})
	return descriptorPath(e.dir, endpointName), e.dir.last()
}
