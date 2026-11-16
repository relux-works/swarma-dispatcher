//go:build linux

package keymaker_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/frame"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
)

// This file is self-contained on purpose: it uses only the parts of the
// client API that did not change with the in-flight rule (Open, Requests,
// End, the Wire methods), so the same file is the control that fails on the
// revision before the rule and passes with it. bindOnce hides the one
// signature difference.

type lateMsg struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	RunID  string `json:"run_id,omitempty"`
	Ended  bool   `json:"ended,omitempty"`
}

// lateWire is a minimal test spelling: one JSON object per frame.
type lateWire struct{}

func (lateWire) Hello(c net.Conn, budget time.Duration) error {
	if err := c.SetDeadline(time.Now().Add(budget)); err != nil {
		return err
	}
	b, _ := json.Marshal(lateMsg{ID: "hello", Method: "hello"})
	if err := frame.Write(c, b); err != nil {
		return err
	}
	raw, err := frame.Read(c)
	if err != nil {
		return err
	}
	var m lateMsg
	if err := json.Unmarshal(raw, &m); err != nil || m.ID != "hello" {
		return errors.New("late wire: hello")
	}
	return nil
}
func (lateWire) Encode(req keymaker.Request) ([]byte, error) {
	m := lateMsg{ID: req.ID, Method: string(req.Method)}
	if req.End != nil {
		m.RunID = req.End.RunID
	}
	return json.Marshal(m)
}
func (lateWire) WriteFrame(w io.Writer, b []byte) error { return frame.Write(w, b) }
func (lateWire) ReadFrame(r io.Reader) ([]byte, error)  { return frame.Read(r) }
func (lateWire) DecodeResponse(expected keymaker.Request, b []byte) (keymaker.Response, error) {
	var m lateMsg
	if err := json.Unmarshal(b, &m); err != nil {
		return keymaker.Response{}, err
	}
	out := keymaker.Response{ID: m.ID}
	if expected.Method == keymaker.MethodBind {
		out.Bind = &keymaker.BindResult{RunID: m.RunID}
	} else {
		out.End = &keymaker.EndResult{RunID: m.RunID, Ended: m.Ended}
	}
	return out, nil
}
func (lateWire) DecodeEndRequest(b []byte) (keymaker.Request, error) {
	var m lateMsg
	if err := json.Unmarshal(b, &m); err != nil {
		return keymaker.Request{}, err
	}
	return keymaker.Request{ID: m.ID, Method: keymaker.MethodEnd, End: &keymaker.EndRequest{RunID: m.RunID}}, nil
}

// latePeer models the holder: it reads frames strictly in sequence and
// answers every one, the bind only `late` after it was read. Bytes that
// arrive while that reply is owed are counted as an overlap; like the
// holder, it then answers the duplicate too.
type latePeer struct {
	late     time.Duration
	binds    atomic.Int32
	overlaps atomic.Int32
	done     chan struct{}
}

func (p *latePeer) Dial(context.Context) (net.Conn, error) {
	a, b, err := fakes.SocketPair()
	if err != nil {
		return nil, err
	}
	go func() {
		defer close(p.done)
		defer b.Close()
		for {
			raw, err := frame.Read(b)
			if err != nil {
				return
			}
			var m lateMsg
			if json.Unmarshal(raw, &m) != nil {
				return
			}
			switch m.Method {
			case "hello":
			case string(keymaker.MethodBind):
				p.binds.Add(1)
				_ = b.SetReadDeadline(time.Now().Add(p.late))
				var one [1]byte
				if n, _ := b.Read(one[:]); n > 0 {
					p.overlaps.Add(1)
					// Drain the rest of the duplicate frame; it is answered below.
					rest := make([]byte, 4096)
					_ = b.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
					_, _ = b.Read(rest)
					out, _ := json.Marshal(lateMsg{ID: m.ID, RunID: "run-1"})
					_ = frame.Write(b, out)
				}
				_ = b.SetReadDeadline(time.Time{})
				m.RunID = "run-1"
			case string(keymaker.MethodEnd):
				m.Ended = true
			}
			out, _ := json.Marshal(lateMsg{ID: m.ID, RunID: m.RunID, Ended: m.Ended})
			if frame.Write(b, out) != nil {
				return
			}
		}
	}()
	return a, nil
}

// bindOnce calls Bind whichever of its two signatures this revision has.
func bindOnce(c *keymaker.Conn, id string, req keymaker.BindRequest) (keymaker.BindResult, error) {
	switch f := any(c.Bind).(type) {
	case func(string, keymaker.BindRequest) (keymaker.BindResult, []byte, error):
		res, _, err := f(id, req)
		return res, err
	case func(string, keymaker.BindRequest, int) (keymaker.BindResult, []byte, error):
		res, _, err := f(id, req, 3)
		return res, err
	}
	return keymaker.BindResult{}, errors.New("unknown Bind signature")
}

// Claim (the in-flight rule, control across revisions): when the holder's
// bind success arrives 20 ms after the request's own timeout_ms, the client
// has written exactly ONE request frame, nothing reached the holder while
// its reply was owed, the run is bound, and the end on the same connection
// is acknowledged (no stale duplicate bind reply is read in its place).
func TestLateBindReply(t *testing.T) {
	const perRequest = 100 * time.Millisecond
	p := &latePeer{late: perRequest + 20*time.Millisecond, done: make(chan struct{})}
	c, err := keymaker.Open(context.Background(), p, lateWire{}, perRequest)
	if err != nil {
		t.Fatal(err)
	}
	res, err := bindOnce(c, "req-1", bindReq)
	if err != nil || res.RunID != "run-1" {
		t.Fatalf("bind: %+v %v", res, err)
	}
	if c.Requests() != 1 || p.binds.Load() != 1 || p.overlaps.Load() != 0 {
		t.Fatalf("request frames written=%d, binds seen by the holder=%d, written while a reply was owed=%d", c.Requests(), p.binds.Load(), p.overlaps.Load())
	}
	end, _, err := c.End("end-1", keymaker.EndRequest{RunID: "run-1"})
	if err != nil || !end.Ended {
		t.Fatalf("end on the same connection after a late bind reply: %+v %v", end, err)
	}
	_ = c.Close()
	<-p.done
}
