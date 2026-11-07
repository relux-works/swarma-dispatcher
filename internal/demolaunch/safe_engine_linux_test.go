//go:build linux

package demolaunch

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// Ordinary fixtures never receive the production runner, even during load.
func testEngine(t *testing.T, ctx context.Context, s *stateStore, a []account, c serverConfig) (*engine, error) {
	t.Helper()
	return newEngineWithRunner(ctx, s, a, c, func(string) *exec.Cmd {
		t.Error("unexpected fixture runner call")
		return &exec.Cmd{} // Start fails without resolving or executing any path.
	})
}

// Inspect production defaults without issuing any operation on this engine.
func TestProductionDefaults(t *testing.T) {
	h := fixture(t, "good")
	e, err := newEngine(h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{commonConfig: commonConfig{ServiceUID: 1234, ServiceGID: 1235}})
	must(t, err)
	if reflect.ValueOf(e.run).Pointer() != reflect.ValueOf(fixedRunner).Pointer() || e.receiptOwner != 0 || e.receiptGroup != 0 || e.stopWait != 45*time.Second || e.receiptWait != 10*time.Second {
		t.Fatal("production defaults changed")
	}
}
