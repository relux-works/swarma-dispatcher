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

var bindReq = keymaker.BindRequest{IdentityRegistrationNumber: "station-1", InputPrincipal: "input-1", Origin: fakes.ValidOrigin()}

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
// "", and exactly one frame was written.
func TestBindHappy(t *testing.T) {
	script := &fakes.KeymakerScript{}
	c, d := open(t, script)
	res, sent, err := c.Bind("req-1", bindReq, 3)
	if err != nil || res.RunID != "run-1" || c.Requests() != 1 || d.Dials() != 1 {
		t.Fatalf("%+v %v requests=%d", res, err, c.Requests())
	}
	binds := script.Binds()
	if len(binds) != 1 || !bytes.Equal(binds[0], sent) {
		t.Fatalf("binds %d", len(binds))
	}
	id, body, err := fakes.ParseBind(binds[0])
	if err != nil || id != "req-1" || !bytes.Equal(body.Origin, fakes.ValidOrigin()) || body.ParentRun != "" || body.IdentityRegistrationNumber != "station-1" {
		t.Fatalf("parsed %q %+v %v", id, body, err)
	}
}

// Claim (H2 step 6, "lost bind: retry on the same connection succeeds"): a
// response lost within the timeout is retried with byte-identical bytes on
// the same connection, no second connection is dialed, and the bind
// completes.
func TestBindLostRetrySameConnection(t *testing.T) {
	script := &fakes.KeymakerScript{BindMode: "drop-first"}
	c, d := open(t, script)
	res, sent, err := c.Bind("req-1", bindReq, 3)
	if err != nil || res.RunID != "run-1" {
		t.Fatalf("%+v %v", res, err)
	}
	binds := script.Binds()
	if len(binds) != 2 || !bytes.Equal(binds[0], binds[1]) || !bytes.Equal(binds[1], sent) || d.Dials() != 1 || c.Requests() != 2 {
		t.Fatalf("binds=%d dials=%d requests=%d", len(binds), d.Dials(), c.Requests())
	}
}

// Claim (H2 step 6, "after connection loss the run is HELD, never bound
// blindly"): a connection closed before the reply, a response whose id does
// not match, a result without run_id, and a response lost on every attempt
// each yield run_bind_unknown; the connection is not live afterwards and no
// new connection is dialed by the client.
func TestBindHeld(t *testing.T) {
	for _, mode := range []string{"close", "wrong-id", "no-run-id", "drop-all"} {
		script := &fakes.KeymakerScript{BindMode: mode}
		c, d := open(t, script)
		_, _, err := c.Bind("req-1", bindReq, 2)
		if !codes.Is(err, codes.BindUnknown) {
			t.Fatalf("%s: %v", mode, err)
		}
		if d.Dials() != 1 {
			t.Fatalf("%s: client dialed again", mode)
		}
		if mode == "drop-all" && (c.Requests() != 2 || !c.Live()) {
			t.Fatalf("drop-all: requests=%d live=%v", c.Requests(), c.Live())
		}
		if mode != "drop-all" && c.Live() {
			t.Fatalf("%s: connection still live", mode)
		}
	}
}

// Claim: an exact Keymaker refusal of the bind is ErrRefused, not UNKNOWN:
// the run was definitely not bound.
func TestBindRefused(t *testing.T) {
	c, _ := open(t, &fakes.KeymakerScript{BindMode: "refuse"})
	_, _, err := c.Bind("req-1", bindReq, 2)
	if !errors.Is(err, keymaker.ErrRefused) || codes.Is(err, codes.BindUnknown) {
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

// Claim (H5 step 3 and "EOF on Keymaker is NOT an end"): a dropped end
// response, EOF before the reply, an explicit refusal, ended:false, and an
// ended result for another run id are each run_end_unknown with the run id
// in the detail; none seals the channel as acknowledged.
func TestEndUnknownVariants(t *testing.T) {
	for _, mode := range []string{"drop", "close", "refuse", "not-ended", "other-run"} {
		c, _ := open(t, &fakes.KeymakerScript{EndMode: mode})
		_, _, err := c.End("end-1", keymaker.EndRequest{RunID: "run-1"})
		if !codes.Is(err, codes.EndUnknown) {
			t.Fatalf("%s: %v", mode, err)
		}
		var r *codes.Refusal
		if !errors.As(err, &r) || r.Detail["run_id"] != "run-1" {
			t.Fatalf("%s: detail %v", mode, err)
		}
	}
}

// Claim (seam): with the pinned codec absent the default wire refuses by
// name before any frame is written.
func TestUnpinnedWire(t *testing.T) {
	d := &fakes.KeymakerDialer{Script: &fakes.KeymakerScript{}}
	c, err := keymaker.Open(context.Background(), d, nil, timeout)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, err = c.Bind("req-1", bindReq, 1)
	if !codes.IsUnpinned(err) || c.Requests() != 0 {
		t.Fatalf("%v requests=%d", err, c.Requests())
	}
	_, _, err = c.End("end-1", keymaker.EndRequest{RunID: "run-1"})
	if !codes.IsUnpinned(err) || c.Requests() != 0 {
		t.Fatalf("%v requests=%d", err, c.Requests())
	}
}

// Claim (pin B corpus): when the pinned vector corpus is supplied through
// SWARMA_KEYMAKER_VECTORS it has exactly the pinned size and SHA-256; without
// it this test is UNRUN, reported as a skip, never as a pass. Replaying the
// vectors through the client needs the pinned codec, which this build does
// not vendor (see keymaker.Pin).
func TestPinnedVectorsDigest(t *testing.T) {
	path := os.Getenv("SWARMA_KEYMAKER_VECTORS")
	if path == "" {
		t.Skipf("pinned vectors UNRUN: set SWARMA_KEYMAKER_VECTORS to %s of %s", keymaker.VectorsPath, keymaker.Pin)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if len(b) != keymaker.VectorsSize || hex.EncodeToString(sum[:]) != keymaker.VectorsSHA256 {
		t.Fatalf("vector corpus mismatch: size=%d sha256=%s", len(b), hex.EncodeToString(sum[:]))
	}
}
