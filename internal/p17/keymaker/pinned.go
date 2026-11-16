package keymaker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-keymaker/j7lifecycle"
)

// Pinned explicitly selects pin B. It is stateless; request context belongs to Conn.
type Pinned struct {
	// Package-private injection is used only by policy tests; callers cannot
	// replace the pinned table through configuration or peer input.
	noEffectCodes *string
}

// PinnedNoEffectCodes is deliberately empty: pin B's public registry documents
// code/retryability, not absence of durable effects (codec.go:63-70, README:49-55).
// Populate only from a reviewed, published no-effect list, never by inference.
const PinnedNoEffectCodes = ""

func noEffectRefusal(expectedID string, r j7lifecycle.Response, set string) bool {
	return r.ID == expectedID && r.ID != "" && r.Refusal != nil && !r.Refusal.Retryable && r.Refusal.Code != "" && strings.Contains("\n"+set+"\n", "\n"+r.Refusal.Code+"\n")
}

func native(req Request) (j7lifecycle.Request, error) {
	q := j7lifecycle.Request{ID: req.ID, Method: j7lifecycle.Method(req.Method), TimeoutMS: req.TimeoutMS}
	switch req.Method {
	case MethodBind:
		if req.Bind == nil || req.End != nil {
			return q, j7lifecycle.ErrRequest
		}
		b := req.Bind
		q.Bind = &j7lifecycle.BindParams{IdentityRegistrationNumber: b.IdentityRegistrationNumber, InputPrincipal: b.InputPrincipal, ParentRun: b.ParentRun}
		if b.Origin != nil {
			// Launcher v1 accepts only Source admission origins, never owner commands.
			if err := source.CheckOrigin(b.Origin); err != nil {
				return q, err
			}
			var o struct {
				Kind      string `json:"kind"`
				EventID   string `json:"event_id"`
				GrantID   string `json:"grant_id"`
				TipHash   string `json:"tip_hash"`
				Mode      string `json:"mode"`
				CommandID string `json:"command_id"`
			}
			if err := json.Unmarshal(b.Origin, &o); err != nil {
				return q, err
			}
			q.Bind.Origin = &j7lifecycle.Origin{Kind: o.Kind, EventID: o.EventID, GrantID: o.GrantID, TipHash: o.TipHash, Mode: o.Mode, CommandID: o.CommandID}
		}
	case MethodEnd:
		if req.End == nil || req.Bind != nil {
			return q, j7lifecycle.ErrRequest
		}
		q.RunID = req.End.RunID
	default:
		return q, j7lifecycle.ErrRequest
	}
	return q, nil
}

func (Pinned) Encode(req Request) ([]byte, error) {
	q, err := native(req)
	if err != nil {
		return nil, err
	}
	return j7lifecycle.EncodeRequestFrame(q)
}

// Frames are already complete, including their four-byte length prefix.
func (Pinned) WriteFrame(w io.Writer, b []byte) error {
	if len(b) < 4 {
		return j7lifecycle.ErrFrameTruncated
	}
	n, err := j7lifecycle.FrameLength([4]byte(b[:4]))
	if err != nil {
		return err
	}
	if n != len(b)-4 {
		return j7lifecycle.ErrFrameTruncated
	}
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func (Pinned) ReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	n, err := io.ReadFull(r, header[:])
	if err != nil {
		if n > 0 {
			return nil, errors.Join(ErrConnectionGone, err)
		}
		return nil, err
	}
	size, err := j7lifecycle.FrameLength(header)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 4+size)
	copy(b, header[:])
	if _, err := io.ReadFull(r, b[4:]); err != nil {
		return nil, errors.Join(ErrConnectionGone, err)
	}
	return b, nil
}
func (w Pinned) DecodeResponse(expected Request, b []byte) (Response, error) {
	q, err := native(expected)
	if err != nil {
		return Response{}, err
	}
	r, err := j7lifecycle.DecodeResponseFrame(q, b)
	if err != nil {
		return Response{}, err
	}
	out := Response{ID: r.ID}
	if r.Refusal != nil {
		// Correlation or nonretryability alone is not no-effect evidence. The
		// refusal is returned decoded: the client alone decides between the
		// exact retry (a correlated retryable code) and HELD.
		out.Refusal = &NativeRefusal{Code: r.Refusal.Code, Retryable: r.Refusal.Retryable}
		set := PinnedNoEffectCodes
		if w.noEffectCodes != nil {
			set = *w.noEffectCodes
		}
		out.Refused = noEffectRefusal(q.ID, r, set)
	} else if r.Run != nil {
		if q.Method == j7lifecycle.BindMethod {
			out.Bind = &BindResult{RunID: r.Run.RunID}
		} else {
			out.End = &EndResult{RunID: r.Run.RunID, Ended: r.Run.State == "ended"}
		}
	} else {
		return Response{}, j7lifecycle.ErrResponse
	}
	return out, nil
}

// DecodeEndRequest recovers context only from the launcher's own persisted
// request. A changed transport timeout must not change the durable wire body.
func (Pinned) DecodeEndRequest(b []byte) (Request, error) {
	q, err := j7lifecycle.DecodeRequestFrame(b)
	if err != nil {
		return Request{}, err
	}
	if q.Method != j7lifecycle.EndMethod {
		return Request{}, j7lifecycle.ErrRequest
	}
	return Request{ID: q.ID, Method: MethodEnd, TimeoutMS: q.TimeoutMS, End: &EndRequest{RunID: q.RunID}}, nil
}

// Hello negotiates the native protocol before any bind, within the given
// budget. It sends a hello frame but no bind; it creates no run/channel and
// authenticates neither the peer nor the launcher. The holder checks and audits
// hello; endpoint pins precede it.
func (w Pinned) Hello(c net.Conn, timeout time.Duration) error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	q := j7lifecycle.Request{ID: hex.EncodeToString(id[:]), Method: j7lifecycle.HelloMethod, Versions: []int64{1}}
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	b, err := j7lifecycle.EncodeRequestFrame(q)
	if err != nil {
		return err
	}
	if err = w.WriteFrame(c, b); err != nil {
		return err
	}
	b, err = w.ReadFrame(c)
	if err != nil {
		return err
	}
	r, err := j7lifecycle.DecodeResponseFrame(q, b)
	if err != nil {
		return err
	}
	if r.Refusal != nil || r.Version == nil || *r.Version != 1 {
		return j7lifecycle.ErrResponse
	}
	return nil
}
