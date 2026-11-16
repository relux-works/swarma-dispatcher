// Package keymaker is the launcher-side Keymaker lifecycle client of the P17
// hook interface: j7_run_bind (H2) and j7_run_end (H5, H6).
//
// The native codec (pin B, module swarma-keymaker, package j7lifecycle) is a
// seam. This build does not vendor that codec, so the default Wire refuses by
// name. The codec is pure in any case: it provides no peer authentication, no
// generation check and no authority. Those duties, plus the per-request
// timeout, the same-connection retry rule, the HELD outcome and the exact-end
// check, live in this client.
package keymaker

import (
	"errors"
	"io"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

// Pin names the exact Keymaker lifecycle codec this client waits for.
const Pin = "github.com/relux-works/swarma-keymaker j7lifecycle at e5d8f2484f8c17f76fceedeed908b2587e9b4d2e"

// The pinned wire-v1 vector corpus the client tests must replay once the
// codec is vendored. See TestPinnedVectorsDigest.
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
type BindRequest struct {
	IdentityRegistrationNumber string
	InputPrincipal             string
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
	ID     string
	Method Method
	Bind   *BindRequest
	End    *EndRequest
}

// Response is one decoded native response: the request ID it answers and
// its opaque result body.
type Response struct {
	ID   string
	Body []byte
}

// ErrRefused is an explicit, exact Keymaker refusal of a request. It is a
// definite outcome: the run was not bound, or the end was refused.
var ErrRefused = errors.New("keymaker: refused")

// Wire is the codec seam. Encode is deterministic for a given request so that
// a retry sends the identical bytes. Frames are the native framing.
type Wire interface {
	Encode(req Request) ([]byte, error)
	WriteFrame(w io.Writer, b []byte) error
	ReadFrame(r io.Reader) ([]byte, error)
	DecodeResponse(b []byte) (Response, error)
	ParseBindResult(body []byte) (BindResult, error)
	ParseEndResult(body []byte) (EndResult, error)
}

// Unpinned is the default Wire; every method refuses by name.
type Unpinned struct{}

func unpinned() error { return codes.Unpinned("keymaker wire codec", Pin) }

func (Unpinned) Encode(Request) ([]byte, error)             { return nil, unpinned() }
func (Unpinned) WriteFrame(io.Writer, []byte) error         { return unpinned() }
func (Unpinned) ReadFrame(io.Reader) ([]byte, error)        { return nil, unpinned() }
func (Unpinned) DecodeResponse([]byte) (Response, error)    { return Response{}, unpinned() }
func (Unpinned) ParseBindResult([]byte) (BindResult, error) { return BindResult{}, unpinned() }
func (Unpinned) ParseEndResult([]byte) (EndResult, error)   { return EndResult{}, unpinned() }
