//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	wire "github.com/relux-works/swarma-keymaker/j7lifecycle"
)

type nativeDialer struct {
	reply []byte
	binds atomic.Int32
	done  chan error
}

func (d *nativeDialer) Dial(context.Context) (net.Conn, error) {
	a, b, err := fakes.SocketPair()
	if err != nil {
		return nil, err
	}
	go func() { defer b.Close(); d.done <- d.serve(b) }()
	return a, nil
}
func (d *nativeDialer) serve(peer net.Conn) error {
	_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
	w := keymaker.Pinned{}
	b, err := w.ReadFrame(peer)
	if err != nil {
		return err
	}
	hello, err := wire.DecodeRequestFrame(b)
	if err != nil {
		return err
	}
	version := int64(1)
	b, err = wire.EncodeResponseFrame(hello, wire.Response{ID: hello.ID, Version: &version})
	if err != nil {
		return err
	}
	if err = w.WriteFrame(peer, b); err != nil {
		return err
	}
	b, err = w.ReadFrame(peer)
	if err != nil {
		return err
	}
	req, err := wire.DecodeRequestFrame(b)
	if err != nil || req.Method != wire.BindMethod {
		return errors.New("native bind missing")
	}
	d.binds.Add(1)
	reply := bytes.ReplaceAll(d.reply, []byte("00000000000000000000000000000001"), []byte(req.ID))
	if _, err = peer.Write(reply); err != nil {
		return err
	}
	// Wait for close after a refusal. No second bind may appear.
	if bytes.Contains(reply, []byte(`"error"`)) {
		b, err = w.ReadFrame(peer)
		if err == nil {
			next, e := wire.DecodeRequestFrame(b)
			if e != nil || next.Method == wire.BindMethod {
				d.binds.Add(1)
				return errors.New("second bind after refusal")
			}
		}
	}
	return nil
}
func nativeVector(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../keymaker/testdata/vectors.json")
	must(t, err)
	var corpus struct {
		Cases []struct {
			Name  string
			Frame string `json:"frame_hex"`
		}
	}
	must(t, json.Unmarshal(b, &corpus))
	for _, v := range corpus.Cases {
		if v.Name == name {
			b, err := hex.DecodeString(v.Frame)
			must(t, err)
			return b
		}
	}
	t.Fatal("missing corpus vector")
	return nil
}

// Claim: native retryable/out-of-set/null/malformed/wrong-ID refusals stay binding, fenced and exit 17 after one bind; literal success binds durably.
func TestNativeHeld(t *testing.T) {
	for _, name := range []string{"bind-success", "wrong-role", "deadline-correlated", "busy-unattributed", "wrong-response-id", "noncanonical-space"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			d := &nativeDialer{reply: nativeVector(t, name), done: make(chan error, 1)}
			e.deps.Keymaker = d
			e.deps.KeymakerWire = keymaker.Pinned{}
			var nonce int
			e.deps.Nonce = func() (string, error) { nonce++; return fmt.Sprintf("%032x", nonce), nil }
			l := e.build()
			run, err := l.Begin(context.Background(), "input-1")
			keys := e.store.Keys()
			if len(keys) != 1 {
				t.Fatalf("records %v: %v", keys, err)
			}
			rec := e.record(keys[0])
			if name == "bind-success" {
				must(t, err)
				if run == nil || rec.State != runrecord.Bound {
					t.Fatalf("%+v", rec)
				}
				_, _ = run.End(context.Background(), 0, true, nil)
			} else {
				v, x := codes.Exit(err, 0, true)
				if !codes.Is(err, codes.BindUnknown) || x != nil || v.Status != 17 || rec.State != runrecord.Binding || !e.quar.Held(cuid) {
					t.Fatalf("state %s err %v exit %+v %v", rec.State, err, v, x)
				}
				if rec.InputReleased || rec.Child != nil || run != nil {
					t.Fatal("input or child released after refusal")
				}
			}
			if d.binds.Load() != 1 {
				t.Fatalf("bind count %d", d.binds.Load())
			}
			select {
			case err := <-d.done:
				must(t, err)
			case <-time.After(4 * time.Second):
				t.Fatal("native peer did not stop")
			}
		})
	}
}

// Claim: a pre-request connect failure creates no record, exits 1, and a later attempt starts fresh H1 with a different prepare and nonce.
func TestConnectFresh(t *testing.T) {
	e := newEnv(t)
	e.placement = &seqPlacement{next: cuid}
	e.kmDialer.Fail = errors.New("synthetic connect failure")
	l := e.build()
	_, err := l.Begin(context.Background(), "input-1")
	v, x := codes.Exit(err, 0, true)
	if !codes.Is(err, codes.BindUnavailable) || x != nil || v.Status != 1 || len(e.store.Keys()) != 0 || len(e.km.Binds()) != 0 {
		t.Fatalf("%v %+v %v", err, v, x)
	}
	first := e.src.ValidatedIDs()
	if len(first) != 1 {
		t.Fatal(first)
	}
	e.kmDialer.Fail = nil
	run, err := l.Begin(context.Background(), "input-2")
	must(t, err)
	ids := e.src.ValidatedIDs()
	if len(ids) != 2 || ids[0] == ids[1] || e.record(run.Key).PreparedID != ids[1] || len(e.store.Keys()) != 1 || e.record(run.Key).State != runrecord.Bound {
		t.Fatalf("fresh prepare: %v", ids)
	}
	if !strings.HasPrefix(run.Key, "n-") {
		t.Fatal("fixture nonce missing")
	}
	_, _ = run.End(context.Background(), 0, true, nil)
}
