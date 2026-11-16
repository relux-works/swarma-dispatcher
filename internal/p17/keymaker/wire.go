// Package keymaker is the launcher-side Keymaker lifecycle client of the P17
// hook interface: j7_run_bind (H2) and j7_run_end (H5, H6).
//
// Pinned explicitly selects the native j7lifecycle codec. The zero-value
// client still selects Unpinned and refuses. The codec grants no authority:
// peer/current-generation authentication remains the dialer's responsibility.
//
// Hook text of record: P17 Curator-side hook interface r2.15 (sha256
// 5b804ba49187df4e7e823703b2e65b569b3dd63cc370f5989e86436b05415246).
//
// Channel discipline (pin B spec/keymaker.md section 7: "One request is in
// flight per connection; no unsolicited replies or pipelining"):
//
//   - Open fails before any bind request; hello is a native request that the
//     holder checks and audits; this path promises no run record and no bind.
//   - Every started request waits ReadBudget(timeout_ms) for its one reply.
//     A local timeout beyond that budget makes the channel ambiguous: the
//     connection is closed and the run is HELD. A second request is never
//     written while a reply is owed.
//   - The exact bind retry (same request ID, same body, hence the same
//     timeout_ms) is written only after READING a correlated retryable
//     refusal (non-null ID equal to the outstanding bind ID, code in
//     RetryableCodes) on the original live connection. A null, malformed or
//     wrong-ID response is never retried: close, HELD.
//   - BindAttemptLimit is the one fixed, finite number of bind frames a run
//     may write. It is a constant, not configuration. When it is reached the
//     run is HELD (run_bind_unknown, exit 17) and the record stays binding.
package keymaker

import (
	"errors"
	"io"
	"net"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-keymaker/j7lifecycle"
)

// Pin names the exact Keymaker lifecycle codec this client waits for.
const Pin = "github.com/relux-works/swarma-keymaker j7lifecycle at e5d8f2484f8c17f76fceedeed908b2587e9b4d2e"

// The pinned wire-v1 vector corpus replayed by the client tests.
const (
	VectorsPath   = "j7lifecycle/testdata/wire-v1/vectors.json"
	VectorsSize   = 23676
	VectorsSHA256 = "658471bd67e9d4b01b8869036fd4936942019a3598f5594b8f523d67ef48dda0"
)

// Method is a Keymaker lifecycle request method.
type Method string

const (
	MethodBind Method = "j7_run_bind"
	MethodEnd  Method = "j7_run_end"
)

// BindRequest is j7_run_bind({identity_registration_number, input_principal,
// origin, parent_run}). Origin is the admission origin exactly as validate
// returned it; nil means JSON null, which only a child run sends.
type Principal = j7lifecycle.Principal

type BindRequest struct {
	IdentityRegistrationNumber uint64
	InputPrincipal             Principal
	Origin                     []byte
	ParentRun                  string
}

// BindResult is the exact bind result.
type BindResult struct {
	RunID string
}

// EndRequest is j7_run_end({run_id}).
type EndRequest struct {
	RunID string
}

// EndResult is the end result. Ended is true only for the exact ended
// acknowledgement of RunID.
type EndResult struct {
	RunID string
	Ended bool
}

// Request is one native request: its ID, method and exactly one body.
type Request struct {
	ID        string
	TimeoutMS int64
	Method    Method
	Bind      *BindRequest
	End       *EndRequest
}

// NativeRefusal is a decoded native refusal: its closed code and retryable
// bit. It is diagnostic and retry-policy input only; it never proves that no
// durable effect started.
type NativeRefusal struct {
	Code      string
	Retryable bool
}

// Response is decoded against the locally outstanding request, never context
// inferred from the peer. Refused certifies membership in the pinned no-effect
// set as well as exact non-null ID correlation; it is not a generic error flag.
// Refusal is set for every decoded native refusal, in or out of that set; an
// empty ID then means wire null (uncorrelated).
type Response struct {
	ID      string
	Bind    *BindResult
	End     *EndResult
	Refused bool
	Refusal *NativeRefusal
}

// RetryableCodes is the closed set of native refusal codes after which the
// exact same-ID bind retry is permitted (pin B spec/keymaker.md section 7:
// "retryable is true only for rate_limited, busy,
// generation_source_unavailable, enrollment_unavailable, audit_unavailable
// and request_deadline_exceeded; mutation retries MUST retain the original
// ID and arguments"). Membership never makes a refusal no-effect.
var retryableCodes = map[string]bool{
	"busy":                          true,
	"rate_limited":                  true,
	"request_deadline_exceeded":     true,
	"generation_source_unavailable": true,
	"enrollment_unavailable":        true,
	"audit_unavailable":             true,
}

// RetryableRefusal reports whether r permits the exact bind retry: the
// retryable bit is set and the code is in the closed retryable set.
// Correlation is checked by the caller against the outstanding request ID.
func RetryableRefusal(r *NativeRefusal) bool {
	return r != nil && r.Retryable && retryableCodes[r.Code]
}

// ErrRefused certifies a correlated refusal in the pinned no-effect set.
// An arbitrary native refusal, even nonretryable, must never carry it.
var ErrRefused = errors.New("keymaker: refused")

// Wire is the codec seam. Encode is deterministic for a given request so that
// a retry sends the identical bytes. Frames are the native framing.
type Wire interface {
	// Hello is a native request checked and audited by the holder. It
	// negotiates the protocol within budget, but authenticates no peer.
	Hello(c net.Conn, budget time.Duration) error
	Encode(req Request) ([]byte, error)
	WriteFrame(w io.Writer, b []byte) error
	ReadFrame(r io.Reader) ([]byte, error)
	DecodeResponse(expected Request, b []byte) (Response, error)
	// DecodeEndRequest reads the launcher's own durable retry intent, not peer data.
	DecodeEndRequest(b []byte) (Request, error)
}

// Unpinned is the default Wire; every method refuses by name.
type Unpinned struct{}

func unpinned() error { return codes.Unpinned("keymaker wire codec", Pin) }

func (Unpinned) Hello(net.Conn, time.Duration) error              { return unpinned() }
func (Unpinned) Encode(Request) ([]byte, error)                   { return nil, unpinned() }
func (Unpinned) WriteFrame(io.Writer, []byte) error               { return unpinned() }
func (Unpinned) ReadFrame(io.Reader) ([]byte, error)              { return nil, unpinned() }
func (Unpinned) DecodeResponse(Request, []byte) (Response, error) { return Response{}, unpinned() }
func (Unpinned) DecodeEndRequest([]byte) (Request, error)         { return Request{}, unpinned() }
