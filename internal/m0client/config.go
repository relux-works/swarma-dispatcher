package m0client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strconv"
	"time"
)

const (
	// MaxConfigBytes is the transport client snapshot limit, including tool members.
	MaxConfigBytes      = 4096
	MaxStageTimeoutMs   = 2000
	MaxSessionTimeoutMs = 5000
	maxPathBytes        = 4095
	maxSerial           = 9007199254740991
	endpointDir         = "/opt/swarma/run/dispatcher"
	endpointName        = "dispatcher.sock"
)

// Principal pins one caller UID and its enrolled directory identity.
type Principal struct {
	UID      uint32
	Dir      string
	Dev, Ino uint64
}

// Config is a closed transport client snapshot with local signing-plan,
// key and state paths. Request members come only from a signing plan.
type Config struct {
	Caller                                      Principal
	EndpointDev, EndpointIno                    uint64
	ServiceUID, ServiceGID, ConnectGID          uint32
	StageTimeout, SessionTimeout                time.Duration
	KeyFile, StateDir, DispatchPlan, CancelPlan string
	host                                        host
}

var errConfig = errors.New("configuration is not the closed form")

type node struct {
	text    string
	number  string
	members map[string]*node
	kind    byte
}

// parseTree gates canonical ASCII bytes before JSON recognition: no escapes,
// whitespace, duplicate/nonascending keys or alternate integer spellings.
func parseTree(b []byte) (*node, error) {
	inside := false
	for _, ch := range b {
		if ch < 0x20 || ch > 0x7e || ch == '\\' || (!inside && ch == ' ') {
			return nil, errConfig
		}
		if ch == '"' {
			inside = !inside
		}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	n, err := parseNode(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errConfig
	}
	return n, nil
}

func parseNode(d *json.Decoder, depth int) (*node, error) {
	t, err := d.Token()
	if err != nil {
		return nil, errConfig
	}
	switch v := t.(type) {
	case string:
		return &node{kind: 's', text: v}, nil
	case json.Number:
		s := v.String()
		u, err := strconv.ParseUint(s, 10, 64)
		if err != nil || strconv.FormatUint(u, 10) != s {
			return nil, errConfig
		}
		return &node{kind: 'n', number: s}, nil
	case json.Delim:
		if v != '{' || depth >= 3 {
			return nil, errConfig
		}
		n := &node{kind: 'o', members: map[string]*node{}}
		last := ""
		for d.More() {
			k, err := d.Token()
			name, ok := k.(string)
			if err != nil || !ok || name <= last {
				return nil, errConfig
			}
			last = name
			child, err := parseNode(d, depth+1)
			if err != nil {
				return nil, err
			}
			n.members[name] = child
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return nil, errConfig
		}
		return n, nil
	}
	return nil, errConfig
}

func (n *node) exactly(keys ...string) bool {
	if n == nil || n.kind != 'o' || len(n.members) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := n.members[k]; !ok {
			return false
		}
	}
	return true
}
func (n *node) str(key string) (string, bool) {
	c := n.members[key]
	if c == nil || c.kind != 's' {
		return "", false
	}
	return c.text, true
}
func (n *node) num(key string, lo, hi uint64) (uint64, bool) {
	c := n.members[key]
	if c == nil || c.kind != 'n' {
		return 0, false
	}
	v, err := strconv.ParseUint(c.number, 10, 64)
	return v, err == nil && v >= lo && v <= hi
}
func printable(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e || s[i] == '"' || s[i] == '\\' {
			return false
		}
	}
	return true
}
func validAbsolute(s string) bool {
	return len(s) >= 2 && len(s) <= maxPathBytes && s[0] == '/' && path.Clean(s) == s && printable(s)
}

// ParseConfig uses an independent closed form: transport's parser is private
// and excludes managed callers. Member names and all other transport limits
// are preserved; M0 additionally checks the plan's managed association UID.
func ParseConfig(b []byte) (*Config, error) {
	if len(b) == 0 || len(b) > MaxConfigBytes {
		return nil, errConfig
	}
	r, err := parseTree(b)
	if err != nil {
		return nil, err
	}
	if !r.exactly("caller", "cancel_plan", "connect_gid", "dispatch_plan", "endpoint_dev", "endpoint_ino", "key_file", "service_gid", "service_uid", "session_timeout_ms", "stage_timeout_ms", "state_dir", "v") {
		return nil, errConfig
	}
	p := r.members["caller"]
	if !p.exactly("dev", "dir", "ino", "uid") {
		return nil, errConfig
	}
	ok := true
	num := func(n *node, k string, lo, hi uint64) uint64 { v, good := n.num(k, lo, hi); ok = ok && good; return v }
	text := func(n *node, k string) string { v, good := n.str(k); ok = ok && good; return v }
	num(r, "v", 1, 1)
	c := &Config{
		Caller:      Principal{UID: uint32(num(p, "uid", 1, 4294967294)), Dir: text(p, "dir"), Dev: num(p, "dev", 1, maxSerial), Ino: num(p, "ino", 1, maxSerial)},
		EndpointDev: num(r, "endpoint_dev", 1, maxSerial), EndpointIno: num(r, "endpoint_ino", 1, maxSerial),
		ServiceUID: uint32(num(r, "service_uid", 1, 4294967294)), ServiceGID: uint32(num(r, "service_gid", 1, 4294967294)), ConnectGID: uint32(num(r, "connect_gid", 1, 4294967294)),
		StageTimeout:   time.Duration(num(r, "stage_timeout_ms", 1, MaxStageTimeoutMs)) * time.Millisecond,
		SessionTimeout: time.Duration(num(r, "session_timeout_ms", 1, MaxSessionTimeoutMs)) * time.Millisecond,
		KeyFile:        text(r, "key_file"), StateDir: text(r, "state_dir"), DispatchPlan: text(r, "dispatch_plan"), CancelPlan: text(r, "cancel_plan"),
	}
	if !ok || c.ServiceUID >= 30000 && c.ServiceUID <= 59999 || c.ServiceUID == c.Caller.UID || c.ServiceGID == c.ConnectGID || len(c.Caller.Dir) > 512 {
		return nil, errConfig
	}
	for _, s := range []string{c.Caller.Dir, c.KeyFile, c.StateDir, c.DispatchPlan, c.CancelPlan} {
		if !validAbsolute(s) {
			return nil, errConfig
		}
	}
	return c, nil
}
