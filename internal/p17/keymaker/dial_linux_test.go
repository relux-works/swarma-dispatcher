//go:build linux

package keymaker

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

type observedInfo struct {
	os.FileInfo
	st syscall.Stat_t
}

func (i observedInfo) Sys() any { return &i.st }

func socketFixture(t *testing.T) (UnixDialer, *net.UnixListener) {
	t.Helper()
	root, err := os.MkdirTemp(os.TempDir(), "sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "control")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "holder.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = ln.Close() })
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	return UnixDialer{Root: root, Path: path, SocketUID: uint32(os.Getuid()), SocketGID: uint32(os.Getgid()), SocketMode: 0600, PeerUID: uint32(os.Getuid())}, ln
}

// Claim: every socket/path/peer/identity mismatch is a named definite refusal with zero bytes sent, beside a valid fresh-connection control.
func TestUnixDialerPins(t *testing.T) {
	for _, check := range []string{"owner pin", "group pin", "mode pin", "symlink component", "writable ancestor", "not a socket", "peer uid pin", "identity changed after connect"} {
		t.Run(strings.ReplaceAll(check, " ", "-"), func(t *testing.T) {
			for _, bad := range []bool{true, false} {
				d, ln := socketFixture(t)
				if bad {
					switch check {
					case "owner pin":
						d.stat = func(path string) (os.FileInfo, error) {
							fi, err := os.Lstat(path)
							if err != nil {
								return nil, err
							}
							if path == d.Path {
								st := *fi.Sys().(*syscall.Stat_t)
								st.Uid++
								return observedInfo{fi, st}, nil
							}
							return fi, nil
						}
					case "group pin":
						d.SocketGID++
					case "mode pin":
						d.SocketMode = 0660
					case "symlink component":
						dir := filepath.Dir(d.Path)
						real := dir + "-real"
						if err := os.Rename(dir, real); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(real, dir); err != nil {
							t.Fatal(err)
						}
					case "writable ancestor":
						if err := os.Chmod(filepath.Dir(d.Path), 0770); err != nil {
							t.Fatal(err)
						}
					case "not a socket":
						if err := os.Remove(d.Path); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(d.Path, nil, 0600); err != nil {
							t.Fatal(err)
						}
					case "peer uid pin":
						d.peer = func(*net.UnixConn) (uint32, error) { return d.PeerUID + 1, nil }
					case "identity changed after connect":
						d.afterConnect = func() {
							if err := os.Rename(d.Path, d.Path+".old"); err != nil {
								t.Error(err)
								return
							}
							replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: d.Path, Net: "unix"})
							if err != nil {
								t.Error(err)
								return
							}
							t.Cleanup(func() { _ = replacement.Close() })
							if err := os.Chmod(d.Path, 0600); err != nil {
								t.Error(err)
							}
						}
					}
				}
				_ = ln.SetDeadline(time.Now().Add(250 * time.Millisecond))
				read := make(chan int, 1)
				go func() {
					c, err := ln.Accept()
					if err != nil {
						read <- 0
						return
					}
					defer c.Close()
					_ = c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
					var b [1]byte
					n, _ := c.Read(b[:])
					read <- n
				}()
				c, err := d.Dial(context.Background())
				if c != nil {
					_ = c.Close()
				}
				if bad {
					if err == nil || !codes.Is(err, codes.BindRefused) || !strings.Contains(err.Error(), check) {
						t.Fatalf("%s: %v", check, err)
					}
					if v, x := codes.Exit(err, 0, false); x != nil || v.Status != 16 {
						t.Fatalf("not definite: %+v %v", v, x)
					}
				} else if err != nil {
					t.Fatalf("valid neighbour: %v", err)
				}
				if n := <-read; n != 0 {
					t.Fatalf("sent %d bytes before checks", n)
				}
			}
		})
	}
}

// Claim: every successful Dial creates a different connection, never a retained channel.
func TestUnixDialerFreshConnections(t *testing.T) {
	d, ln := socketFixture(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	a, err := d.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := d.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a == b {
		t.Fatal("dialer reused connection")
	}
	<-done
}
