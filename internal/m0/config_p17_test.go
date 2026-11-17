package m0

import (
	"encoding/json"
	"strings"
	"testing"
)

const fixtureP17 = `{"bind_timeout_ms":1000,"keymaker_gid":30004,"keymaker_mode":432,"keymaker_socket_dir":"/run/keymaker","keymaker_socket_name":"request.sock","keymaker_uid":30003,"source_control_dir":"/run/source","source_generation":"11111111111111111111111111111111","source_socket_name":"origin.sock","source_uid":30002,"state_dir":"/var/lib/p17","station":"1"}`
const configP17Control = `{"callers":[{"dev":1,"dir":"/example/caller","ino":2,"uid":30001}],"enabled":false,"entry":"initialize","kind":"swarma-dispatcher m0-config/3","p17":` + fixtureP17 + `,"recovery_attested":true,"state_dir":"/var/lib/m0","supported_local_storage":true,"v":3}`

// Claim: configuration /3 validates every immutable P17 pin even while disabled, refuses /1 and /2, and excludes worker identities and mutable installation facts.
func TestP17Configuration(t *testing.T) {
	good := configP17Control
	cfg, err := ParseConfig([]byte(good))
	if err != nil || cfg.P17.SourceUID != 30002 || cfg.P17.BindTimeoutMS != 1000 || cfg.P17.Station != "1" {
		t.Fatalf("valid disabled pins: %+v %v", cfg, err)
	}
	for _, row := range []struct{ old, replacement, detail string }{
		{`"v":3`, `"v":2`, "configuration version must be 3"}, {`"v":3`, `"v":1`, "configuration version must be 3"},
		{`"p17":` + fixtureP17 + `,`, "", "configuration: missing member: p17"},
		{`"station":"1"`, `"station":"1","worker_uid":30001`, "p17: unknown member: worker_uid"},
		{`"bind_timeout_ms":1000,`, `"bind_timeout_ms":1000,"installation":"fresh",`, "p17: unknown member: installation"},
		{`"source_uid":30002`, `"source_uid":0`, "p17: member range: source_uid"},
		{`"keymaker_uid":30003`, `"keymaker_uid":4294967295`, "p17: member range: keymaker_uid"},
		{`"keymaker_gid":30004`, `"keymaker_gid":-1`, "p17: member range: keymaker_gid"},
		{`"keymaker_mode":432`, `"keymaker_mode":512`, "p17: member range: keymaker_mode"},
		{`"bind_timeout_ms":1000`, `"bind_timeout_ms":2001`, "p17: member range: bind_timeout_ms"},
		{`/run/source`, `run/source`, "p17: member path: source_control_dir"},
		{`/run/source`, `/run/../source`, "p17: member path: source_control_dir"},
		{`/run/keymaker`, `/run//keymaker`, "p17: member path: keymaker_socket_dir"},
		{`origin.sock`, `other.sock`, "p17: source_socket_name must be origin.sock"},
		{`request.sock`, `../request.sock`, "p17: member spelling: keymaker_socket_name"},
		{`11111111111111111111111111111111`, `AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA`, "p17: member spelling: source_generation"},
		{`/var/lib/p17`, `/var/lib/m0`, "p17: state_dir overlaps M0 state_dir"},
		{`/var/lib/p17`, `/var/lib/m0/p17`, "p17: state_dir overlaps M0 state_dir"},
		{`/var/lib/p17`, `/var/lib`, "p17: state_dir overlaps M0 state_dir"},
		{`"station":"1"`, `"station":"01"`, "p17: member spelling: station"},
		{`"source_uid":30002`, `"source_uid":30002,"source_uid":30002`, "configuration duplicate member: p17.source_uid"},
		{`"source_uid":30002`, `"source_uid":30002,"source_\u0075id":30002`, "configuration duplicate member: p17.source_uid"},
	} {
		_, err := ParseConfig([]byte(strings.Replace(good, row.old, row.replacement, 1)))
		r, ok := err.(*Refusal)
		if !ok || r.Code != "request_invalid" || r.Detail != row.detail {
			t.Errorf("expected request_invalid %q, got %v", row.detail, err)
		}
		if _, err := ParseConfig([]byte(good)); err != nil {
			t.Fatalf("valid neighbor: %v", err)
		}
	}
}

// Claim: every P17 member is required and named on omission; the complete immutable pin object remains a valid control for each omission.
func TestP17RequiredMembers(t *testing.T) {
	for _, row := range []struct{ name, detail string }{
		{"bind_timeout_ms", "p17: missing member: bind_timeout_ms"}, {"keymaker_gid", "p17: missing member: keymaker_gid"}, {"keymaker_mode", "p17: missing member: keymaker_mode"},
		{"keymaker_socket_dir", "p17: missing member: keymaker_socket_dir"}, {"keymaker_socket_name", "p17: missing member: keymaker_socket_name"}, {"keymaker_uid", "p17: missing member: keymaker_uid"},
		{"source_control_dir", "p17: missing member: source_control_dir"}, {"source_generation", "p17: missing member: source_generation"}, {"source_socket_name", "p17: missing member: source_socket_name"},
		{"source_uid", "p17: missing member: source_uid"}, {"state_dir", "p17: missing member: state_dir"}, {"station", "p17: missing member: station"},
	} {
		name := row.name
		var doc map[string]any
		must(t, json.Unmarshal([]byte(configP17Control), &doc))
		delete(doc["p17"].(map[string]any), name)
		bad, err := json.Marshal(doc)
		must(t, err)
		_, err = ParseConfig(bad)
		r, ok := err.(*Refusal)
		if !ok || r.Code != "request_invalid" || r.Detail != row.detail {
			t.Fatalf("required %s: %v", name, err)
		}
		_, err = ParseConfig([]byte(configP17Control))
		must(t, err)
	}
}
