package demolaunch

import (
	"encoding/json"
	"regexp"
)

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
	if e != nil || typed(b, &c) != nil || !c.commonConfig.valid() || !c.Caller.valid(c.ServiceUID) || !accountName.MatchString(c.ServiceAccount) || executorPath(c.Executor) == "" || !validPath(c.ReceiptRoot, 4095) || !validPath(c.StateDir, 4095) {
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
}

const rosterLimit = 48 * 1024
const dispatcherName = "swarma-demo-dispatcher"
const dispatcherUID = 1101

func executorPath(id string) string {
	switch id {
	case "swarma-demo-executor":
		return "/usr/local/libexec/swarma/swarma-demo-executor"
	case "swarma-demo-codex-executor":
		return "/usr/local/libexec/swarma/swarma-demo-codex-executor"
	}
	return ""
}

// Configuration binding is supplied only by the protected server snapshot.
// Operator IDs are JSON integers; no helper-specific operator is compiled in.
func parseRoster(b []byte, c serverConfig) ([]account, error) {
	m, e := object(b, rosterLimit, "format source_revision boot_id operator dispatcher executor runtime accounts", "")
	if e != nil {
		return nil, ErrConfig
	}
	var format, revision, boot string
	if typed(m["format"], &format) != nil || format != "swarma-user-manager demo-roster/1" || typed(m["source_revision"], &revision) != nil || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) || typed(m["boot_id"], &boot) != nil || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(boot) {
		return nil, ErrConfig
	}
	var operator struct {
		Name string
		UID  json.Number
		GID  json.Number
	}
	if _, e := object(m["operator"], rosterLimit, "name uid gid", ""); e != nil || typed(m["operator"], &operator) != nil || !accountName.MatchString(operator.Name) || !jsonInteger(mustMember(m["operator"], "uid")) || !jsonInteger(mustMember(m["operator"], "gid")) {
		return nil, ErrConfig
	}
	var dispatcher struct {
		Name       string
		UID        uint32
		GID        uint32
		Principal  string
		Generation string
	}
	if _, e := object(m["dispatcher"], rosterLimit, "name uid gid principal generation", ""); e != nil || typed(m["dispatcher"], &dispatcher) != nil || dispatcher.Name != dispatcherName || dispatcher.Principal != dispatcherName || dispatcher.UID != dispatcherUID || dispatcher.GID != dispatcherUID || !validGeneration(dispatcher.Generation) || dispatcher.Name == operator.Name || operator.UID.String() == jsonNumberUID(dispatcher.UID) {
		return nil, ErrConfig
	}
	var executor struct {
		ID     string
		Path   string
		SHA256 string
	}
	if _, e := object(m["executor"], rosterLimit, "id path sha256", ""); e != nil || typed(m["executor"], &executor) != nil || executorPath(executor.ID) == "" || executor.Path != executorPath(executor.ID) || !digestID.MatchString(executor.SHA256) {
		return nil, ErrConfig
	}
	runtime, e := object(m["runtime"], rosterLimit, "admitted inventory_qualified creation_exclusion_qualified interpreter_qualified", "")
	if e != nil {
		return nil, ErrConfig
	}
	for _, b := range runtime {
		var v bool
		if typed(b, &v) != nil {
			return nil, ErrConfig
		}
	}
	// These checks join the roster to loadServerInputs' protected configuration.
	if operator.UID.String() != jsonNumberUID(c.Caller.UID) || c.ServiceAccount != dispatcher.Principal || c.ServiceUID != dispatcher.UID || c.Executor != executor.ID {
		return nil, ErrConfig
	}
	operatorUID := c.Caller.UID
	if operatorUID >= 30000 && operatorUID <= 30019 {
		return nil, ErrConfig
	}
	var raw []jsonRaw
	if typed(m["accounts"], &raw) != nil || len(raw) != 3 {
		return nil, ErrConfig
	}
	result := make([]account, 0, len(raw))
	names := map[string]bool{}
	uids := map[uint32]bool{}
	gens := map[string]bool{dispatcher.Generation: true}
	for _, b := range raw {
		var a struct {
			account
			Owner      string `json:"owning_dispatcher"`
			Home       string `json:"home"`
			Workspace  string `json:"workspace"`
			CopyDigest string `json:"copy_digest"`
		}
		if _, e := object(b, rosterLimit, "name uid gid generation owning_dispatcher home workspace copy_digest", ""); e != nil || typed(b, &a) != nil || (a.Name != "swarma-demo-01" && a.Name != "swarma-demo-02" && a.Name != "swarma-demo-03") || a.UID < 30000 || a.UID > 30019 || a.GID != a.UID || !validGeneration(a.Generation) || a.Owner != dispatcher.Principal || a.Home != "/srv/swarma-demo/home/"+a.Name || a.Workspace != "/srv/swarma-demo/work/"+a.Name || !digestID.MatchString(a.CopyDigest) || names[a.Name] || uids[a.UID] || gens[a.Generation] {
			return nil, ErrConfig
		}
		names[a.Name] = true
		uids[a.UID] = true
		gens[a.Generation] = true
		result = append(result, a.account)
	}
	return result, nil
}

func jsonNumberUID(uid uint32) string { b, _ := json.Marshal(uid); return string(b) }
func jsonInteger(b []byte) bool       { return regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`).Match(b) }
func mustMember(b []byte, key string) []byte {
	var m map[string]jsonRaw
	_ = json.Unmarshal(b, &m)
	return m[key]
}
