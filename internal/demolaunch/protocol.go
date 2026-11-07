// Package demolaunch is a demo cut, not production, not a conforming dispatcher.
package demolaunch

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
)

const protocol = "swarma-demo-dispatcher/1"
const endpointDir = "/run/swarma-demo-dispatcher"
const endpointName = "dispatcher.sock"
const lockName = "lifetime.lock"
const rosterPath = "/etc/swarma-demo/roster.json"
const serverConfigLimit = 16384
const clientConfigLimit = 4096
const maxIdentity = 9007199254740991

var (
	ErrInvalid     = errors.New("request_invalid")
	ErrConfig      = errors.New("config_invalid")
	ErrIdentity    = errors.New("identity_mismatch")
	ErrAncillary   = errors.New("ancillary_refused")
	ErrLimit       = errors.New("resource_limit")
	ErrOwnership   = errors.New("ownership_uncertain")
	ErrShutdown    = errors.New("shutdown_timeout")
	ErrUnsupported = errors.New("demo, not production: Linux required")
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
var accountName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var generationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var digestID = regexp.MustCompile(`^[0-9a-f]{64}$`)
var executionID = regexp.MustCompile(`^EX-[0-9a-f]{32}$`)

func validID(s string) bool     { return identifier.MatchString(s) }
func validNumber(v uint32) bool { return v > 0 && v < 4294967295 }
func unmanaged(v uint32) bool   { return validNumber(v) && (v < 30000 || v > 59999) }
func validPath(s string, limit int) bool {
	if len(s) < 2 || len(s) > limit || s[0] != '/' || path.Clean(s) != s {
		return false
	}
	for _, c := range []byte(s) {
		if c < 32 || c > 126 || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// Check duplicate keys at every depth before decoding into maps or structs.
// A bounded depth avoids recursive input amplification. Null is never a field value.
func unique(d *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrInvalid
	}
	t, e := d.Token()
	if e != nil || t == nil {
		return ErrInvalid
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return ErrInvalid
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return ErrInvalid
			}
			seen[s] = true
			if unique(d, depth+1) != nil {
				return ErrInvalid
			}
		}
	case '[':
		for d.More() {
			if unique(d, depth+1) != nil {
				return ErrInvalid
			}
		}
	default:
		return ErrInvalid
	}
	end, e := d.Token()
	if e != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
		return ErrInvalid
	}
	return nil
}
func object(b []byte, limit int, required, optional string) (map[string]json.RawMessage, error) {
	if len(b) == 0 || len(b) > limit {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if unique(d, 0) != nil {
		return nil, ErrInvalid
	}
	if _, e := d.Token(); e != io.EOF {
		return nil, ErrInvalid
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil || m == nil {
		return nil, ErrInvalid
	}
	allowed := map[string]bool{}
	for _, k := range strings.Fields(required) {
		allowed[k] = true
		if _, ok := m[k]; !ok {
			return nil, ErrInvalid
		}
	}
	for _, k := range strings.Fields(optional) {
		allowed[k] = true
	}
	for k := range m {
		if !allowed[k] {
			return nil, ErrInvalid
		}
	}
	return m, nil
}
func typed(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrInvalid
	}
	return nil
}
func readSnapshot(r io.Reader, limit int) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, int64(limit+1)))
	if e != nil {
		return nil, e
	}
	if len(b) == 0 || len(b) > limit {
		return nil, ErrConfig
	}
	return b, nil
}

type request struct {
	Op        string `json:"op"`
	RequestID string `json:"request_id,omitempty"`
	Account   string `json:"account"`
	Plan      string `json:"plan,omitempty"`
	Grace     int    `json:"grace_seconds,omitempty"`
}

func parseRequest(b []byte) (request, error) {
	var r request
	m, e := object(b, 131072, "op", "account request_id plan grace_seconds")
	if e != nil || typed(b, &r) != nil {
		return request{}, ErrInvalid
	}
	if r.Op != "demo.launch" && r.Op != "demo.stop" && r.Op != "demo.status" {
		return request{}, errors.New("operation_unknown")
	}
	if !accountName.MatchString(r.Account) {
		return request{}, ErrInvalid
	}
	switch r.Op {
	case "demo.launch":
		if len(m) != 4 || m["plan"] == nil || !validID(r.RequestID) || len(r.Plan) > 65536 {
			return request{}, ErrInvalid
		}
		decoded, e := base64.StdEncoding.Strict().DecodeString(r.Plan)
		if e != nil || len(decoded) > 49152 || base64.StdEncoding.EncodeToString(decoded) != r.Plan {
			return request{}, ErrInvalid
		}
	case "demo.stop":
		if m["plan"] != nil || !validID(r.RequestID) {
			return request{}, ErrInvalid
		}
		if m["grace_seconds"] == nil {
			r.Grace = 10
		}
		if r.Grace < 1 || r.Grace > 30 {
			return request{}, ErrInvalid
		}
	case "demo.status":
		if len(m) != 2 {
			return request{}, ErrInvalid
		}
	default:
		return request{}, errors.New("operation_unknown")
	}
	return r, nil
}

type receipt struct {
	ExecutionID string `json:"execution_id"`
	Epoch       uint64 `json:"epoch"`
	PID         uint32 `json:"pid"`
	StartTime   uint64 `json:"start_time"`
	BootID      string `json:"boot_id"`
	SessionID   uint32 `json:"session_id"`
}

func parseReceipt(b []byte, execution string) (*receipt, error) {
	var r receipt
	if _, e := object(b, 4096, "execution_id epoch pid start_time boot_id session_id", ""); e != nil || typed(b, &r) != nil || r.ExecutionID != execution || r.Epoch != 1 || r.PID == 0 || r.SessionID == 0 || r.StartTime == 0 || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(r.BootID) {
		return nil, ErrInvalid
	}
	return &r, nil
}

type response struct {
	State   string   `json:"state"`
	Code    string   `json:"code,omitempty"`
	Timing  string   `json:"timing,omitempty"`
	Receipt *receipt `json:"receipt,omitempty"`
	Attempt *attempt `json:"attempt,omitempty"`
	Closed  bool     `json:"closed"`
	Stopped bool     `json:"stopped,omitempty"`
}

func refusal(code string) response { return response{State: "refused", Code: code} }
func marshal(v any) []byte         { b, _ := json.Marshal(v); return b }

type attempt struct {
	Digest      string `json:"request_digest"`
	ExecutionID string `json:"execution_id"`
	Time        string `json:"time"`
}
