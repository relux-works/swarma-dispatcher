package keymaker

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

type observedConn struct {
	net.Conn
	deadline time.Time
}

func (c *observedConn) SetDeadline(t time.Time) error { c.deadline = t; return nil }
func (c *observedConn) Close() error                  { return nil }

type observedDialer struct{ c *observedConn }

func (d observedDialer) Dial(context.Context) (net.Conn, error) { return d.c, nil }

type policyWire struct {
	code          string
	writes, reads int
	lost          bool
}

func (*policyWire) Hello(net.Conn, time.Duration) error { return nil }
func (*policyWire) Encode(Request) ([]byte, error)      { return []byte("identical"), nil }
func (w *policyWire) WriteFrame(io.Writer, []byte) error {
	w.writes++
	if w.lost && w.writes > 1 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func (w *policyWire) ReadFrame(io.Reader) ([]byte, error) { w.reads++; return nil, nil }
func (w *policyWire) DecodeResponse(r Request, _ []byte) (Response, error) {
	return Response{ID: r.ID, Refusal: &NativeRefusal{Code: w.code, Retryable: true}}, nil
}
func (*policyWire) DecodeEndRequest([]byte) (Request, error) { return Request{}, errors.New("unused") }

// Claim: closing refusals hold immediately; busy/rate-limited retries pause twice, preserve the limit, and retain native detail after transport loss.
func TestClosingRefusalsAndRetryPause(t *testing.T) {
	for _, code := range []string{"generation_source_unavailable", "audit_unavailable", "busy", "rate_limited", "request_deadline_exceeded"} {
		for _, lost := range []bool{false, true} {
			t.Run(code+map[bool]string{false: "", true: "-lost"}[lost], func(t *testing.T) {
				w := &policyWire{code: code, lost: lost}
				c, err := Open(context.Background(), observedDialer{&observedConn{}}, w, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				pauses := 0
				c.pause = func(d time.Duration) {
					if d != RetryPause {
						t.Errorf("pause %v", d)
					}
					pauses++
				}
				_, _, err = c.Bind("id", BindRequest{})
				var r *codes.Refusal
				if !codes.Is(err, codes.BindUnknown) || !errors.As(err, &r) || r.Detail["native_code"] != code || r.Detail["native_retryable"] != "true" {
					t.Fatalf("native detail lost: %v", err)
				}
				writes, wantPauses := 3, 0
				if code == "busy" || code == "rate_limited" {
					wantPauses = 2
				}
				if lost {
					writes = 2
					if wantPauses > 0 {
						wantPauses = 1
					}
				}
				if code == "generation_source_unavailable" || code == "audit_unavailable" {
					writes = 1
					wantPauses = 0
				}
				if w.writes != writes || pauses != wantPauses {
					t.Fatalf("writes=%d want=%d pauses=%d want=%d", w.writes, writes, pauses, wantPauses)
				}
			})
		}
	}
}

// Claim: the guard uses the hello completion instant and never calls WriteFrame after 1500 ms; a just-inside neighbour starts normally.
func TestBindGuardClock(t *testing.T) {
	for _, late := range []bool{true, false} {
		w := &policyWire{code: "audit_unavailable"}
		c, err := Open(context.Background(), observedDialer{&observedConn{}}, w, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		elapsed := BindStartWindow - time.Nanosecond
		if late {
			elapsed = BindStartWindow
		}
		c.now = func() time.Time { return c.helloCompleted.Add(elapsed) }
		_, _, err = c.Bind("id", BindRequest{})
		if late {
			if !errors.Is(err, ErrUnsent) || c.BindWriteStarted() || w.writes != 0 {
				t.Fatalf("late guard: %v writes=%d", err, w.writes)
			}
		} else if !c.BindWriteStarted() || w.writes != 1 || errors.Is(err, ErrUnsent) {
			t.Fatalf("inside control: %v writes=%d", err, w.writes)
		}
	}
}

// Claim: hello diagnostics promise no record or bind, not absence of native checks/audit.
func TestHelloDiagnosticContract(t *testing.T) {
	want := "before any bind request; hello is a native request that the holder checks and audits; this path promises no run record and no bind"
	if !strings.Contains(ErrHello.Error(), want) {
		t.Fatalf("hello wording: %s", ErrHello)
	}
}

// Claim: Open with a nil dialer is named unpinned rather than a retryable connection failure.
func TestOpenNilDialer(t *testing.T) {
	if _, err := Open(context.Background(), nil, &policyWire{}, time.Second); !codes.IsUnpinned(err) {
		t.Fatalf("nil dialer: %v", err)
	}
	c, err := Open(context.Background(), observedDialer{&observedConn{}}, &policyWire{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}
