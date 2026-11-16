//go:build linux

package keymaker_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	wire "github.com/relux-works/swarma-keymaker/j7lifecycle"
)

// Claim: a no-byte lost bind retries identical bytes on one assigned native connection; a started partial reply instead leaves it HELD.
func TestPinnedRetry(t *testing.T) {
	c := vectors(t)
	for _, mode := range []string{"retry", "partial"} {
		t.Run(mode, func(t *testing.T) {
			w := keymaker.Pinned{}
			d := &vectorDialer{done: make(chan error, 1)}
			d.serve = func(peer net.Conn) error {
				raw, err := w.ReadFrame(peer)
				if err != nil {
					return err
				}
				hello, err := wire.DecodeRequestFrame(raw)
				if err != nil {
					return err
				}
				response := bytes.ReplaceAll(c.named(t, "hello-success"), []byte(vectorID), []byte(hello.ID))
				if err = w.WriteFrame(peer, response); err != nil {
					return err
				}
				first, err := w.ReadFrame(peer)
				if err != nil {
					return err
				}
				if mode == "partial" {
					response = c.named(t, "bind-success")
					if _, err = peer.Write(response[:5]); err != nil {
						return err
					}
					var one [1]byte
					n, err := peer.Read(one[:])
					if n != 0 || err == nil {
						return errors.New("partial response caused a second request")
					}
					return nil
				}
				second, err := w.ReadFrame(peer)
				if err != nil {
					return err
				}
				if !bytes.Equal(first, second) {
					return errors.New("retry bytes changed")
				}
				return w.WriteFrame(peer, c.named(t, "bind-success"))
			}
			conn, err := keymaker.Open(context.Background(), d, w, 50*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = conn.Bind(vectorID, bindReq, 3)
			if mode == "retry" {
				if err != nil || conn.Requests() != 2 {
					t.Fatalf("requests %d: %v", conn.Requests(), err)
				}
			} else if !codes.Is(err, codes.BindUnknown) || conn.Live() || conn.Requests() != 1 {
				t.Fatalf("partial: live=%v requests=%d err=%v", conn.Live(), conn.Requests(), err)
			}
			if d.dials != 1 {
				t.Fatal("client reconnected")
			}
			_ = conn.Close()
			select {
			case err := <-d.done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("retry peer did not stop")
			}
		})
	}
}

// Claim: native typed request guards reject invalid IDs, timeouts, registration, principal and origins; adjacent valid opaque-generation requests encode deterministically.
func TestPinnedRequestGuards(t *testing.T) {
	w := keymaker.Pinned{}
	base := func() keymaker.Request {
		b := bindReq
		return keymaker.Request{ID: vectorID, TimeoutMS: 2000, Method: keymaker.MethodBind, Bind: &b}
	}
	for _, generation := range []string{"input-generation-1", "α\n<>&", "e\u0301"} {
		q := base()
		q.Bind.InputPrincipal.Generation = generation
		b, err := w.Encode(q)
		if err != nil {
			t.Fatal(err)
		}
		again, e := w.Encode(q)
		if e != nil || !bytes.Equal(b, again) {
			t.Fatal("nondeterministic encoding")
		}
		decoded, e := wire.DecodeRequestFrame(b)
		if e != nil || decoded.Bind.InputPrincipal.Generation != generation {
			t.Fatalf("generation changed: %v", e)
		}
	}
	for _, mut := range []func(*keymaker.Request){
		func(q *keymaker.Request) { q.ID = "not-an-id" },
		func(q *keymaker.Request) { q.TimeoutMS = 0 },
		func(q *keymaker.Request) { q.TimeoutMS = 2001 },
		func(q *keymaker.Request) { q.Method = "j7_run_rebind" },
		func(q *keymaker.Request) { q.Bind.IdentityRegistrationNumber = 0 },
		func(q *keymaker.Request) { q.Bind.IdentityRegistrationNumber = uint64(wire.MaxInteger) + 1 },
		func(q *keymaker.Request) { q.Bind.InputPrincipal.UID = 0 },
		func(q *keymaker.Request) { q.Bind.InputPrincipal.Generation = "" },
		func(q *keymaker.Request) {
			q.Bind.Origin = []byte(strings.Replace(string(bindReq.Origin), `"grant_id":`, `"forged_id":`, 1))
		},
		func(q *keymaker.Request) { q.Bind.ParentRun = vectorRun },
	} {
		q := base()
		mut(&q)
		if b, err := w.Encode(q); err == nil || len(b) != 0 {
			t.Fatal("invalid request encoded")
		}
	}
}
