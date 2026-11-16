package transport

import (
	"encoding/json"
	"path"
)

const (
	endpointDir       = "/opt/swarma/run/dispatcher"
	endpointName      = "dispatcher.sock"
	lockName          = "lifetime.lock"
	serverConfigLimit = 16384
	clientConfigLimit = 4096
	maxIdentity       = 9007199254740991
)

// Principal pins a numeric caller UID to its protected directory identity.
type Principal struct {
	Dev uint64 `json:"dev"`
	Dir string `json:"dir"`
	Ino uint64 `json:"ino"`
	UID uint32 `json:"uid"`
}
type principal = Principal

type commonConfig struct {
	ConnectGID  uint32 `json:"connect_gid"`
	EndpointDev uint64 `json:"endpoint_dev"`
	EndpointIno uint64 `json:"endpoint_ino"`
	ServiceGID  uint32 `json:"service_gid"`
	ServiceUID  uint32 `json:"service_uid"`
	Version     uint32 `json:"v"`
}
type serverConfig struct {
	commonConfig
	Callers []principal `json:"callers"`
}
type clientConfig struct {
	commonConfig
	Caller principal `json:"caller"`
}

func validNumber(v uint32) bool { return v > 0 && v < 4294967295 }
func managed(v uint32) bool     { return v >= 30000 && v <= 59999 }
func unmanaged(v uint32) bool   { return validNumber(v) && !managed(v) }
func validPath(v string, limit int) bool {
	if len(v) < 2 || len(v) > limit || v[0] != '/' || path.Clean(v) != v {
		return false
	}
	for i := range len(v) {
		if v[i] < 0x20 || v[i] > 0x7e || v[i] == '"' || v[i] == '\\' {
			return false
		}
	}
	return true
}
func (c commonConfig) valid() bool {
	return c.Version == 1 && unmanaged(c.ServiceUID) && validNumber(c.ServiceGID) && validNumber(c.ConnectGID) && c.ConnectGID != c.ServiceGID && c.EndpointDev > 0 && c.EndpointDev <= maxIdentity && c.EndpointIno > 0 && c.EndpointIno <= maxIdentity
}
func (p Principal) validPin() bool {
	return p.Dev > 0 && p.Dev <= maxIdentity && p.Ino > 0 && p.Ino <= maxIdentity && validPath(p.Dir, 512)
}

// ValidManaged checks a managed principal's closed numeric and path bounds.
// Filesystem identity and service-UID exclusion are checked by the transport.
func (p Principal) ValidManaged() bool { return managed(p.UID) && p.validPin() }

func (p Principal) valid(service uint32) bool {
	return unmanaged(p.UID) && p.UID != service && p.validPin()
}
func parseServer(b []byte) (serverConfig, error) {
	var c serverConfig
	if !configKeys(b, true) || decodeConfig(b, &c, serverConfigLimit) != nil || !c.commonConfig.valid() || len(c.Callers) < 1 || len(c.Callers) > 32 {
		return serverConfig{}, ErrConfig
	}
	for i, p := range c.Callers {
		if !p.valid(c.ServiceUID) || i > 0 && c.Callers[i-1].UID >= p.UID {
			return serverConfig{}, ErrConfig
		}
	}
	return c, nil
}
func parseClient(b []byte) (clientConfig, error) {
	var c clientConfig
	if !configKeys(b, false) || decodeConfig(b, &c, clientConfigLimit) != nil || !c.commonConfig.valid() || !c.Caller.valid(c.ServiceUID) {
		return clientConfig{}, ErrConfig
	}
	return c, nil
}

// encoding/json matches field names case-insensitively, so exact member names
// are independently checked after byte gating and before typed acceptance.
func configKeys(b []byte, server bool) bool {
	limit := clientConfigLimit
	if server {
		limit = serverConfigLimit
	}
	if len(b) == 0 || len(b) > limit || !scanCanonical(b, 3, false) {
		return false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(b, &root) != nil {
		return false
	}
	role := "caller"
	if server {
		role = "callers"
	}
	expected := []string{role, "connect_gid", "endpoint_dev", "endpoint_ino", "service_gid", "service_uid", "v"}
	if len(root) != len(expected) {
		return false
	}
	for _, k := range expected {
		if _, ok := root[k]; !ok {
			return false
		}
	}
	check := func(b json.RawMessage) bool {
		var p map[string]json.RawMessage
		if json.Unmarshal(b, &p) != nil || len(p) != 4 {
			return false
		}
		for _, k := range []string{"dev", "dir", "ino", "uid"} {
			if _, ok := p[k]; !ok {
				return false
			}
		}
		return true
	}
	if !server {
		return check(root[role])
	}
	var entries []json.RawMessage
	if json.Unmarshal(root[role], &entries) != nil || len(entries) < 1 || len(entries) > 32 {
		return false
	}
	for _, p := range entries {
		if !check(p) {
			return false
		}
	}
	return true
}
