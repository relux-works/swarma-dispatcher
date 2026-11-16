// Package keymaker is the launcher-side Keymaker lifecycle client of the P17
// hook interface: j7_run_bind (H2) and j7_run_end (H5, H6).
//
// Pinned explicitly selects the native j7lifecycle codec. The zero-value
// client still selects Unpinned and refuses. The codec grants no authority:
// peer/current-generation authentication remains the dialer's responsibility.
package keymaker

import (
	"errors"
	"io"

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

// Response is decoded against the locally outstanding request, never context
// inferred from the peer. Refused certifies membership in the pinned no-effect
// set as well as exact non-null ID correlation; it is not a generic error flag.
type Response struct {
	ID      string
	Bind    *BindResult
	End     *EndResult
	Refused bool
}

// ErrRefused certifies a correlated refusal in the pinned no-effect set.
// An arbitrary native refusal, even nonretryable, must never carry it.
var ErrRefused = errors.New("keymaker: refused")

// Wire is the codec seam. Encode is deterministic for a given request so that
// a retry sends the identical bytes. Frames are the native framing.
type Wire interface {
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

func (Unpinned) Encode(Request) ([]byte, error)                   { return nil, unpinned() }
func (Unpinned) WriteFrame(io.Writer, []byte) error               { return unpinned() }
func (Unpinned) ReadFrame(io.Reader) ([]byte, error)              { return nil, unpinned() }
func (Unpinned) DecodeResponse(Request, []byte) (Response, error) { return Response{}, unpinned() }
func (Unpinned) DecodeEndRequest([]byte) (Request, error)         { return Request{}, unpinned() }
