package demolaunch

import "encoding/json"

type jsonRaw = json.RawMessage

type principal struct {
	Dev uint64 `json:"dev"`
	Dir string `json:"dir"`
	Ino uint64 `json:"ino"`
	UID uint32 `json:"uid"`
}
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
	Caller         principal `json:"caller"`
	ServiceAccount string    `json:"service_account"`
	Executor       string    `json:"executor"`
	ReceiptRoot    string    `json:"receipt_root"`
	StateDir       string    `json:"state_dir"`
}
type clientConfig struct {
	commonConfig
	Caller principal `json:"caller"`
}

const commonKeys = "connect_gid endpoint_dev endpoint_ino service_gid service_uid v caller"

func (c commonConfig) valid() bool {
	return c.Version == 1 && unmanaged(c.ServiceUID) && validNumber(c.ServiceGID) && validNumber(c.ConnectGID) && c.ConnectGID != c.ServiceGID && c.EndpointDev > 0 && c.EndpointDev <= maxIdentity && c.EndpointIno > 0 && c.EndpointIno <= maxIdentity
}
func (p principal) valid(service uint32) bool {
	return unmanaged(p.UID) && p.UID != service && p.Dev > 0 && p.Dev <= maxIdentity && p.Ino > 0 && p.Ino <= maxIdentity && validPath(p.Dir, 512)
}
func parseServer(b []byte) (serverConfig, error) {
	var c serverConfig
	m, e := object(b, serverConfigLimit, commonKeys+" service_account executor receipt_root state_dir", "")
	if e != nil || typed(b, &c) != nil || !c.commonConfig.valid() || !c.Caller.valid(c.ServiceUID) || !accountName.MatchString(c.ServiceAccount) || !validID(c.Executor) || !validPath(c.ReceiptRoot, 4095) || !validPath(c.StateDir, 4095) {
		return serverConfig{}, ErrConfig
	}
	if _, e = object(m["caller"], 1024, "dev dir ino uid", ""); e != nil {
		return serverConfig{}, ErrConfig
	}
	return c, nil
}
func parseClient(b []byte) (clientConfig, error) {
	var c clientConfig
	m, e := object(b, clientConfigLimit, commonKeys, "")
	if e != nil || typed(b, &c) != nil || !c.commonConfig.valid() || !c.Caller.valid(c.ServiceUID) {
		return clientConfig{}, ErrConfig
	}
	if _, e = object(m["caller"], 1024, "dev dir ino uid", ""); e != nil {
		return clientConfig{}, ErrConfig
	}
	return c, nil
}

type account struct {
	Name       string `json:"name"`
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
	Generation string `json:"generation"`
	Owner      string `json:"owner"`
}

func parseRoster(b []byte, owner string) ([]account, error) {
	m, e := object(b, 16384, "accounts", "")
	if e != nil {
		return nil, ErrConfig
	}
	var raw []jsonRaw
	if typed(m["accounts"], &raw) != nil || len(raw) < 1 || len(raw) > 20 {
		return nil, ErrConfig
	}
	result := make([]account, 0, len(raw))
	names := map[string]bool{}
	uids := map[uint32]bool{}
	gens := map[string]bool{}
	for _, b := range raw {
		var a account
		if _, e := object(b, 1024, "name uid gid generation owner", ""); e != nil || typed(b, &a) != nil || !accountName.MatchString(a.Name) || a.UID < 30000 || a.UID > 30019 || a.GID != a.UID || !generationID.MatchString(a.Generation) || a.Owner != owner || names[a.Name] || uids[a.UID] || gens[a.Generation] {
			return nil, ErrConfig
		}
		names[a.Name] = true
		uids[a.UID] = true
		gens[a.Generation] = true
		result = append(result, a)
	}
	return result, nil
}
