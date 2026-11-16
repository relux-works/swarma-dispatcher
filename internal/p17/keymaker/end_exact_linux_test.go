//go:build linux

package keymaker_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	wire "github.com/relux-works/swarma-keymaker/j7lifecycle"
)

// Claim: a fresh connection with a different timeout replays the exact persisted end frame, including its original wire timeout (the stored frame says 2000 ms, the connection 100 ms; sent == stored).
func TestPinnedEndExact(t *testing.T) {
	c := vectors(t)
	stored := c.named(t, "end")
	conn, _ := openVectorAt(t, c, c.named(t, "end-success"), stored, 100*time.Millisecond)
	res, sent, err := conn.EndExact(vectorID, keymaker.EndRequest{RunID: vectorRun}, stored)
	if err != nil || !res.Ended || res.RunID != vectorRun || !bytes.Equal(stored, sent) || conn.Requests() != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

// Claim: wrong-ID, wrong-run, wrong-method, truncated or malformed persisted end intents refuse run_end_unknown before any request frame is written; the positive control in the same loop ("exact") replays the unmodified persisted intent on the same kind of connection and is acknowledged.
func TestPinnedEndIntentRefused(t *testing.T) {
	c := vectors(t)
	for _, mode := range []string{"exact", "id", "run", "method", "truncated", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			w := keymaker.Pinned{}
			var frames atomic.Int32
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
				if err = w.WriteFrame(peer, bytes.ReplaceAll(c.named(t, "hello-success"), []byte(vectorID), []byte(hello.ID))); err != nil {
					return err
				}
				for {
					raw, err = w.ReadFrame(peer)
					if err != nil {
						if errors.Is(err, io.EOF) {
							return nil
						}
						return err
					}
					frames.Add(1)
					if !bytes.Equal(raw, c.named(t, "end")) {
						return errors.New("a frame other than the exact persisted end was written")
					}
					if err = w.WriteFrame(peer, c.named(t, "end-success")); err != nil {
						return err
					}
				}
			}
			conn, err := keymaker.Open(context.Background(), d, w, 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			body := c.named(t, "end")
			switch mode {
			case "id":
				body = bytes.ReplaceAll(body, []byte(vectorID), []byte(strings.Repeat("2", 32)))
			case "run":
				body = bytes.ReplaceAll(body, []byte(vectorRun), []byte(strings.Repeat("2", 32)))
			case "method":
				body = c.named(t, "admission-bind")
			case "truncated":
				body = body[:len(body)-1]
			case "malformed":
				body = frameBytes(`{"params":`)
			}
			res, _, err := conn.EndExact(vectorID, keymaker.EndRequest{RunID: vectorRun}, body)
			requests := conn.Requests()
			_ = conn.Close()
			select {
			case perr := <-d.done:
				if perr != nil {
					t.Fatal(perr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("peer did not stop")
			}
			if mode == "exact" {
				if err != nil || !res.Ended || res.RunID != vectorRun || requests != 1 || frames.Load() != 1 {
					t.Fatalf("exact replay: %+v %v requests=%d", res, err, requests)
				}
				return
			}
			if !codes.Is(err, codes.EndUnknown) || requests != 0 || frames.Load() != 0 {
				t.Fatalf("intent sent: %v requests=%d frames=%d", err, requests, frames.Load())
			}
		})
	}
}
