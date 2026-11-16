package sweep

import (
	"testing"
	"time"
)

// Claim: Configure cannot return while the ownership mutex is held; after unlock the same valid configuration succeeds.
func TestConfigureUsesOwnershipMutex(t *testing.T) {
	q := privateQuarantine(t, ReleaseGate{}, nil)
	entered := make(chan struct{})
	done := make(chan error, 1)
	q.mu.Lock()
	go func() { close(entered); done <- q.Configure(q, ReleaseGate{}, nil) }()
	<-entered
	select {
	case err := <-done:
		q.mu.Unlock()
		t.Fatalf("configuration bypassed ownership mutex: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	q.mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("configuration did not proceed after unlock")
	}
}
