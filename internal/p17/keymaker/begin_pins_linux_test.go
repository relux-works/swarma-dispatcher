//go:build linux && (amd64 || arm64)

package keymaker_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/frame"
	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

type countedEndpoint struct {
	net.Conn
	bytes *atomic.Int64
}

func (c countedEndpoint) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.bytes.Add(int64(n))
	return n, err
}

type countedUnixDialer struct {
	dial  keymaker.Dialer
	calls atomic.Int32
}

func (d *countedUnixDialer) Dial(ctx context.Context) (net.Conn, error) {
	d.calls.Add(1)
	return d.dial.Dial(ctx)
}

func pinBeginFixture(t *testing.T, d keymaker.Dialer) (*launcher.Launcher, *runrecord.Store, *sweep.Quarantine, *fakes.SourceScript) {
	t.Helper()
	uid := uint32(os.Getuid())
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"records", "inputs"} {
		if err := os.Mkdir(filepath.Join(state, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	q := &sweep.Quarantine{}
	// Reflection is solely a test adapter for the unchanged-production-base control.
	start := reflect.ValueOf(q).MethodByName("Start")
	args := []reflect.Value{reflect.ValueOf(state)}
	var values []reflect.Value
	if start.Type().IsVariadic() {
		facts := reflect.MakeSlice(start.Type().In(1), 1, 1)
		facts.Index(0).SetUint(1)
		values = start.CallSlice(append(args, facts))
	} else {
		values = start.Call(args)
	}
	if !values[0].IsNil() {
		t.Fatal(values[0].Interface())
	}
	t.Cleanup(func() { _ = q.Close() })
	mask := uint32(0077)
	st, err := runrecord.Initialize(journal.Config{Directory: state, Records: "records", Inputs: "inputs", DeclaredUmask: &mask, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := os.MkdirTemp(os.TempDir(), "src-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	control, stop, err := fakes.ControlDir(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	script := &fakes.SourceScript{}
	sd := &fakes.SourceDialer{Script: script, Peer: source.Credential{UID: uid, PID: 1}}
	proc, err := fakes.NewProcTree(t.TempDir(), "boot")
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Add(os.Getpid(), [4]uint32{uid, uid, uid, uid}, 1); err != nil {
		t.Fatal(err)
	}
	gens := source.StaticGenerations{uid: "1"}
	cfg := launcher.Config{StateDirectory: state, LauncherUID: uid, Station: "1", RequestTimeout: 250 * time.Millisecond, ProcRoot: proc.Root, ProcMountInfo: proc.MountInfoPath()}
	fact := reflect.ValueOf(&cfg).Elem().FieldByName("Installation")
	if fact.IsValid() {
		fact.SetUint(1)
	}
	var nonce atomic.Int32
	l, err := launcher.New(cfg, launcher.Deps{
		Source:      &source.Client{Pins: source.Pins{Root: root, ControlDir: control, SourceUID: uid, SourceGeneration: "1"}, Dialer: sd, Wire: fakes.SourceWire{}, Generations: gens, Budget: time.Second},
		Generations: gens, Keymaker: d, KeymakerWire: fakes.KeymakerWire{}, Records: st, Quarantine: q, Placement: isolation.Fixed(2001), Incarnations: proc.Incarnations(),
		Nonce: func() (string, error) { return fmt.Sprintf("nonce-%d", nonce.Add(1)), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if reports := l.Restart(context.Background()); len(reports) != 0 {
		t.Fatalf("startup: %+v", reports)
	}
	return l, st, q, script
}

// Claim: every endpoint pin fails Begin definitely with its named check, no bytes/record/retry and a returned uid; absent/refused transport remains unavailable, each beside a real valid-dialer bind.
func TestBeginEndpointPins(t *testing.T) {
	for _, check := range []string{"owner pin", "group pin", "mode pin", "symlink component", "writable ancestor", "ancestor owner", "not a socket", "peer uid pin", "identity changed after connect", "identity removed after connect", "absent socket", "refused connection"} {
		t.Run(check, func(t *testing.T) {
			for _, bad := range []bool{true, false} {
				t.Run(map[bool]string{true: "refusal", false: "valid"}[bad], func(t *testing.T) {
					d, ln := keymaker.SocketFixtureForTest(t)
					var received atomic.Int64
					accepted := make(chan struct{})
					done := make(chan struct{})
					go func() {
						defer close(done)
						c, err := ln.Accept()
						close(accepted)
						if err != nil {
							return
						}
						defer c.Close()
						_ = c.SetDeadline(time.Now().Add(2 * time.Second))
						counted := countedEndpoint{c, &received}
						for range 2 {
							b, err := frame.Read(counted)
							if err != nil {
								return
							}
							var req struct {
								ID     string `json:"id"`
								Method string `json:"method"`
							}
							if json.Unmarshal(b, &req) != nil {
								return
							}
							result := `{}`
							if req.Method != "hello" {
								result = `{"run_id":"run-1"}`
							}
							reply := []byte(fmt.Sprintf(`{"id":%q,"result":%s}`, req.ID, result))
							if frame.Write(c, reply) != nil {
								return
							}
						}
					}()
					keymaker.AfterConnectForTest(&d, func() {
						<-accepted
						if bad && check == "identity changed after connect" {
							if err := os.Rename(d.Path, d.Path+".old"); err != nil {
								t.Fatal(err)
							}
							replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: d.Path, Net: "unix"})
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() { _ = replacement.Close() })
							if err := os.Chmod(d.Path, 0600); err != nil {
								t.Fatal(err)
							}
						}
						if bad && check == "identity removed after connect" {
							if err := os.Remove(d.Path); err != nil {
								t.Fatal(err)
							}
						}
					})
					if bad {
						switch check {
						case "owner pin":
							keymaker.OwnerMismatchForTest(&d, false)
						case "group pin":
							d.SocketGID++
						case "mode pin":
							d.SocketMode = 0660
						case "symlink component":
							dir := filepath.Dir(d.Path)
							if err := os.Rename(dir, dir+"-real"); err != nil {
								t.Fatal(err)
							}
							if err := os.Symlink(dir+"-real", dir); err != nil {
								t.Fatal(err)
							}
						case "writable ancestor":
							if err := os.Chmod(filepath.Dir(d.Path), 0770); err != nil {
								t.Fatal(err)
							}
						case "ancestor owner":
							keymaker.OwnerMismatchForTest(&d, true)
						case "not a socket":
							if err := os.Remove(d.Path); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(d.Path, nil, 0600); err != nil {
								t.Fatal(err)
							}
						case "peer uid pin":
							d.PeerUID++
						case "absent socket":
							if err := os.Remove(d.Path); err != nil {
								t.Fatal(err)
							}
						case "refused connection":
							if err := ln.Close(); err != nil {
								t.Fatal(err)
							}
						}
					}
					dial := &countedUnixDialer{dial: d}
					l, st, q, src := pinBeginFixture(t, dial)
					run, err := l.Begin(context.Background(), runorigin.EventRef{RoomID: "!room:example", EventID: "$event"}, keymaker.Principal{UID: 2001, Generation: "cccccccccccccccccccccccccccccccc"})
					_ = ln.Close()
					<-done
					if bad {
						want, status := codes.BindRefused, 16
						if check == "absent socket" || check == "refused connection" {
							want, status = codes.BindUnavailable, 1
						}
						if run != nil || !codes.Is(err, want) {
							t.Fatalf("%s: run=%v err=%v", check, run, err)
						}
						v, x := codes.Exit(err, 0, false)
						if x != nil || v.Status != status {
							t.Fatalf("classification: %+v %v", v, x)
						}
						named := check
						if check == "identity removed after connect" {
							named = "identity changed after connect"
						}
						if want == codes.BindRefused && v.Detail["check"] != named {
							t.Fatalf("check name lost: %+v", v)
						}
						var keyed interface{ RecordKey() string }
						if errors.As(err, &keyed) {
							t.Fatal("pre-record pin refusal exposed record identity")
						}
						if received.Load() != 0 || len(st.Keys()) != 0 || q.Held(2001) {
							t.Fatalf("side effects: bytes=%d records=%v held=%v", received.Load(), st.Keys(), q.Held(2001))
						}
					} else {
						if err != nil || run == nil || received.Load() == 0 || !q.Held(2001) {
							t.Fatalf("valid neighbour: %v bytes=%d", err, received.Load())
						}
						if rec, ok := st.Get(run.Key); !ok || rec.State != runrecord.Bound {
							t.Fatal("valid neighbour not durably bound")
						}
						_, _ = run.End(context.Background(), 0, false, nil)
					}
					prepares, _, validates, _ := src.Counts()
					if dial.calls.Load() != 1 || prepares != 1 || validates != 1 {
						t.Fatalf("unexpected retry: dial=%d prepare=%d validate=%d", dial.calls.Load(), prepares, validates)
					}
				})
			}
		})
	}
}
