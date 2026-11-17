package m0

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

// ConfigKind is the closed M0 daemon configuration discriminator. The file is
// separate from the transport server configuration, which stays unchanged.
const ConfigKind = "swarma-dispatcher m0-config/3"

const (
	EntryInitialize  = "initialize"
	EntryEstablished = "established"
)

// DaemonConfig selects M0 explicitly. Enabled false keeps the daemon
// capacity-only; a selected but unreadable file refuses Attach. Entry chooses
// the journal entry explicitly, never from missing or empty state files.
type DaemonConfig struct {
	Callers               []transport.Principal
	Enabled               bool
	Entry                 string
	RecoveryAttested      bool
	StateDir              string
	SupportedLocalStorage bool
	P17                   P17Config
}

// P17Config contains immutable service pins, never worker identity or progress.
type P17Config struct {
	SourceControlDir, SourceSocketName, SourceGeneration string
	SourceUID                                            uint32
	KeymakerSocketDir, KeymakerSocketName                string
	KeymakerUID, KeymakerGID, KeymakerMode               uint32
	StateDir, Station                                    string
	BindTimeoutMS                                        int64
}

func configMembers(v *value, prefix string, keys ...string) error {
	if v == nil || v.kind != kindObject {
		return invalid(prefix + ": object required")
	}
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	for _, m := range v.members {
		if !want[m.key] {
			return invalid(prefix + ": unknown member: " + m.key)
		}
	}
	for _, k := range keys {
		if v.member(k) == nil {
			return invalid(prefix + ": missing member: " + k)
		}
	}
	return nil
}

func decodeP17(v *value, m0dir string) (P17Config, error) {
	var p P17Config
	if err := configMembers(v, "p17", "bind_timeout_ms", "keymaker_gid", "keymaker_mode", "keymaker_socket_dir", "keymaker_socket_name", "keymaker_uid", "source_control_dir", "source_generation", "source_socket_name", "source_uid", "state_dir", "station"); err != nil {
		return p, err
	}
	for _, f := range []struct {
		name string
		dst  *string
	}{
		{"source_control_dir", &p.SourceControlDir}, {"source_socket_name", &p.SourceSocketName}, {"source_generation", &p.SourceGeneration},
		{"keymaker_socket_dir", &p.KeymakerSocketDir}, {"keymaker_socket_name", &p.KeymakerSocketName}, {"state_dir", &p.StateDir}, {"station", &p.Station},
	} {
		s, ok := v.member(f.name).text()
		if !ok || s == "" {
			return p, invalid("p17: member spelling: " + f.name)
		}
		*f.dst = s
	}
	for _, f := range []struct {
		name string
		dst  *uint32
		max  int64
	}{
		{"source_uid", &p.SourceUID, 4294967294}, {"keymaker_uid", &p.KeymakerUID, 4294967294}, {"keymaker_gid", &p.KeymakerGID, 4294967294}, {"keymaker_mode", &p.KeymakerMode, 0777},
	} {
		n, ok := v.member(f.name).number()
		if !ok || n < 1 || n > f.max {
			return p, invalid("p17: member range: " + f.name)
		}
		*f.dst = uint32(n)
	}
	for _, f := range []struct{ name, path string }{{"source_control_dir", p.SourceControlDir}, {"keymaker_socket_dir", p.KeymakerSocketDir}, {"state_dir", p.StateDir}} {
		if !validStatePath(f.path) {
			return p, invalid("p17: member path: " + f.name)
		}
	}
	if p.StateDir == m0dir || strings.HasPrefix(p.StateDir, m0dir+"/") || strings.HasPrefix(m0dir, p.StateDir+"/") {
		return p, invalid("p17: state_dir overlaps M0 state_dir")
	}
	if p.SourceSocketName != "origin.sock" {
		return p, invalid("p17: source_socket_name must be origin.sock")
	}
	if len(p.KeymakerSocketName) > 255 || p.KeymakerSocketName == "." || p.KeymakerSocketName == ".." || strings.Contains(p.KeymakerSocketName, "/") {
		return p, invalid("p17: member spelling: keymaker_socket_name")
	}
	if !isLowerHex(p.SourceGeneration, 32) {
		return p, invalid("p17: member spelling: source_generation")
	}
	n, err := strconv.ParseUint(p.Station, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != p.Station {
		return p, invalid("p17: member spelling: station")
	}
	var ok bool
	if p.BindTimeoutMS, ok = v.member("bind_timeout_ms").number(); !ok || p.BindTimeoutMS < 1 || p.BindTimeoutMS > 2000 {
		return p, invalid("p17: member range: bind_timeout_ms")
	}
	return p, nil
}

func validStatePath(s string) bool {
	if len(s) < 2 || len(s) > 4095 || s[0] != '/' || strings.HasSuffix(s, "/") || strings.Contains(s, "//") || strings.Contains(s, "/./") || strings.Contains(s, "/../") || strings.HasSuffix(s, "/.") || strings.HasSuffix(s, "/..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e || s[i] == '"' || s[i] == '\\' {
			return false
		}
	}
	return true
}

