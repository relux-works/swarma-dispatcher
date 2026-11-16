// Package transport implements only the closed capacity-refusal transport.
// Kernel and protected-deployment qualification is separate from parser success.
package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

var (
	ErrInvalid     = errors.New("transport: invalid message")
	ErrConfig      = errors.New("transport: invalid configuration")
	ErrIdentity    = errors.New("transport: identity mismatch")
	ErrAncillary   = errors.New("transport: ancillary data refused")
	ErrLimit       = errors.New("transport: resource limit")
	ErrOwnership   = errors.New("transport: endpoint ownership uncertain")
	ErrShutdown    = errors.New("transport: shutdown budget exceeded")
	ErrUnsupported = errors.New("transport: Linux is required")
	// ErrFatal from an Extension ends the daemon: the serve loop requests
	// shutdown with that cause and the process exits failure.
	ErrFatal = errors.New("transport: extension requires process exit")
)

// Extension serves one post-hello frame that is not the closed capacity or
// two-member rejection grammar. It receives the kernel peer UID, the validated
// connection correlation ID and the borrowed frame body, and returns the
// complete response payload (at most 508 bytes) or an error that closes the
// connection without a reply. A nil Extension keeps the reference subset
// unchanged: such frames close without a reply. Daemon attaches none; only
// DaemonWith can. ManagedCallers exposes protected M0 installation data, not
// request-supplied authority. It is snapshotted and pinned before listening.
// An empty list admits no managed peers (NewExtension without Attach); M0
// Attach itself requires 1..4 entries. Managed peers can send only M0 frames.
type Extension interface {
	ManagedCallers() []Principal
	Serve(peer uint32, id string, body []byte) (string, error)
}

// M0RequestKind is the only post-hello discriminator available to managed peers.
const M0RequestKind = "swarma-dispatcher m0-request/1"

func m0Request(b []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil {
		return false
	}
	var kind string
	return json.Unmarshal(fields["kind"], &kind) == nil && kind == M0RequestKind
}

// scanCanonical gates bytes before typed decoding; it never normalizes input.
func scanCanonical(b []byte, maxDepth int, wire bool) bool {
	s := scanner{b: b, maxDepth: maxDepth, wire: wire}
	return s.value(0) && s.i == len(b)
}

type scanner struct {
	b           []byte
	i, maxDepth int
	wire        bool
}

func (s *scanner) take(c byte) bool {
	if s.i < len(s.b) && s.b[s.i] == c {
		s.i++
		return true
	}
	return false
}
func (s *scanner) str() ([]byte, bool) {
	if !s.take('"') {
		return nil, false
	}
	start := s.i
	for s.i < len(s.b) {
		c := s.b[s.i]
		if c == '"' {
			v := s.b[start:s.i]
			s.i++
			return v, true
		}
		if c < 0x20 || c > 0x7e || c == '\\' {
			return nil, false
		}
		s.i++
	}
	return nil, false
}
func (s *scanner) value(depth int) bool {
	if s.i >= len(s.b) {
		return false
	}
	switch s.b[s.i] {
	case '{':
		if depth >= s.maxDepth {
			return false
		}
		s.i++
		if s.take('}') {
			return true
		}
		var previous []byte
		first := true
		for {
			key, ok := s.str()
			if !ok || (!first && bytes.Compare(previous, key) >= 0) {
				return false
			}
			previous = key
			first = false
			if !s.take(':') || !s.value(depth+1) {
				return false
			}
			if s.take('}') {
				return true
			}
			if !s.take(',') {
				return false
			}
		}
	case '[':
		if depth >= s.maxDepth {
			return false
		}
		s.i++
		if s.take(']') {
			return true
		}
		for {
			if !s.value(depth + 1) {
				return false
			}
			if s.take(']') {
				return true
			}
			if !s.take(',') {
				return false
			}
		}
	case '"':
		_, ok := s.str()
		return ok
	case 't':
		return s.literal("true")
	case 'f':
		return s.literal("false")
	default:
		start := s.i
		for s.i < len(s.b) && s.b[s.i] >= '0' && s.b[s.i] <= '9' {
			s.i++
		}
		n := s.i - start
		return n > 0 && (n == 1 || s.b[start] != '0') && (!s.wire || string(s.b[start:s.i]) == "1")
	}
}
func (s *scanner) literal(v string) bool {
	if !bytes.HasPrefix(s.b[s.i:], []byte(v)) {
		return false
	}
	s.i += len(v)
	return true
}
func decodeConfig(b []byte, out any, limit int) error {
	if len(b) == 0 || len(b) > limit || !scanCanonical(b, 3, false) {
		return ErrConfig
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrConfig
	}
	return nil
}
func validID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", rune(c))) {
			return false
		}
	}
	return true
}
func hello(id string) string { return `{"request_id":"` + id + `","versions":[1]}` }
func helloResult(id string) string {
	return `{"ok":true,"request_id":"` + id + `","result":{"version":1},"v":1}`
}
func capacity(id string) string { return `{"op":"capacity","request_id":"` + id + `"}` }
func hold(id string) string {
	return `{"error":{"code":"capacity:hold","message":"admission closed"},"ok":false,"request_id":"` + id + `","v":1}`
}
func invalid(id string) string {
	return `{"error":{"code":"request_invalid","message":"unsupported request"},"ok":false,"request_id":"` + id + `","v":1}`
}
func parseHello(b []byte) (string, error) {
	const prefix = `{"request_id":"`
	const suffix = `","versions":[1]}`
	if !bytes.HasPrefix(b, []byte(prefix)) || !bytes.HasSuffix(b, []byte(suffix)) || len(b) < len(prefix)+len(suffix) {
		return "", ErrInvalid
	}
	length := len(b) - len(prefix) - len(suffix)
	if length < 1 || length > 64 {
		return "", ErrInvalid
	}
	id := string(b[len(prefix) : len(b)-len(suffix)])
	if !validID(id) {
		return "", ErrInvalid
	}
	return id, nil
}
func parseOperation(b []byte, id string) (bool, error) {
	// Extract strings as byte slices, avoiding an allocation proportional to an unknown op.
	s := scanner{b: b}
	if !s.take('{') {
		return false, ErrInvalid
	}
	key, ok := s.str()
	if !ok || !bytes.Equal(key, []byte("op")) || !s.take(':') {
		return false, ErrInvalid
	}
	op, ok := s.str()
	if !ok || !s.take(',') {
		return false, ErrInvalid
	}
	key, ok = s.str()
	if !ok || !bytes.Equal(key, []byte("request_id")) || !s.take(':') {
		return false, ErrInvalid
	}
	value, ok := s.str()
	if !ok || !bytes.Equal(value, []byte(id)) || !validID(id) || !s.take('}') || s.i != len(b) {
		return false, ErrInvalid
	}
	return bytes.Equal(op, []byte("capacity")), nil
}

// readSnapshot's size gate is independent of semantic entry maxima.
func readSnapshot(r io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(limit+1)))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b) > limit {
		return nil, ErrConfig
	}
	return b, nil
}
