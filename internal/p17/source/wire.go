// Package source is the launcher-side Source client of the P17 hook
// interface: H1 run.prepare and the validate step of H2 run.bind.
//
// The wire codec of Source (pin A, module a2a-swarm, package runorigin) is a
// seam. This build does not vendor that codec, so the default Wire refuses by
// name; nothing here substitutes a local spelling of the Source wire. The
// client around the seam owns everything the codec does not: the socket path
// check, kernel peer authentication, the generation check, the five-second
// budget, the one-connection validate, the byte-equal binding recheck and the
// origin shape check.
package source

import (
	"errors"
	"net"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

// Pin names the exact Source origin codec this client waits for.
const Pin = "github.com/relux-works/a2a-swarm runorigin at f9d465ba460472ab6253cf82b8edaf8050e74ce6"

// HelloMessage is what Source announces after connect. Capability and
// incarnation are announcements, not authentication.
type HelloMessage struct {
	Capability  bool
	Incarnation string
}

// PrepareRequest is the launcher's input to source.origin.prepare/1. The
// pinned codec decides the wire spelling; LaunchNonce identifies the run.
type PrepareRequest struct {
	LaunchNonce string
}

// PreparedMessage is the prepare result. Bindings are every binding Source
// returns, by name, as exact strings.
type PreparedMessage struct {
	PreparedID  string
	Incarnation string
	Bindings    map[string]string
}

// ValidateRequest is the launcher's input to source.origin.validate/1.
type ValidateRequest struct {
	PreparedID string
}

// ValidatedMessage is the validate result. Origin is the admission origin
// exactly as returned, as raw bytes.
type ValidatedMessage struct {
	PreparedID  string
	Incarnation string
	Bindings    map[string]string
	Origin      []byte
}

// Wire is the Source codec seam. Each method performs exactly one exchange on
// the connection and honours the connection deadline set by the client. It
// returns ErrNotSupported, ErrRefused or ErrDependency for Source-reported
// outcomes, and the transport error otherwise.
type Wire interface {
	Hello(c net.Conn) (HelloMessage, error)
	Prepare(c net.Conn, req PrepareRequest) (PreparedMessage, error)
	Validate(c net.Conn, req ValidateRequest) (ValidatedMessage, error)
}

var (
	// ErrNotSupported is origin_not_supported from an a2ad without the P17
	// capability. It is a definite refusal, never unavailable.
	ErrNotSupported = errors.New("source: origin_not_supported")
	// ErrRefused is any other explicit Source refusal.
	ErrRefused = errors.New("source: refused")
	// ErrDependency is an explicit Source dependency failure.
	ErrDependency = errors.New("source: dependency failure")
)

// Unpinned is the default Wire. It refuses every exchange by name.
type Unpinned struct{}

func (Unpinned) Hello(net.Conn) (HelloMessage, error) {
	return HelloMessage{}, codes.Unpinned("source wire codec", Pin)
}
func (Unpinned) Prepare(net.Conn, PrepareRequest) (PreparedMessage, error) {
	return PreparedMessage{}, codes.Unpinned("source wire codec", Pin)
}
func (Unpinned) Validate(net.Conn, ValidateRequest) (ValidatedMessage, error) {
	return ValidatedMessage{}, codes.Unpinned("source wire codec", Pin)
}

// Credential is the kernel peer credential of a connection.
type Credential struct {
	UID uint32
	GID uint32
	PID int32
}

// Conn is a Source connection with its kernel peer credential.
type Conn interface {
	net.Conn
	Peer() (Credential, error)
}

// Dialer opens one connection to the control socket at the given path.
type Dialer interface {
	Dial(path string) (Conn, error)
}

// Generations reads the CURRENT authoritative generation of a principal from
// the protected role pins. Default: Keymaker role pins, which this build does
// not read; see UnpinnedGenerations.
type Generations interface {
	Current(uid uint32) (string, error)
}

// GenerationsPin names the authority UnpinnedGenerations waits for.
const GenerationsPin = "Keymaker protected role pins (r2.6 PENDING item 1)"

// UnpinnedGenerations refuses every read by name.
type UnpinnedGenerations struct{}

func (UnpinnedGenerations) Current(uint32) (string, error) {
	return "", codes.Unpinned("authoritative generation reader", GenerationsPin)
}

// StaticGenerations is a fixed table, for tests and for deployments that
// inject an already-read pin table.
type StaticGenerations map[uint32]string

func (s StaticGenerations) Current(uid uint32) (string, error) {
	g, ok := s[uid]
	if !ok {
		return "", errors.New("source: no generation for uid")
	}
	return g, nil
}
