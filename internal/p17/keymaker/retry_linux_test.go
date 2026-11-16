//go:build linux

package keymaker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	wire "github.com/relux-works/swarma-keymaker/j7lifecycle"
)

// quiet holds a reply back for d and fails if the client writes any byte
// meanwhile: one request is in flight per connection.
func quiet(peer net.Conn, d time.Duration) error {
	if err := peer.SetReadDeadline(time.Now().Add(d)); err != nil {
		return err
	}
	var one [1]byte
	n, err := peer.Read(one[:])
	var ne net.Error
	if n != 0 || !errors.As(err, &ne) || !ne.Timeout() {
		return errors.New("request written while a reply was owed")
	}
	return peer.SetReadDeadline(time.Now().Add(4 * time.Second))
}

// Claim (pinned codec, the late-reply holder): "retry": the literal
// correlated request_deadline_exceeded vector, sent 20 ms after timeout_ms,
// is read and only then the identical bind frame is written again and binds
// (two frames, never overlapping); "late-success": a literal bind success
// sent 20 ms after timeout_ms binds with ONE frame and the end on the same
// connection is acknowledged; "partial": a started partial reply is HELD
// after one frame with the connection closed.
func TestPinnedRetry(t *testing.T) {
	c := vectors(t)
	const perRequest = 50 * time.Millisecond
	endID := strings.Repeat("2", 32)
	for _, mode := range []string{"retry", "late-success", "partial"} {
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
				q, err := wire.DecodeRequestFrame(first)
				if err != nil || q.Method != wire.BindMethod || q.TimeoutMS != perRequest.Milliseconds() {
					return errors.New("first frame is not the bind with its own timeout")
				}
				switch mode {
				case "partial":
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
				case "late-success":
					if err = quiet(peer, perRequest+20*time.Millisecond); err != nil {
						return err
					}
					if err = w.WriteFrame(peer, c.named(t, "bind-success")); err != nil {
						return err
					}
					end, err := w.ReadFrame(peer)
					if err != nil {
						return err
					}
					eq, err := wire.DecodeRequestFrame(end)
					if err != nil || eq.Method != wire.EndMethod || eq.ID != endID {
						return errors.New("the frame after the late success is not the end")
					}
					return w.WriteFrame(peer, bytes.ReplaceAll(c.named(t, "end-success"), []byte(vectorID), []byte(endID)))
				}
				if err = quiet(peer, perRequest+20*time.Millisecond); err != nil {
					return err
				}
				if err = w.WriteFrame(peer, c.named(t, "deadline-correlated")); err != nil {
					return err
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
			conn, err := keymaker.Open(context.Background(), d, w, perRequest)
			if err != nil {
				t.Fatal(err)
			}
			res, _, err := conn.Bind(vectorID, bindReq)
			switch mode {
			case "retry":
				if err != nil || res.RunID != vectorRun || conn.Requests() != 2 {
					t.Fatalf("requests %d: %v", conn.Requests(), err)
				}
			case "late-success":
				if err != nil || res.RunID != vectorRun || conn.Requests() != 1 {
					t.Fatalf("requests %d: %v", conn.Requests(), err)
				}
				end, _, err := conn.End(endID, keymaker.EndRequest{RunID: vectorRun})
				if err != nil || !end.Ended || conn.Requests() != 2 {
					t.Fatalf("end after a late bind success: %+v %v", end, err)
				}
			default:
				if !codes.Is(err, codes.BindUnknown) || conn.Live() || conn.Requests() != 1 {
					t.Fatalf("partial: live=%v requests=%d err=%v", conn.Live(), conn.Requests(), err)
				}
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
			case <-time.After(5 * time.Second):
				t.Fatal("retry peer did not stop")
			}
		})
	}
}

// Owner-command origin of the corpus vector owner-grammar-only, as the
// launcher would receive it: the six-member object with only command_id set.
func ownerOrigin() []byte {
	return []byte(`{"command_id":"` + strings.Repeat("d", 64) + `","event_id":"","grant_id":"","kind":"owner_command","mode":"","tip_hash":""}`)
}

// Claim (launcher v1 policy, owner command): Pinned.Encode of a bind whose
// origin is the corpus owner_command origin returns an error and zero bytes,
// although the pinned codec itself accepts that vector; beside it the
// admission origin encodes to the literal corpus admission-bind frame.
func TestPinnedOwnerOriginRefused(t *testing.T) {
	c := vectors(t)
	w := keymaker.Pinned{}
	// The fixture origin is exactly the origin member of the corpus vector.
	var frame struct {
		Params struct {
			Origin json.RawMessage `json:"origin"`
		} `json:"params"`
	}
	if err := json.Unmarshal(c.named(t, "owner-grammar-only")[4:], &frame); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame.Params.Origin, ownerOrigin()) {
		t.Fatalf("fixture differs from the corpus owner origin: %s", frame.Params.Origin)
	}
	if _, err := wire.DecodeRequestFrame(c.named(t, "owner-grammar-only")); err != nil {
		t.Fatalf("the pinned codec no longer accepts the owner grammar: %v", err)
	}
	b := bindReq
	b.Origin = ownerOrigin()
	out, err := w.Encode(keymaker.Request{ID: vectorID, TimeoutMS: 2000, Method: keymaker.MethodBind, Bind: &b})
	if err == nil || len(out) != 0 {
		t.Fatalf("owner_command origin encoded: %d bytes, %v", len(out), err)
	}
	// Positive control: the admission origin.
	ok := bindReq
	out, err = w.Encode(keymaker.Request{ID: vectorID, TimeoutMS: 2000, Method: keymaker.MethodBind, Bind: &ok})
	if err != nil || !bytes.Equal(out, c.named(t, "admission-bind")) {
		t.Fatalf("admission control: %v", err)
	}
}

// Claim: native typed request guards reject invalid IDs, timeouts, a foreign method, registration, principal and a forged origin member; a bind that carries both an origin and a parent_run is refused by the PINNED CODEC itself (it is a codec refusal, not a launcher policy); adjacent valid opaque-generation requests encode deterministically.
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
