package m0client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strconv"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

const (
	// ConfigKind is the closed caller configuration discriminator.
	ConfigKind = "swarma-dispatcher m0-caller-config/1"
	// MaxConfigBytes bounds the configuration file before parsing.
	MaxConfigBytes = 8192
	// Stage and session timeouts may only narrow the transport contract's
	// 2 s stage and 5 s session budgets.
	MaxStageTimeoutMs   = 2000
	MaxSessionTimeoutMs = 5000

	maxPathBytes = 4095
	maxNameBytes = 64
	probeID      = "00000000000000000000000000000000"
)

// Config is one validated caller configuration: the values of R that are not
// per-request, the client's transport pins, the key file and the state
// directory.
type Config struct {
	Receiver    string
	Policy      m0.PolicyPin
	Association m0.Association
	Worker      m0.Worker
	Plan        m0.Plan

	EndpointDir    string
	Endpoint       string
	ServiceUID     uint32
	ConnectGID     uint32
	StageTimeout   time.Duration
	SessionTimeout time.Duration

	KeyFile  string
	StateDir string
}

var errConfig = errors.New("configuration is not the closed form")

type node struct {
	text    string
	number  string
	members map[string]*node
	kind    byte // 's' string, 'n' number, 'o' object
}

// parseTree reads one JSON document of objects, strings and unsigned integer
// tokens. Duplicate member names, arrays, booleans, null, fractions and
// trailing bytes refuse; nothing is normalized into acceptance.
func parseTree(b []byte) (*node, error) {
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
		return &node{kind: 'n', number: v.String()}, nil
	case json.Delim:
		if v != '{' || depth >= 2 {
			return nil, errConfig
		}
		n := &node{kind: 'o', members: map[string]*node{}}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, errConfig
			}
			name, ok := k.(string)
			if !ok {
				return nil, errConfig
			}
			if _, dup := n.members[name]; dup {
				return nil, errConfig
			}
			child, err := parseNode(d, depth+1)
			if err != nil {
				return nil, err
			}
			n.members[name] = child
		}
		if _, err := d.Token(); err != nil {
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

// num accepts only the shortest unsigned decimal spelling within [lo, hi].
func (n *node) num(key string, lo, hi uint64) (uint64, bool) {
	c := n.members[key]
	if c == nil || c.kind != 'n' {
		return 0, false
	}
	v, err := strconv.ParseUint(c.number, 10, 64)
	if err != nil || strconv.FormatUint(v, 10) != c.number || v < lo || v > hi {
		return 0, false
	}
	return v, true
}

func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e || s[i] == '"' || s[i] == '\\' {
			return false
		}
	}
	return true
}

func validAbsolute(s string) bool {
	return len(s) >= 2 && len(s) <= maxPathBytes && s[0] == '/' && path.Clean(s) == s && printable(s)
}

func validName(s string) bool {
	if len(s) < 1 || len(s) > maxNameBytes || s == "." || s == ".." || !printable(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return false
		}
	}
	return true
}

const maxSerial = 9007199254740991

// ParseConfig accepts exactly the closed configuration object. Every member is
// required and unknown members refuse at every depth. The request values are
// then validated by the daemon's own closed-form decoder, so a configuration
// this function accepts can only yield requests of the daemon's grammar.
func ParseConfig(b []byte) (*Config, error) {
	if len(b) == 0 || len(b) > MaxConfigBytes {
		return nil, errConfig
	}
	root, err := parseTree(b)
	if err != nil {
		return nil, err
	}
	if !root.exactly("association", "connect_gid", "endpoint", "endpoint_dir", "key_file", "kind", "plan", "policy", "receiver", "service_uid", "session_timeout_ms", "stage_timeout_ms", "state_dir", "v", "worker") {
		return nil, errConfig
	}
	if v, ok := root.num("v", 1, 1); !ok || v != 1 {
		return nil, errConfig
	}
	if k, ok := root.str("kind"); !ok || k != ConfigKind {
		return nil, errConfig
	}
	c := &Config{}
	ok := true
	take := func(n *node, key string) string {
		s, good := n.str(key)
		ok = ok && good
		return s
	}
	count := func(n *node, key string, lo, hi uint64) uint64 {
		v, good := n.num(key, lo, hi)
		ok = ok && good
		return v
	}
	c.Receiver = take(root, "receiver")
	policy, association, worker, plan := root.members["policy"], root.members["association"], root.members["worker"], root.members["plan"]
	if !policy.exactly("id", "revision") || !association.exactly("generation", "id", "key", "master_epoch", "registration_number", "revision", "uid") || !worker.exactly("generation", "uid") || !plan.exactly("exec_profile", "harness", "profile", "worker_plan") {
		return nil, errConfig
	}
	c.Policy = m0.PolicyPin{ID: take(policy, "id"), Revision: count(policy, "revision", 1, maxSerial)}
	c.Association = m0.Association{
		ID:                 take(association, "id"),
		Revision:           count(association, "revision", 1, maxSerial),
		UID:                uint32(count(association, "uid", 1, 4294967294)),
		Generation:         take(association, "generation"),
		Key:                take(association, "key"),
		RegistrationNumber: count(association, "registration_number", 1, maxSerial),
		MasterEpoch:        count(association, "master_epoch", 1, maxSerial),
	}
	c.Worker = m0.Worker{UID: uint32(count(worker, "uid", 1, 4294967294)), Generation: take(worker, "generation")}
	c.Plan = m0.Plan{WorkerPlan: take(plan, "worker_plan"), Profile: take(plan, "profile"), Harness: take(plan, "harness"), ExecProfile: take(plan, "exec_profile")}
	c.EndpointDir = take(root, "endpoint_dir")
	c.Endpoint = take(root, "endpoint")
	c.ServiceUID = uint32(count(root, "service_uid", 1, 4294967294))
	c.ConnectGID = uint32(count(root, "connect_gid", 1, 4294967294))
	c.StageTimeout = time.Duration(count(root, "stage_timeout_ms", 1, MaxStageTimeoutMs)) * time.Millisecond
	c.SessionTimeout = time.Duration(count(root, "session_timeout_ms", 1, MaxSessionTimeoutMs)) * time.Millisecond
	c.KeyFile = take(root, "key_file")
	c.StateDir = take(root, "state_dir")
	if !ok {
		return nil, errConfig
	}
	// The transport contract admits only a nonroot service UID outside the
	// managed range.
	if c.ServiceUID >= 30000 && c.ServiceUID <= 59999 {
		return nil, errConfig
	}
	if !validAbsolute(c.EndpointDir) || !validName(c.Endpoint) || !validAbsolute(c.KeyFile) || !validAbsolute(c.StateDir) {
		return nil, errConfig
	}
	// The daemon's decoder is the judge of every request spelling and range.
	doc, err := unsignedDocument(c, m0.OpDispatch, probeID, "")
	if err != nil {
		return nil, errConfig
	}
	if _, err := m0.DecodeUnsigned(doc); err != nil {
		return nil, errConfig
	}
	return c, nil
}