// ParseConfig accepts only canonical bytes of the closed configuration object.
func ParseConfig(b []byte) (*DaemonConfig, error) {
	v, canon, err := canonicalize(b, configLimits)
	if err != nil {
		if r, ok := err.(*Refusal); ok && r.Detail == "duplicate member name" {
			if named := duplicateConfigMember(b); named != nil {
				return nil, named
			}
		}
		return nil, err
	}
	if !bytes.Equal(canon, b) {
		return nil, invalid("configuration must be canonical bytes")
	}
	callers, err := decodeCallers(v.member("callers"))
	if err != nil {
		return nil, err
	}
	if err := configMembers(v, "configuration", "callers", "enabled", "entry", "kind", "p17", "recovery_attested", "state_dir", "supported_local_storage", "v"); err != nil {
		return nil, err
	}
	// Older versions never silently acquire new installation semantics.
	if n, ok := v.member("v").number(); !ok || n != 3 {
		return nil, invalid("configuration version must be 3")
	}
	if err := constMember(v, "kind", ConfigKind); err != nil {
		return nil, err
	}
	c := &DaemonConfig{Callers: callers}
	var ok bool
	if c.Enabled, ok = v.member("enabled").flag(); !ok {
		return nil, invalid("member type: enabled")
	}
	if c.RecoveryAttested, ok = v.member("recovery_attested").flag(); !ok {
		return nil, invalid("member type: recovery_attested")
	}
	if c.SupportedLocalStorage, ok = v.member("supported_local_storage").flag(); !ok {
		return nil, invalid("member type: supported_local_storage")
	}
	if c.Entry, ok = v.member("entry").text(); !ok || (c.Entry != EntryInitialize && c.Entry != EntryEstablished) {
		return nil, invalid("member constant: entry")
	}
	if c.StateDir, ok = v.member("state_dir").text(); !ok || !validStatePath(c.StateDir) {
		return nil, invalid("member spelling: state_dir")
	}
	c.P17, err = decodeP17(v.member("p17"), c.StateDir)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// duplicateConfigMember runs only after the bounded parser found a duplicate.
// It supplies the member name without normalizing any accepted configuration.
func duplicateConfigMember(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	var walk func(string) error
	walk = func(prefix string) error {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if t == json.Delim('{') {
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return invalid("configuration member name")
				}
				if seen[name] {
					return invalid("configuration duplicate member: " + prefix + name)
				}
				seen[name] = true
				if err := walk(prefix + name + "."); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		if t == json.Delim('[') {
			for dec.More() {
				if err := walk(prefix); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	return walk("")
}

// decodeCallers names the installation member on every shape/range refusal.
func decodeCallers(v *value) ([]transport.Principal, error) {
	if v == nil || v.kind != kindArray || len(v.items) < 1 || len(v.items) > 4 {
		return nil, invalid("callers: expected 1..4 principals")
	}
	out := make([]transport.Principal, 0, len(v.items))
	for _, item := range v.items {
		if err := item.exactly("dev", "dir", "ino", "uid"); err != nil {
			return nil, invalid("callers: principal members must be dev, dir, ino, uid")
		}
		uid, uok := item.member("uid").number()
		dev, dok := item.member("dev").number()
		ino, iok := item.member("ino").number()
		dir, pok := item.member("dir").text()
		if !uok || uid < uidMin || uid > uidMax || !dok || dev < 1 || !iok || ino < 1 || !pok {
			return nil, invalid("callers: invalid principal uid, dev, ino or dir")
		}
		p := transport.Principal{UID: uint32(uid), Dev: uint64(dev), Ino: uint64(ino), Dir: dir}
		if !p.ValidManaged() {
			return nil, invalid("callers: invalid principal pin")
		}
		if len(out) > 0 && out[len(out)-1].UID >= p.UID {
			return nil, invalid("callers: UIDs must be strictly ascending")
		}
		out = append(out, p)
	}
	return out, nil
}
