//go:build linux

package keymaker

import (
	"net"
	"os"
	"syscall"
	"testing"
)

// Test-only access keeps observation seams out of the production API.
func SocketFixtureForTest(t *testing.T) (UnixDialer, *net.UnixListener) { return socketFixture(t) }
func AfterConnectForTest(d *UnixDialer, f func())                       { d.afterConnect = f }
func OwnerMismatchForTest(d *UnixDialer, ancestor bool) {
	d.stat = func(path string) (os.FileInfo, error) {
		fi, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if (!ancestor && path == d.Path) || (ancestor && path == d.Root) {
			st := *fi.Sys().(*syscall.Stat_t)
			st.Uid = d.SocketUID + 1
			if st.Uid == 0 {
				st.Uid++
			}
			return observedInfo{fi, st}, nil
		}
		return fi, nil
	}
}
