package m0

import (
	"bytes"
	"strings"

	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

// ConfigKind is the closed M0 daemon configuration discriminator. The file is
// separate from the transport server configuration, which stays unchanged.
const ConfigKind = "swarma-dispatcher m0-config/2"

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
		return nil, err
	}
	if !bytes.Equal(canon, b) {
		return nil, invalid("configuration must be canonical bytes")
	}
	callers, err := decodeCallers(v.member("callers"))
	if err != nil {
		return nil, err
	}
	if err := v.exactly("callers", "enabled", "entry", "kind", "recovery_attested", "state_dir", "supported_local_storage", "v"); err != nil {
		return nil, err
	}
	// Version 1 cannot silently acquire managed admission: it now refuses.
	if n, ok := v.member("v").number(); !ok || n != 2 {
		return nil, invalid("configuration version must be 2")
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
	return c, nil
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
