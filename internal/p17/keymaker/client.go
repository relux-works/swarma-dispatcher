package keymaker

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

// Dialer opens one fresh, direct Keymaker connection. It is never inherited
// and the client never passes the connection on.
type Dialer interface {
	Dial(ctx context.Context) (net.Conn, error)
}

// DefaultBindAttempts bounds same-connection retries of a lost bind response.
const DefaultBindAttempts = 3

var (
	// ErrConnectionGone means the assigned connection is no longer usable.
	ErrConnectionGone = errors.New("keymaker: assigned connection gone")
	// ErrLost means one response was not received inside its timeout while the
	// connection stayed assigned.
	ErrLost = errors.New("keymaker: response lost within timeout")
	// ErrSealed means an ended acknowledgement already closed this channel to
	// further mutation.
	ErrSealed = errors.New("keymaker: channel sealed after ended acknowledgement")
	// ErrTimeout means the per-request timeout is outside 1..2000 ms.
	ErrTimeout = errors.New("keymaker: per-request timeout outside 1..2000 ms")
)

// Conn is one assigned Keymaker connection for one run.
type Conn struct {
	mu      sync.Mutex
	c       net.Conn
	wire    Wire
	timeout time.Duration
	gone    bool
	sealed  bool
	closed  bool
	// Requests counts frames written; tests use it to prove retry identity.
	requests int
}

// Open dials one fresh connection. timeout is the per-request timeout and
// must be within 1..2000 ms. A nil wire is the refusing default.
func Open(ctx context.Context, d Dialer, w Wire, timeout time.Duration) (*Conn, error) {
	if timeout < time.Millisecond || timeout > 2000*time.Millisecond || !codes.RequestTimeoutOK(timeout.Milliseconds()) {
		return nil, ErrTimeout
	}
	if d == nil {
		return nil, errors.New("keymaker: no dialer")
	}
	if w == nil {
		w = Unpinned{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c, err := d.Dial(ctx)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("keymaker: dialer returned no connection")
	}
	return &Conn{c: c, wire: w, timeout: timeout}, nil
}

// Live reports whether the connection is still assigned and unsealed.
func (c *Conn) Live() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.gone && !c.closed && !c.sealed
}

// Requests returns the number of request frames written so far.
func (c *Conn) Requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

// Close drops the connection. It is idempotent.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}
func (c *Conn) closeLocked() error {
	if c.closed {
		return nil
	}
	c.closed = true
	c.gone = true
	return c.c.Close()
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// exchange writes b once and waits for exactly one response with its own
// timeout. A timeout leaves the connection assigned and returns ErrLost;
// any other failure, including EOF, marks the connection gone.
func (c *Conn) exchange(id string, b []byte) (Response, error) {
	if c.gone || c.closed {
		return Response{}, ErrConnectionGone
	}
	if c.sealed {
		return Response{}, ErrSealed
	}
	now := time.Now()
	if err := c.c.SetDeadline(now.Add(c.timeout)); err != nil {
		c.gone = true
		return Response{}, errors.Join(ErrConnectionGone, err)
	}
	c.requests++
	if err := c.wire.WriteFrame(c.c, b); err != nil {
		c.gone = true
		return Response{}, errors.Join(ErrConnectionGone, err)
	}
	fr, err := c.wire.ReadFrame(c.c)
	if err != nil {
		if isTimeout(err) {
			return Response{}, errors.Join(ErrLost, err)
		}
		c.gone = true
		return Response{}, errors.Join(ErrConnectionGone, err)
	}
	resp, err := c.wire.DecodeResponse(fr)
	if err != nil {
		c.gone = true
		return Response{}, errors.Join(ErrConnectionGone, err)
	}
	if resp.ID != id {
		c.gone = true
		return Response{}, errors.Join(ErrConnectionGone, errors.New("keymaker: response id mismatch"))
	}
	return resp, nil
}

// EncodeBind returns the exact bytes Bind would send, for the durable record
// written before the request. Encode is deterministic.
func (c *Conn) EncodeBind(id string, req BindRequest) ([]byte, error) {
	return c.wire.Encode(Request{ID: id, Method: MethodBind, Bind: &req})
}

// EncodeEnd returns the exact bytes End would send, for the durable record
// written before the request.
func (c *Conn) EncodeEnd(id string, req EndRequest) ([]byte, error) {
	return c.wire.Encode(Request{ID: id, Method: MethodEnd, End: &req})
}

// Bind sends j7_run_bind with the given request ID. A lost response is
// retried with the identical bytes on this same connection, at most
// attempts times. If the connection is gone the run is HELD:
// run_bind_unknown. An exact Keymaker refusal returns ErrRefused. The
// returned bytes are the exact request sent, for the durable record.
func (c *Conn) Bind(id string, req BindRequest, attempts int) (BindResult, []byte, error) {
	if attempts <= 0 {
		attempts = DefaultBindAttempts
	}
	if id == "" {
		return BindResult{}, nil, errors.New("keymaker: request id required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := c.wire.Encode(Request{ID: id, Method: MethodBind, Bind: &req})
	if err != nil {
		return BindResult{}, nil, err
	}
	var last error
	for i := 0; i < attempts; i++ {
		resp, err := c.exchange(id, b)
		if err == nil {
			res, perr := c.wire.ParseBindResult(resp.Body)
			if perr != nil {
				if errors.Is(perr, ErrRefused) {
					return BindResult{}, b, perr
				}
				c.gone = true
				return BindResult{}, b, codes.Refuse(codes.BindUnknown, "bind result undecodable; run HELD", perr).With("request_id", id)
			}
			if res.RunID == "" {
				c.gone = true
				return BindResult{}, b, codes.Refuse(codes.BindUnknown, "bind result without run_id; run HELD", nil).With("request_id", id)
			}
			return res, b, nil
		}
		last = err
		if errors.Is(err, ErrLost) {
			continue
		}
		return BindResult{}, b, codes.Refuse(codes.BindUnknown, "assigned connection gone; run HELD", err).With("request_id", id)
	}
	return BindResult{}, b, codes.Refuse(codes.BindUnknown, "bind response lost after retries; run HELD", last).With("request_id", id)
}

// End sends j7_run_end once and checks the exact ended result for RunID.
// Anything else, including a lost response, EOF, or a refusal, is
// run_end_unknown. On the exact acknowledgement the channel is sealed.
func (c *Conn) End(id string, req EndRequest) (EndResult, []byte, error) {
	if id == "" || req.RunID == "" {
		return EndResult{}, nil, errors.New("keymaker: request id and run_id required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := c.wire.Encode(Request{ID: id, Method: MethodEnd, End: &req})
	if err != nil {
		return EndResult{}, nil, err
	}
	unknown := func(op string, cause error) (EndResult, []byte, error) {
		return EndResult{}, b, codes.Refuse(codes.EndUnknown, op, cause).With("request_id", id).With("run_id", req.RunID)
	}
	resp, err := c.exchange(id, b)
	if err != nil {
		return unknown("end not acknowledged", err)
	}
	res, err := c.wire.ParseEndResult(resp.Body)
	if err != nil {
		return unknown("end result undecodable or refused", err)
	}
	if !res.Ended || res.RunID != req.RunID {
		return unknown("end result not the exact ended acknowledgement", nil)
	}
	c.sealed = true
	return res, b, nil
}
