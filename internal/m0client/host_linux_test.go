//go:build linux

package m0client

import (
	"os"
	"syscall"
	"testing"
)

// Claim: production's zero host uses native identities and the fixed endpoint, and rejects the unprivileged snapshot that the explicit installation model accepts.
func TestNativeHostAndFixedEndpoint(t *testing.T) {
	e := newEnv(t)
	h := host{}
	if h.realUID() != uint32(os.Getuid()) || h.effectiveUID() != uint32(os.Geteuid()) || h.endpointPath() != endpointDir {
		t.Fatal("native host defaults changed")
	}
	fd, err := syscall.Open(e.cfgPath, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	must(t, err)
	defer syscall.Close(fd)
	modeled, err := e.host.fstat(fd)
	must(t, err)
	actual, err := h.fstat(fd)
	must(t, err)
	if modeled.Uid != 0 || actual.Uid != uint32(os.Geteuid()) {
		t.Fatal("ownership boundary not modeled independently")
	}
	if _, err := e.host.loadConfig(e.cfgPath); err != nil {
		t.Fatal("modeled protected control", err)
	}
	if _, err := loadConfig(e.cfgPath); err == nil {
		t.Fatal("unprotected native config accepted")
	}
	c := e.config()
	c.host = host{}
	spec := specOf(c)
	if spec.dir != endpointDir || spec.name != endpointName {
		t.Fatal("production endpoint can be overridden by configuration")
	}
}
