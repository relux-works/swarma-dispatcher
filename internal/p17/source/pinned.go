package source

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"unicode/utf8"

	"github.com/relux-works/a2a-swarm/runorigin"
)

// Pinned implements the documented pin A protocol using public runorigin values.
// No internal Source parser or station protocol is imported.
type Pinned struct{}

const sourceMaxFrame = 65536

func sourceRead(r io.Reader) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > sourceMaxFrame {
		return nil, ErrRefused
	}
	b := make([]byte, int(n))
	_, err := io.ReadFull(r, b)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return b, err
}
func sourceWrite(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) == 0 || len(b) > sourceMaxFrame {
		return ErrRefused
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	for _, part := range [][]byte{h[:], b} {
		n, err := w.Write(part)
		if err != nil {
			return err
		}
		if n != len(part) {
			return io.ErrShortWrite
		}
	}
	return nil
}

// object requires exact, case-sensitive names, rejecting escaped duplicates too.
func object(b []byte, names ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(b) {
		return nil, ErrRefused
	}
	d := json.NewDecoder(bytes.NewReader(b))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, ErrRefused
	}
	allowed := map[string]bool{}
	for _, n := range names {
		allowed[n] = true
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		t, err = d.Token()
		if err != nil {
			return nil, ErrRefused
		}
		k, ok := t.(string)
		if !ok || !allowed[k] || out[k] != nil {
			return nil, ErrRefused
		}
		var raw json.RawMessage
		if err = d.Decode(&raw); err != nil {
			return nil, ErrRefused
		}
		out[k] = raw
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') || len(out) != len(names) {
		return nil, ErrRefused
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrRefused
	}
	return out, nil
}
func text(b []byte) (string, error) {
	if len(b) == 0 || b[0] != '"' {
		return "", ErrRefused
	}
	var s string
	err := json.Unmarshal(b, &s)
	return s, err
}
func token(s string) bool { return runorigin.LowerHex(s, 32) && s != strings.Repeat("0", 32) }
func opaque(s string) bool {
	return len(s) > 0 && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func sourceReply(c net.Conn, expectedIncarnation string) ([]byte, error) {
	b, err := sourceRead(c)
	if err != nil {
		return nil, err
	}
	if m, e := object(b, "code", "source_incarnation"); e == nil {
		inc, e := text(m["source_incarnation"])
		if e != nil || !token(inc) || (expectedIncarnation != "" && inc != expectedIncarnation) {
			return nil, ErrRefused
		}
		code, e := text(m["code"])
		if e != nil {
			return nil, ErrRefused
		}
		switch code {
		case runorigin.NotSupported:
			return nil, ErrNotSupported
		case runorigin.Unavailable:
			return nil, ErrDependency
		default:
			return nil, ErrRefused
		}
	}
	return b, nil
}
func (Pinned) Hello(c net.Conn) (HelloMessage, error) {
	b, err := sourceReply(c, "")
	if err != nil {
		return HelloMessage{}, err
	}
	m, err := object(b, "capabilities", "source_incarnation")
	if err != nil {
		return HelloMessage{}, err
	}
	inc, err := text(m["source_incarnation"])
	if err != nil || !token(inc) {
		return HelloMessage{}, ErrRefused
	}
	var caps []string
	if len(m["capabilities"]) == 0 || m["capabilities"][0] != '[' || json.Unmarshal(m["capabilities"], &caps) != nil {
		return HelloMessage{}, ErrRefused
	}
	if len(caps) == 0 {
		return HelloMessage{Incarnation: inc}, nil
	}
	if len(caps) != 2 || caps[0] == caps[1] {
		return HelloMessage{}, ErrRefused
	}
	seen := map[string]bool{}
	for _, s := range caps {
		seen[s] = true
	}
	if !seen["source.origin.prepare/1"] || !seen["source.origin.validate/1"] {
		return HelloMessage{}, ErrRefused
	}
	return HelloMessage{Capability: true, Incarnation: inc}, nil
}

type pinnedResult struct {
	prepared         PreparedMessage
	origin           []byte
	requestID, nonce string
	input            runorigin.Principal
}

func preparedResult(b []byte) (pinnedResult, error) {
	var out pinnedResult
	m, err := object(b, "request_id", "launch_nonce", "input_principal", "identity_registration_number", "source_incarnation", "prepared_id", "origin")
	if err != nil {
		return out, err
	}
	values := map[string]string{}
	for _, n := range []string{"request_id", "launch_nonce", "identity_registration_number", "source_incarnation", "prepared_id"} {
		s, e := text(m[n])
		if e != nil {
			return out, ErrRefused
		}
		values[n] = s
	}
	if !runorigin.LowerHex(values["request_id"], 32) || !runorigin.LowerHex(values["launch_nonce"], 32) || !token(values["source_incarnation"]) || !token(values["prepared_id"]) || !opaque(values["identity_registration_number"]) {
		return out, ErrRefused
	}
	if _, err := object(m["input_principal"], "uid", "generation"); err != nil {
		return out, err
	}
	if err := json.Unmarshal(m["input_principal"], &out.input); err != nil || out.input.UID == 0 || !runorigin.ValidGeneration(out.input.Generation) {
		return out, ErrRefused
	}
	if err := CheckOrigin(m["origin"]); err != nil {
		return out, ErrRefused
	}
	// Use the public prepared type to bind the schema, while retaining origin bytes.
	var typed runorigin.PreparedOrigin
	if err := json.Unmarshal(b, &typed); err != nil || !typed.Origin.Valid() {
		return out, ErrRefused
	}
	input, _ := json.Marshal(typed.InputPrincipal)
	values["input_principal"] = string(input)
	values["origin"] = string(m["origin"])
	out.prepared = PreparedMessage{PreparedID: typed.PreparedID, Incarnation: typed.SourceIncarnation, Bindings: values}
	out.origin = append([]byte(nil), m["origin"]...)
	out.requestID, out.nonce = typed.RequestID, typed.LaunchNonce
	return out, nil
}
func (Pinned) Prepare(c net.Conn, req PrepareRequest) (PreparedMessage, error) {
	if !runorigin.LowerHex(req.RequestID, 32) || !runorigin.LowerHex(req.LaunchNonce, 32) || !opaque(req.Event.RoomID) || !opaque(req.Event.EventID) || req.InputPrincipal.UID == 0 || !runorigin.ValidGeneration(req.InputPrincipal.Generation) {
		return PreparedMessage{}, ErrRefused
	}
	if err := sourceWrite(c, struct {
		Op     string         `json:"op"`
		Params PrepareRequest `json:"params"`
	}{"source.origin.prepare/1", req}); err != nil {
		return PreparedMessage{}, err
	}
	b, err := sourceReply(c, req.SourceIncarnation)
	if err != nil {
		return PreparedMessage{}, err
	}
	out, err := preparedResult(b)
	if err != nil {
		return PreparedMessage{}, err
	}
	if out.requestID != req.RequestID || out.nonce != req.LaunchNonce || out.input != req.InputPrincipal {
		return PreparedMessage{}, ErrRefused
	}
	return out.prepared, nil
}
func (Pinned) Validate(c net.Conn, req ValidateRequest) (ValidatedMessage, error) {
	if !runorigin.LowerHex(req.RequestID, 32) || !runorigin.LowerHex(req.LaunchNonce, 32) || !token(req.PreparedID) || !token(req.SourceIncarnation) {
		return ValidatedMessage{}, ErrRefused
	}
	if err := sourceWrite(c, struct {
		Op     string          `json:"op"`
		Params ValidateRequest `json:"params"`
	}{"source.origin.validate/1", req}); err != nil {
		return ValidatedMessage{}, err
	}
	b, err := sourceReply(c, req.SourceIncarnation)
	if err != nil {
		return ValidatedMessage{}, err
	}
	out, err := preparedResult(b)
	if err != nil {
		return ValidatedMessage{}, err
	}
	if out.requestID != req.RequestID || out.nonce != req.LaunchNonce || out.prepared.PreparedID != req.PreparedID || out.prepared.Incarnation != req.SourceIncarnation {
		return ValidatedMessage{}, ErrRefused
	}
	return ValidatedMessage{PreparedID: out.prepared.PreparedID, Incarnation: out.prepared.Incarnation, Bindings: out.prepared.Bindings, Origin: out.origin}, nil
}
