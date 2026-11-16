package keymaker

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

// Dialer opens one fresh, direct Keymaker connection. It is never inherited
// and the client never passes the connection on.
type Dialer interface {
	Dial(ctx context.Context) (net.Conn, error)
}

const (
	// BindAttemptLimit is the one fixed, finite number of j7_run_bind request
	// frames a run may write on its assigned connection: the first request
	// plus the exact retries, each of which follows a read, correlated,
	// retryable refusal. It is deliberately a constant and not a
	// configuration field: no caller can raise it. When it is reached the run
	// is HELD (run_bind_unknown).
	BindAttemptLimit = 3

	// HelloBudget is the protocol's hello deadline (pin B spec/keymaker.md
	// section 7: "Hello deadline is 2 seconds"; internal/control/server.go
	// frameBudget). Open negotiates hello eagerly inside this budget.
	HelloBudget = 2 * time.Second

	// HolderFrameReportBudget is the holder's own budget for framing and for
	// the refusal report it still writes after a request deadline (pin B
	// spec/keymaker.md section 7: "the service MUST also bound framing to 2
	// seconds"; internal/control/server.go: const frameBudget = 2 *
	// time.Second). The holder measures timeout_ms from completion of the
	// request frame and may answer request_deadline_exceeded only after it.
	HolderFrameReportBudget = 2 * time.Second

	// ReadMargin is the fixed local margin added on top of timeout_ms and
	// HolderFrameReportBudget, for scheduling and socket latency.
	ReadMargin = 500 * time.Millisecond

	// BindStartWindow is below the pinned holder's 2 s complete-frame bound
	// after hello (B e5d8f248, internal/control/server.go). It bounds START,
	// not completion: a slow or failed attempted write is still HELD.
	BindStartWindow = 1500 * time.Millisecond
	// RetryPause separates busy/rate_limited exact retries; the limit stays 3.
	RetryPause = 200 * time.Millisecond
)

// ReadBudget is the local wait for the one reply a started request is owed:
// max(timeout_ms, 2000 ms) + 2000 ms + ReadMargin. The pinned holder may
// spend 2 s in observer.Enter before reporting its deadline refusal within
// another frameBudget (B e5d8f248 internal/control/server.go). The margin is
// local slack, not a promise of remote latency; expiry is still ambiguous.
func ReadBudget(timeoutMS int64) time.Duration {
	return max(time.Duration(timeoutMS)*time.Millisecond, HolderFrameReportBudget) + HolderFrameReportBudget + ReadMargin
}

var (
	// ErrConnectionGone means the assigned connection is no longer usable.
	ErrConnectionGone = errors.New("keymaker: assigned connection gone")
	// ErrLost means the one reply a started request was owed did not arrive
	// inside ReadBudget. The channel is ambiguous and has been closed; the
	// request is never written again on it.
	ErrLost = errors.New("keymaker: reply not received within the local read budget; channel closed")
	// ErrSealed means an ended acknowledgement already closed this channel to
	// further mutation.
	ErrSealed = errors.New("keymaker: channel sealed after ended acknowledgement")
	// ErrTimeout means the per-request timeout is outside 1..2000 ms.
	ErrTimeout = errors.New("keymaker: per-request timeout outside 1..2000 ms")
	// ErrHello is before any bind request; hello is a native request that the
	// holder checks and audits; this path promises no run record and no bind.
	ErrHello = errors.New("keymaker: hello failed before any bind request; hello is a native request that the holder checks and audits; this path promises no run record and no bind")
	// ErrUnsent proves this process never called write for this bind frame.
	ErrUnsent = errors.New("keymaker: bind write never called; post-hello start window elapsed")
	// ErrUncorrelated means a response did not carry the outstanding request
	// ID (null or different). It is never retried.
	ErrUncorrelated = errors.New("keymaker: response id is null or differs from the outstanding request")
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
	// requests counts request frames written (hello excluded); tests use it
	// to prove retry identity and the attempt limit.
	requests         int
	helloCompleted   time.Time
	bindWriteStarted bool
	// Private observation seams for clock/pause tests; production uses time.
	now   func() time.Time
	pause func(time.Duration)
}

