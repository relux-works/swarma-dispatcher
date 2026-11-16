//go:build linux

package keymaker_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
)

const timeout = 100 * time.Millisecond

func open(t *testing.T, script *fakes.KeymakerScript) (*keymaker.Conn, *fakes.KeymakerDialer) {
	t.Helper()
	d := &fakes.KeymakerDialer{Script: script}
	c, err := keymaker.Open(context.Background(), d, fakes.KeymakerWire{}, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, d
}

var bindReq = keymaker.BindRequest{IdentityRegistrationNumber: 1, InputPrincipal: keymaker.Principal{UID: 22001, Generation: "input-generation-1"}, Origin: fakes.ValidOrigin()}

// Claim (D4): the per-request timeout must be 1..2000 ms; 0, 2001 ms and a
// sub-millisecond value refuse before dialing, 1 ms and 2000 ms are accepted.
func TestOpenTimeoutWindow(t *testing.T) {
	d := &fakes.KeymakerDialer{Script: &fakes.KeymakerScript{}}
	for _, bad := range []time.Duration{0, 2001 * time.Millisecond, 500 * time.Microsecond, -time.Second} {
		if _, err := keymaker.Open(context.Background(), d, fakes.KeymakerWire{}, bad); !errors.Is(err, keymaker.ErrTimeout) {
			t.Fatalf("%v accepted: %v", bad, err)
		}
	}
	if d.Dials() != 0 {
		t.Fatal("dialed with an invalid timeout")
	}
	for _, ok := range []time.Duration{time.Millisecond, 2000 * time.Millisecond} {
		c, err := keymaker.Open(context.Background(), d, fakes.KeymakerWire{}, ok)
		if err != nil {
			t.Fatalf("%v refused: %v", ok, err)
		}
		_ = c.Close()
	}
}

// Claim (H2 step 5, positive control): a bind on a fresh connection returns
// the run_id, the request carries the origin bytes verbatim and parent_run
// "", exactly one frame was written, and hello was negotiated before it.
func TestBindHappy(t *testing.T) {
	script := &fakes.KeymakerScript{}
	c, d := open(t, script)
	if script.Hellos() != 1 || len(script.Binds()) != 0 {
		t.Fatalf("after Open: hellos=%d binds=%d", script.Hellos(), len(script.Binds()))
	}
	res, sent, err := c.Bind("req-1", bindReq)
	if err != nil || res.RunID != "run-1" || c.Requests() != 1 || d.Dials() != 1 {
		t.Fatalf("%+v %v requests=%d", res, err, c.Requests())
	}
	binds := script.Binds()
	if len(binds) != 1 || !bytes.Equal(binds[0], sent) {
		t.Fatalf("binds %d", len(binds))
	}
	id, body, err := fakes.ParseBind(binds[0])
	if err != nil || id != "req-1" || !bytes.Equal(body.Origin, fakes.ValidOrigin()) || body.ParentRun != "" || body.IdentityRegistrationNumber != 1 {
		t.Fatalf("parsed %q %+v %v", id, body, err)
	}
}

// Claim (eager hello): Open negotiates hello before returning; a peer that
// closes on hello or refuses it fails Open with ErrHello and no request frame
// is ever written. Positive control: the answering peer opens and binds.
func TestOpenHelloEager(t *testing.T) {
	for _, mode := range []string{"close", "refuse"} {
		script := &fakes.KeymakerScript{HelloMode: mode}
		d := &fakes.KeymakerDialer{Script: script}
		c, err := keymaker.Open(context.Background(), d, fakes.KeymakerWire{}, timeout)
		if !errors.Is(err, keymaker.ErrHello) || c != nil || d.Dials() != 1 {
			t.Fatalf("%s: %v", mode, err)
		}
		if script.Hellos() != 1 || len(script.Binds()) != 0 || len(script.Ends()) != 0 {
			t.Fatalf("%s: hellos=%d binds=%d", mode, script.Hellos(), len(script.Binds()))
		}
	}
	script := &fakes.KeymakerScript{}
	c, _ := open(t, script)
	if res, _, err := c.Bind("req-1", bindReq); err != nil || res.RunID != "run-1" || script.Hellos() != 1 {
		t.Fatalf("positive control: %+v %v", res, err)
	}
}

const lateBy = timeout + 20*time.Millisecond

// Claim (F1 scenario b, "a late success is read, never answered by a
// resend"): a bind success that arrives 20 ms after the request's own
// timeout_ms is read inside the local read budget; exactly one request frame
// was written, no byte reached the peer while its reply was owed, and the end
// on the same live connection is then acknowledged.
func TestBindLateReplyNoResend(t *testing.T) {
	script := &fakes.KeymakerScript{BindMode: "late", Delay: lateBy}
	c, d := open(t, script)
	res, _, err := c.Bind("req-1", bindReq)
	if err != nil || res.RunID != "run-1" {
		t.Fatalf("late success not read: %+v %v", res, err)
	}
	if c.Requests() != 1 || len(script.Binds()) != 1 || script.Overlaps() != 0 || d.Dials() != 1 || !c.Live() {
		t.Fatalf("requests=%d binds=%d overlaps=%d dials=%d live=%v", c.Requests(), len(script.Binds()), script.Overlaps(), d.Dials(), c.Live())
	}
	end, _, err := c.End("end-1", keymaker.EndRequest{RunID: "run-1"})
	if err != nil || !end.Ended || len(script.Ends()) != 1 || script.Overlaps() != 0 {
		t.Fatalf("end on the same connection: %+v %v", end, err)
	}
}

// Claim (F1 scenario a and the positive control "a read correlated retryable
// refusal, then the exact retry succeeds"): a correlated
// request_deadline_exceeded that arrives 20 ms after timeout_ms is READ, and
// only then the identical bytes (same ID, same body, same timeout_ms inside
// 1..2000) are written again on the same connection and bind; exactly two
// request frames, never overlapping, one connection.
func TestBindRetryAfterReadRetryableRefusal(t *testing.T) {
	script := &fakes.KeymakerScript{BindMode: "deadline-then-ok", Delay: lateBy}
	c, d := open(t, script)
	res, sent, err := c.Bind("req-1", bindReq)
	if err != nil || res.RunID != "run-1" {
		t.Fatalf("%+v %v", res, err)
	}
	binds := script.Binds()
	if len(binds) != 2 || !bytes.Equal(binds[0], binds[1]) || !bytes.Equal(binds[1], sent) || d.Dials() != 1 || c.Requests() != 2 || script.Overlaps() != 0 || !c.Live() {
		t.Fatalf("binds=%d dials=%d requests=%d overlaps=%d", len(binds), d.Dials(), c.Requests(), script.Overlaps())
	}
	for i, raw := range binds {
		ms, err := fakes.RequestTimeoutMS(raw)
		if err != nil || ms != timeout.Milliseconds() || !codes.RequestTimeoutOK(ms) {
			t.Fatalf("attempt %d carries timeout_ms %d: %v", i+1, ms, err)
		}
	}
}

// Claim ("a timeout with a reply owed closes the channel, HELD, exactly one
// request written"): a peer that holds the bind unanswered makes the local
// read budget (timeout_ms + holder budget + margin) expire; the client closes
// the connection and returns run_bind_unknown with ErrLost after writing
// exactly one request frame, and the peer saw no byte while its reply was
// owed. Positive control: a reply that is late but inside the budget binds
// (TestBindLateReplyNoResend) and a fast peer binds here.
func TestBindTimeoutReplyOwedHeld(t *testing.T) {
	script := &fakes.KeymakerScript{BindMode: "silent"}
	d := &fakes.KeymakerDialer{Script: script}
	c, err := keymaker.Open(context.Background(), d, fakes.KeymakerWire{}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	_, _, err = c.Bind("req-1", bindReq)
	waited := time.Since(start)
	if !codes.Is(err, codes.BindUnknown) || !errors.Is(err, keymaker.ErrLost) || !errors.Is(err, keymaker.ErrConnectionGone) {
		t.Fatalf("%v", err)
	}
	if c.Requests() != 1 || len(script.Binds()) != 1 || script.Overlaps() != 0 || c.Live() || d.Dials() != 1 {
		t.Fatalf("requests=%d binds=%d overlaps=%d live=%v dials=%d", c.Requests(), len(script.Binds()), script.Overlaps(), c.Live(), d.Dials())
	}
	if want := keymaker.ReadBudget(1); waited < want || want != time.Millisecond+keymaker.HolderFrameReportBudget+keymaker.ReadMargin {
		t.Fatalf("gave up after %v, before the read budget %v", waited, want)
	}
	var r *codes.Refusal
	if !errors.As(err, &r) || r.Detail["requests_written"] != "1" || r.Detail["request_id"] != "req-1" {
		t.Fatalf("detail %v", err)
	}
	if v, e := codes.Exit(err, 0, false); e != nil || v.Status != 17 {
		t.Fatalf("exit %+v %v", v, e)
	}
	// Positive control on the same fixture.
	ok, _ := open(t, &fakes.KeymakerScript{})
	if res, _, err := ok.Bind("req-1", bindReq); err != nil || res.RunID != "run-1" {
		t.Fatalf("positive control: %+v %v", res, err)
	}
}

// Claim ("wrong-ID response: no retry"; a null ID likewise): a retryable
// refusal whose ID is null or differs from the outstanding bind ID is never
// retried: run_bind_unknown with ErrUncorrelated, the connection closed,
// exactly one request frame, and the native code and retryable bit in the
// detail. Positive control: the same retryable refusal WITH the outstanding
// ID is retried and binds.
func TestBindUncorrelatedRefusalNoRetry(t *testing.T) {
	for _, mode := range []string{"wrong-id", "null-id"} {
		script := &fakes.KeymakerScript{BindMode: mode}
		c, d := open(t, script)
		_, _, err := c.Bind("req-1", bindReq)
		if !codes.Is(err, codes.BindUnknown) || !errors.Is(err, keymaker.ErrUncorrelated) {
			t.Fatalf("%s: %v", mode, err)
		}
		if c.Requests() != 1 || len(script.Binds()) != 1 || c.Live() || d.Dials() != 1 {
			t.Fatalf("%s: requests=%d binds=%d live=%v", mode, c.Requests(), len(script.Binds()), c.Live())
		}
		var r *codes.Refusal
		if !errors.As(err, &r) || r.Detail["native_code"] != "busy" || r.Detail["native_retryable"] != "true" {
			t.Fatalf("%s: detail %v", mode, err)
		}
	}
	script := &fakes.KeymakerScript{BindMode: "deadline-then-ok"}
	c, _ := open(t, script)
	if res, _, err := c.Bind("req-1", bindReq); err != nil || res.RunID != "run-1" || c.Requests() != 2 {
		t.Fatalf("positive control: %+v %v requests=%d", res, err, c.Requests())
	}
}

// Claim ("limit reached: HELD, requests written == limit"): a peer that
// answers every bind with a correlated retryable refusal is asked exactly
// BindAttemptLimit times with identical bytes, then the connection is closed
// and the run is HELD, run_bind_unknown, exit 17, never run_bind_refused.
// Positive control: one retryable refusal followed by success binds with two
// requests, below the limit.
func TestBindAttemptLimitHeld(t *testing.T) {
	if keymaker.BindAttemptLimit != 3 {
		t.Fatalf("the fixed attempt limit changed: %d", keymaker.BindAttemptLimit)
	}
	script := &fakes.KeymakerScript{BindMode: "busy"}
	c, d := open(t, script)
	_, _, err := c.Bind("req-1", bindReq)
	if !codes.Is(err, codes.BindUnknown) || errors.Is(err, keymaker.ErrRefused) {
		t.Fatalf("%v", err)
	}
	binds := script.Binds()
	if c.Requests() != keymaker.BindAttemptLimit || len(binds) != keymaker.BindAttemptLimit || c.Live() || d.Dials() != 1 || script.Overlaps() != 0 {
		t.Fatalf("requests=%d binds=%d live=%v", c.Requests(), len(binds), c.Live())
	}
	for _, b := range binds[1:] {
		if !bytes.Equal(b, binds[0]) {
			t.Fatal("a retry changed the request bytes")
		}
	}
	var r *codes.Refusal
	if !errors.As(err, &r) || r.Detail["requests_written"] != "3" || r.Detail["native_code"] != "busy" || r.Detail["native_retryable"] != "true" {
		t.Fatalf("detail %v", err)
	}
	if v, e := codes.Exit(err, 0, false); e != nil || v.Status != 17 {
		t.Fatalf("exit %+v %v", v, e)
	}
	script = &fakes.KeymakerScript{BindMode: "deadline-then-ok"}
	c, _ = open(t, script)
	if res, _, err := c.Bind("req-1", bindReq); err != nil || res.RunID != "run-1" || c.Requests() != 2 {
		t.Fatalf("positive control: %+v %v requests=%d", res, err, c.Requests())
	}
}

// Claim (H2 step 5c and step 6, "after connection loss the run is HELD,
// never bound blindly"): a connection closed before the reply, a result
// without run_id and a correlated non-retryable refusal outside the no-effect
// set each yield run_bind_unknown after exactly one request; the connection
// is not live afterwards and no new connection is dialed by the client.
// Positive control: TestBindHappy on the same fixture.
func TestBindHeld(t *testing.T) {
	for _, mode := range []string{"close", "no-run-id", "nonretryable"} {
		script := &fakes.KeymakerScript{BindMode: mode}
		c, d := open(t, script)
		_, _, err := c.Bind("req-1", bindReq)
		if !codes.Is(err, codes.BindUnknown) {
			t.Fatalf("%s: %v", mode, err)
		}
		if d.Dials() != 1 || c.Requests() != 1 || c.Live() {
			t.Fatalf("%s: dials=%d requests=%d live=%v", mode, d.Dials(), c.Requests(), c.Live())
		}
		var r *codes.Refusal
		if mode == "nonretryable" && (!errors.As(err, &r) || r.Detail["native_code"] != "role_not_permitted" || r.Detail["native_retryable"] != "false") {
			t.Fatalf("%s: detail %v", mode, err)
		}
	}
}

// Claim: a correlated refusal that the wire certifies as in the no-effect
// set is run_bind_refused with ErrRefused, not UNKNOWN, after one request.
// Negative neighbour: the uncertified correlated refusal in TestBindHeld.
func TestBindRefused(t *testing.T) {
	c, _ := open(t, &fakes.KeymakerScript{BindMode: "refuse"})
	_, _, err := c.Bind("req-1", bindReq)
	if !errors.Is(err, keymaker.ErrRefused) || !codes.Is(err, codes.BindRefused) || c.Requests() != 1 {
		t.Fatalf("%v", err)
	}
}

// Claim (H5 step 3, positive control): the exact ended result for the run id
// acknowledges the end and seals the channel; a later request on it is
// refused as sealed rather than sent.
func TestEndExactAndSealed(t *testing.T) {
	script := &fakes.KeymakerScript{}
	c, _ := open(t, script)
	res, sent, err := c.End("end-1", keymaker.EndRequest{RunID: "run-1"})
	if err != nil || !res.Ended || res.RunID != "run-1" {
		t.Fatalf("%+v %v", res, err)
	}
	if ends := script.Ends(); len(ends) != 1 || !bytes.Equal(ends[0], sent) {
		t.Fatalf("ends %d", len(ends))
	}
	_, _, err = c.End("end-2", keymaker.EndRequest{RunID: "run-1"})
	if !codes.Is(err, codes.EndUnknown) || !errors.Is(err, keymaker.ErrSealed) || c.Requests() != 1 {
		t.Fatalf("after seal: %v requests=%d", err, c.Requests())
	}
}

// Claim (the in-flight rule on the end path): an exact ended result that
// arrives 20 ms after the end request's own timeout_ms is read inside the
// local read budget and acknowledges the end with one request frame.
func TestEndLateReplyAcknowledged(t *testing.T) {
	script := &fakes.KeymakerScript{EndMode: "late", Delay: lateBy}
	c, _ := open(t, script)
	res, _, err := c.End("end-1", keymaker.EndRequest{RunID: "run-1"})
	if err != nil || !res.Ended || c.Requests() != 1 || script.Overlaps() != 0 {
		t.Fatalf("late end ack not read: %+v %v requests=%d", res, err, c.Requests())
	}
}

// Claim (H5 step 3 and "EOF on Keymaker is NOT an end"): an end held
// unanswered past the local read budget, EOF before the reply, an explicit
// refusal, ended:false, and an ended result for another run id are each
// run_end_unknown with the run id in the detail; none seals the channel as
// acknowledged, each wrote exactly one request and closed the connection so
// that nothing further can be written while a reply is owed. Positive
// control: TestEndExactAndSealed.
func TestEndUnknownVariants(t *testing.T) {
	for _, mode := range []string{"silent", "close", "refuse", "not-ended", "other-run"} {
		script := &fakes.KeymakerScript{EndMode: mode}
		c, _ := open(t, script)
		_, _, err := c.End("end-1", keymaker.EndRequest{RunID: "run-1"})
		if !codes.Is(err, codes.EndUnknown) {
			t.Fatalf("%s: %v", mode, err)
		}
		var r *codes.Refusal
		if !errors.As(err, &r) || r.Detail["run_id"] != "run-1" {
			t.Fatalf("%s: detail %v", mode, err)
		}
		if c.Requests() != 1 || c.Live() || script.Overlaps() != 0 {
			t.Fatalf("%s: requests=%d live=%v overlaps=%d", mode, c.Requests(), c.Live(), script.Overlaps())
		}
		if mode == "silent" && !errors.Is(err, keymaker.ErrLost) {
			t.Fatalf("silent: %v", err)
		}
		if mode == "refuse" && r.Detail["native_code"] != "run_unknown" {
			t.Fatalf("refuse: detail %v", err)
		}
	}
}

// Claim (seam): with the pinned codec absent the default wire refuses by
// name at the eager hello: Open fails unpinned and no request is written.
// Positive control: the same dialer with the test wire opens.
func TestUnpinnedWire(t *testing.T) {
	script := &fakes.KeymakerScript{}
	d := &fakes.KeymakerDialer{Script: script}
	c, err := keymaker.Open(context.Background(), d, nil, timeout)
	if !codes.IsUnpinned(err) || !errors.Is(err, keymaker.ErrHello) || c != nil {
		t.Fatalf("%v", err)
	}
	if script.Hellos() != 0 || len(script.Binds()) != 0 || len(script.Ends()) != 0 {
		t.Fatal("frames written with the unpinned wire")
	}
	c, err = keymaker.Open(context.Background(), d, fakes.KeymakerWire{}, timeout)
	if err != nil {
		t.Fatalf("positive control: %v", err)
	}
	_ = c.Close()
}

// Claim: the byte copy of pin B's corpus always has the pinned size and SHA-256.
func TestPinnedVectorsDigest(t *testing.T) {
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if len(b) != keymaker.VectorsSize || hex.EncodeToString(sum[:]) != keymaker.VectorsSHA256 {
		t.Fatalf("vector corpus mismatch: size=%d sha256=%s", len(b), hex.EncodeToString(sum[:]))
	}
}
