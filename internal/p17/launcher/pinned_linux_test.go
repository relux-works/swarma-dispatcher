//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
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
	_ = peer.SetDeadline(time.Now().Add(8 * time.Second))
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
	// Like the holder, answer every bind in sequence until the client closes.
	for {
		b, err = w.ReadFrame(peer)
		if err != nil {
			if errors.Is(err, io.EOF) && d.binds.Load() >= 1 {
				return nil
			}
			return err
		}
		req, err := wire.DecodeRequestFrame(b)
		if err != nil {
			return err
		}
		if req.Method != wire.BindMethod {
			// The end after a bound run: not answered here; the client closes.
			continue
		}
		d.binds.Add(1)
		reply := bytes.ReplaceAll(d.reply, []byte("00000000000000000000000000000001"), []byte(req.ID))
		if _, err = peer.Write(reply); err != nil {
			return err
		}
	}
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

// Claim: literal native refusals keep the record binding with exit 17, no Run, no input and no child: out-of-set (wrong-role), null-ID (busy-unattributed), wrong-ID and malformed replies after exactly ONE bind frame, the correlated retryable request_deadline_exceeded after exactly BindAttemptLimit identical bind frames; the uid stays quarantined. Positive control: the literal success binds durably with one frame.
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
			select {
			case err := <-d.done:
				must(t, err)
			case <-time.After(4 * time.Second):
				t.Fatal("native peer did not stop")
			}
			wantBinds := int32(1)
			if name == "deadline-correlated" {
				wantBinds = keymaker.BindAttemptLimit
			}
			if d.binds.Load() != wantBinds {
				t.Fatalf("bind count %d want %d", d.binds.Load(), wantBinds)
			}
		})
	}
}

// Claim (F3 with r2.7 Q1): a pre-request Keymaker connect failure creates no
// record and exits 1 (run_bind_unavailable); the lease of the SAME fixed uid
// is returned after the zero-process proof, so a later attempt with
// isolation.Fixed binds under that uid, starting from a fresh H1 with a
// different prepared_id; the failed attempt's prepared_id is never bound.
func TestConnectFresh(t *testing.T) {
	e := newEnv(t)
	e.kmDialer.Fail = errors.New("synthetic connect failure")
	l := e.build()
	_, err := l.Begin(context.Background(), "input-1")
	v, x := codes.Exit(err, 0, true)
	if !codes.Is(err, codes.BindUnavailable) || x != nil || v.Status != 1 || len(e.store.Keys()) != 0 || len(e.km.Binds()) != 0 {
		t.Fatalf("%v %+v %v", err, v, x)
	}
	if e.quar.Held(cuid) || v.Detail["lease"] != "" {
		t.Fatalf("pre-record lease not returned: held=%v detail=%v", e.quar.Held(cuid), v.Detail)
	}
	first := e.src.ValidatedIDs()
	if len(first) != 1 {
		t.Fatal(first)
	}
	e.kmDialer.Fail = nil
	run, err := l.Begin(context.Background(), "input-2")
	must(t, err)
	ids := e.src.ValidatedIDs()
	rec := e.record(run.Key)
	if len(ids) != 2 || ids[0] == ids[1] || rec.PreparedID != ids[1] || rec.PreparedID == first[0] || len(e.store.Keys()) != 1 || rec.State != runrecord.Bound || rec.ChildUID != cuid || run.ChildUID != cuid {
		t.Fatalf("fresh prepare: %v record %+v", ids, rec)
	}
	_, _ = run.End(context.Background(), 0, true, nil)
}
