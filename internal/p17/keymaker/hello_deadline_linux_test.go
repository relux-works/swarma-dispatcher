//go:build linux

package keymaker_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	wire "github.com/relux-works/swarma-keymaker/j7lifecycle"
)

// Claim: the pinned hello accepts corpus success but refuses corpus version_unsupported and wrong-ID success before any bind frame.
func TestPinnedHelloRefusals(t *testing.T) {
	corpus := vectors(t)
	for _, mode := range []string{"unsupported", "wrong-id", "hello-success"} {
		t.Run(mode, func(t *testing.T) {
			w := keymaker.Pinned{}
			d := &vectorDialer{done: make(chan error, 1)}
			d.serve = func(c net.Conn) error {
				b, err := w.ReadFrame(c)
				if err != nil {
					return err
				}
				q, err := wire.DecodeRequestFrame(b)
				if err != nil {
					return err
				}
				name := mode
				if mode == "wrong-id" {
					name = "hello-success"
				}
				reply := corpus.named(t, name)
				if mode != "wrong-id" {
					reply = bytes.ReplaceAll(reply, []byte(vectorID), []byte(q.ID))
				}
				if err := w.WriteFrame(c, reply); err != nil {
					return err
				}
				_ = c.SetReadDeadline(time.Now().Add(time.Second))
				var one [1]byte
				n, _ := c.Read(one[:])
				if n != 0 {
					return errors.New("bind sent after hello-only exchange")
				}
				return nil
			}
			c, err := keymaker.Open(context.Background(), d, w, time.Second)
			if mode == "hello-success" {
				if err != nil {
					t.Fatal(err)
				}
				_ = c.Close()
			} else if !errors.Is(err, keymaker.ErrHello) || c != nil {
				t.Fatalf("hello refusal: %v", err)
			}
			if err := <-d.done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Claim: with the default 1000 ms request timeout a deadline refusal at max(timeout,2s)+2s is read and retried, not lost; a fast reply also binds.
func TestDefaultAdmissionReadBudget(t *testing.T) {
	for _, delay := range []time.Duration{4 * time.Second, 0} {
		s := &fakes.KeymakerScript{BindMode: "deadline-then-ok", Delay: delay}
		d := &fakes.KeymakerDialer{Script: s}
		c, err := keymaker.Open(context.Background(), d, fakes.KeymakerWire{}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		r, _, err := c.Bind("req-1", bindReq)
		_ = c.Close()
		if err != nil || r.RunID != "run-1" || len(s.Binds()) != 2 || s.Overlaps() != 0 {
			t.Fatalf("delay=%v: %+v %v binds=%d", delay, r, err, len(s.Binds()))
		}
	}
}