// Open dials one fresh connection and negotiates hello on it, eagerly and
// within HelloBudget. timeout is the per-request timeout and must be within
// 1..2000 ms. A nil wire is the refusing default.
//
// Open fails before any bind request; hello is a native request that the
// holder checks and audits; this path promises no run record and no bind.
// The connection is closed on failure.
func Open(ctx context.Context, d Dialer, w Wire, timeout time.Duration) (*Conn, error) {
	if timeout < time.Millisecond || timeout > 2000*time.Millisecond || !codes.RequestTimeoutOK(timeout.Milliseconds()) {
		return nil, ErrTimeout
	}
	if d == nil {
		return nil, codes.Unpinned("keymaker dialer", Pin)
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
	if err := w.Hello(c, HelloBudget); err != nil {
		_ = c.Close()
		return nil, errors.Join(ErrHello, err)
	}
	return &Conn{c: c, wire: w, timeout: timeout, helloCompleted: time.Now(), now: time.Now, pause: time.Sleep}, nil
}

// BindWriteStarted is monotone and set BEFORE the first bind write call,
// including calls that fail reporting zero bytes. It is process-local proof.
func (c *Conn) BindWriteStarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bindWriteStarted
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

// exchange writes b once and waits ReadBudget for exactly the one reply that
// request is owed. It is the only place a request frame is written, it runs
// under c.mu, and every failure closes the connection before returning: a
// second request can therefore never be written while a reply is owed. On a
// correlation failure the decoded response is still returned for diagnostics.
func (c *Conn) exchange(req Request, b []byte) (Response, error) {
	if c.gone || c.closed {
		return Response{}, ErrConnectionGone
	}
	if c.sealed {
		return Response{}, ErrSealed
	}
	fail := func(resp Response, err error) (Response, error) {
		_ = c.closeLocked()
		return resp, errors.Join(ErrConnectionGone, err)
	}
	// Every attempt carries its own request timeout inside 1..2000 ms.
	if !codes.RequestTimeoutOK(req.TimeoutMS) {
		return fail(Response{}, ErrTimeout)
	}
	if err := c.c.SetDeadline(time.Now().Add(ReadBudget(req.TimeoutMS))); err != nil {
		return fail(Response{}, err)
	}
	if req.Method == MethodBind && !c.bindWriteStarted {
		if c.now().Sub(c.helloCompleted) >= BindStartWindow {
			return fail(Response{}, ErrUnsent)
		}
		c.bindWriteStarted = true
	}
	c.requests++
	if err := c.wire.WriteFrame(c.c, b); err != nil {
		return fail(Response{}, err)
	}
	fr, err := c.wire.ReadFrame(c.c)
	if err != nil {
		if isTimeout(err) {
			// A reply is still owed on this channel: it is ambiguous.
			return fail(Response{}, errors.Join(ErrLost, err))
		}
		return fail(Response{}, err)
	}
	resp, err := c.wire.DecodeResponse(req, fr)
	if err != nil {
		return fail(Response{}, err)
	}
	if resp.ID == "" || resp.ID != req.ID {
		return fail(resp, ErrUncorrelated)
	}
	return resp, nil
}

// withNative adds the closed native refusal code and retryable bit of resp, if
// any, to a HELD refusal for operator reconciliation.
func withNative(r *codes.Refusal, resp Response) *codes.Refusal {
	if resp.Refusal == nil {
		return r
	}
	return r.With("native_code", resp.Refusal.Code).With("native_retryable", strconv.FormatBool(resp.Refusal.Retryable))
}

// EncodeBind returns the exact bytes Bind would send, for the durable record
// written before the request. Encode is deterministic.
func (c *Conn) EncodeBind(id string, req BindRequest) ([]byte, error) {
	return c.wire.Encode(Request{ID: id, TimeoutMS: c.timeout.Milliseconds(), Method: MethodBind, Bind: &req})
}

// EncodeEnd returns the exact bytes End would send, for the durable record
// written before the request.
func (c *Conn) EncodeEnd(id string, req EndRequest) ([]byte, error) {
	return c.wire.Encode(Request{ID: id, TimeoutMS: c.timeout.Milliseconds(), Method: MethodEnd, End: &req})
}

// Bind sends j7_run_bind with the given request ID (hooks r2.15, H2 steps 5 and 6).
//
// The identical bytes are written again only after a correlated retryable
// refusal was READ on this same live connection, and at most
// BindAttemptLimit frames are written in total. Every other outcome that is
// not a success or a correlated refusal in the pinned no-effect set closes
// the connection and is HELD, run_bind_unknown: a local timeout with a reply
// still owed, EOF, a null, malformed or wrong-ID response, a correlated
// refusal that is not retryable, and the attempt limit. A refusal in the
// no-effect set returns run_bind_refused with ErrRefused. The returned bytes
// are the exact request sent, for the durable record.
func (c *Conn) Bind(id string, req BindRequest) (BindResult, []byte, error) {
	if id == "" {
		return BindResult{}, nil, errors.New("keymaker: request id required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	request := Request{ID: id, TimeoutMS: c.timeout.Milliseconds(), Method: MethodBind, Bind: &req}
	b, err := c.wire.Encode(request)
	if err != nil {
		return BindResult{}, nil, err
	}
	held := func(op string, cause error, resp Response) (BindResult, []byte, error) {
		_ = c.closeLocked()
		r := codes.Refuse(codes.BindUnknown, op, cause).With("request_id", id).With("requests_written", strconv.Itoa(c.requests))
		return BindResult{}, b, withNative(r, resp)
	}
	var last Response
	for attempt := 1; ; attempt++ {
		resp, err := c.exchange(request, b)
		if err != nil {
			if last.Refusal == nil {
				last = resp
			}
			return held("bind outcome unknown on the assigned connection; run HELD", err, last)
		}
		if resp.Refusal != nil {
			last = resp
		}
		if resp.Refused {
			return BindResult{}, b, withNative(codes.Refuse(codes.BindRefused, "correlated no-effect bind refusal", ErrRefused), resp)
		}
		if resp.Refusal != nil {
			// The pinned holder closes after these refusals; never retry them.
			if resp.Refusal.Code == "generation_source_unavailable" || resp.Refusal.Code == "audit_unavailable" {
				return held("holder closes after correlated refusal; run HELD", nil, resp)
			}
			if !RetryableRefusal(resp.Refusal) {
				return held("correlated bind refusal outside the no-effect set; run HELD", nil, resp)
			}
			if attempt >= BindAttemptLimit {
				return held("bind attempt limit reached after retryable refusals; run HELD", nil, resp)
			}
			if resp.Refusal.Code == "busy" || resp.Refusal.Code == "rate_limited" {
				c.pause(RetryPause)
			}
			// The one reply owed was read, correlated and retryable: nothing
			// is in flight, so the exact same-ID, same-body retry may follow.
			continue
		}
		if resp.Bind == nil || resp.Bind.RunID == "" {
			return held("bind result without run_id; run HELD", nil, resp)
		}
		return *resp.Bind, b, nil
	}
}

// End sends j7_run_end once and checks the exact ended result for RunID.
// Anything else, including a local timeout, EOF, or a refusal, is
// run_end_unknown and closes the connection. On the exact acknowledgement the
// channel is sealed.
func (c *Conn) End(id string, req EndRequest) (EndResult, []byte, error) {
	if id == "" || req.RunID == "" {
		return EndResult{}, nil, errors.New("keymaker: request id and run_id required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	request := Request{ID: id, TimeoutMS: c.timeout.Milliseconds(), Method: MethodEnd, End: &req}
	b, err := c.wire.Encode(request)
	if err != nil {
		return EndResult{}, nil, err
	}
	return c.endLocked(request, b)
}

// EndExact replays only the durable end intent, byte for byte, including its
// original timeout. Fresh connection configuration cannot rewrite that
// intent; the local wait is derived from the persisted timeout_ms alone.
func (c *Conn) EndExact(id string, req EndRequest, body []byte) (EndResult, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body = append([]byte(nil), body...)
	request, err := c.wire.DecodeEndRequest(body)
	if err != nil || request.ID != id || request.Method != MethodEnd || request.End == nil || request.End.RunID != req.RunID {
		return EndResult{}, body, codes.Refuse(codes.EndUnknown, "durable end intent differs or is invalid", err).With("request_id", id).With("run_id", req.RunID)
	}
	return c.endLocked(request, body)
}

func (c *Conn) endLocked(request Request, b []byte) (EndResult, []byte, error) {
	unknown := func(op string, cause error, resp Response) (EndResult, []byte, error) {
		if !errors.Is(cause, ErrSealed) {
			// The end is written at most once per connection; a retry uses a
			// fresh connection and the persisted exact request.
			_ = c.closeLocked()
		}
		r := codes.Refuse(codes.EndUnknown, op, cause).With("request_id", request.ID).With("run_id", request.End.RunID)
		return EndResult{}, b, withNative(r, resp)
	}
	resp, err := c.exchange(request, b)
	if err != nil {
		return unknown("end not acknowledged", err, resp)
	}
	if resp.Refused || resp.Refusal != nil || resp.End == nil {
		return unknown("end result refused or missing", nil, resp)
	}
	res := *resp.End
	if !res.Ended || res.RunID != request.End.RunID {
		return unknown("end result not the exact ended acknowledgement", nil, resp)
	}
	c.sealed = true
	return res, b, nil
}
