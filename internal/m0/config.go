package m0

import (
	"bytes"
	"strings"
)

// ConfigKind is the closed M0 daemon configuration discriminator. The file is
// separate from the transport server configuration, which stays unchanged.
const ConfigKind = "swarma-dispatcher m0-config/1"

const (
	EntryInitialize  = "initialize"
	EntryEstablished = "established"
)

// DaemonConfig selects M0 explicitly. Enabled false, or an absent file, keeps
// the daemon capacity-only. Entry chooses the journal entry explicitly; it is
// never inferred from missing or empty files.
type DaemonConfig struct {
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
	if err := v.exactly("enabled", "entry", "kind", "recovery_attested", "state_dir", "supported_local_storage", "v"); err != nil {
		return nil, err
	}
	if err := versionMember(v); err != nil {
		return nil, err
	}
	if err := constMember(v, "kind", ConfigKind); err != nil {
		return nil, err
	}
	c := &DaemonConfig{}
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
