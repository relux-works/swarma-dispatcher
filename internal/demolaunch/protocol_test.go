package demolaunch

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Literal neighboring shapes prove exact-key and plan bounds, not authority.
func TestRequestContract(t *testing.T) {
	positive := []string{
		`{"op":"demo.launch","request_id":"r","account":"demo","plan":""}`,
		`{"op":"demo.stop","request_id":"r","account":"demo"}`,
		`{"op":"demo.stop","request_id":"r","account":"demo","grace_seconds":1}`,
		`{"op":"demo.stop","request_id":"r","account":"demo","grace_seconds":30}`,
		`{"op":"demo.status","account":"demo"}`,
	}
	for _, b := range positive {
		if _, e := parseRequest([]byte(b)); e != nil {
			t.Fatal(b, e)
		}
	}
	r, e := parseRequest([]byte(positive[1]))
	if e != nil || r.Grace != 10 {
		t.Fatal("default grace", r, e)
	}
	for _, size := range []int{49152, 49153} {
		b := `{"op":"demo.launch","request_id":"` + strings.Repeat("a", 64) + `","account":"demo","plan":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{255}, size)) + `"}`
		_, e := parseRequest([]byte(b))
		if size == 49152 && e != nil || size == 49153 && !errors.Is(e, ErrInvalid) {
			t.Fatal(size, e)
		}
	}
	for _, b := range []string{
		`{"op":"demo.launch","op":"demo.launch","request_id":"r","account":"demo","plan":""}`,
		`{"op":"demo.launch","request_id":"r","account":"demo","plan":"!"}`,
		`{"op":"demo.launch","request_id":"r","account":"demo","plan":null}`,
		`{"op":"demo.stop","request_id":"r","account":"demo","grace_seconds":0}`,
		`{"op":"demo.stop","request_id":"r","account":"demo","grace_seconds":31}`,
		`{"op":"demo.stop","request_id":"r","account":"demo","grace_seconds":1.0}`,
		`{"op":"demo.status","account":"../demo"}`,
		`{"Op":"demo.status","account":"demo"}`,
		positive[0] + `{}`, strings.Replace(positive[0], `"r"`, `"`+strings.Repeat("a", 65)+`"`, 1),
	} {
		if _, e := parseRequest([]byte(b)); !errors.Is(e, ErrInvalid) {
			t.Fatal("accepted", b, e)
		}
	}
	for _, key := range []string{"uid", "path", "argv", "env", "grant", "unknown"} {
		b := strings.TrimSuffix(positive[0], "}") + `,"` + key + `":"x"}`
		if _, e := parseRequest([]byte(b)); !errors.Is(e, ErrInvalid) {
			t.Fatal(key, e)
		}
	}
	if _, e := parseRequest([]byte(`{"op":"capacity","account":"demo"}`)); e == nil || e.Error() != "operation_unknown" {
		t.Fatal(e)
	}
}
func cfgFixture() map[string]any {
	return map[string]any{"v": 1, "service_uid": 900, "service_gid": 900, "connect_gid": 901, "endpoint_dev": 1, "endpoint_ino": 2, "caller": map[string]any{"uid": 1000, "dev": 1, "ino": 3, "dir": "/home/operator"}, "service_account": "demo-service", "executor": "demo-executor", "receipt_root": "/var/lib/receipts", "state_dir": "/var/lib/demo"}
}
func rosterFixture(uid int) []byte {
	return []byte(`{"accounts":[{"name":"demo","uid":` + jsonNumber(uid) + `,"gid":` + jsonNumber(uid) + `,"generation":"0123456789abcdef0123456789abcdef","owner":"demo-service"}]}`)
}
func jsonNumber(n int) string { b, _ := json.Marshal(n); return string(b) }

// Both inclusive roster bounds are valid controls; config roles remain unmanaged.
func TestConfigRosterBounds(t *testing.T) {
	for _, uid := range []int{30000, 30019, 29999, 30020} {
		a, e := parseRoster(rosterFixture(uid), "demo-service")
		if uid == 30000 || uid == 30019 {
			if e != nil || len(a) != 1 {
				t.Fatal(uid, e)
			}
		} else if !errors.Is(e, ErrConfig) || a != nil {
			t.Fatal(uid, e)
		}
	}
	if _, e := parseServer(marshal(cfgFixture())); e != nil {
		t.Fatal(e)
	}
	for _, uid := range []int{0, 30000, 30019, 30020, 59999} {
		c := cfgFixture()
		c["service_uid"] = uid
		if _, e := parseServer(marshal(c)); !errors.Is(e, ErrConfig) {
			t.Fatal("service", uid, e)
		}
	}
	for _, uid := range []int{29999, 60000} {
		c := cfgFixture()
		c["service_uid"] = uid
		if _, e := parseServer(marshal(c)); e != nil {
			t.Fatal(uid, e)
		}
	}
	for _, mutate := range []func(map[string]any){
		func(c map[string]any) { c["executor"] = "/bin/sh" }, func(c map[string]any) { c["state_dir"] = "relative" }, func(c map[string]any) { c["receipt_root"] = "/a/../b" },
		func(c map[string]any) { c["service_account"] = "../x" }, func(c map[string]any) { c["runner"] = "anything" }, func(c map[string]any) { delete(c, "executor") },
		func(c map[string]any) { c["caller"].(map[string]any)["UID"] = 1000 }, func(c map[string]any) { c["caller"].(map[string]any)["uid"] = 900 },
	} {
		c := cfgFixture()
		mutate(c)
		if _, e := parseServer(marshal(c)); !errors.Is(e, ErrConfig) {
			t.Fatal(c, e)
		}
	}
	for _, b := range []string{
		strings.Replace(string(rosterFixture(30000)), `"gid":30000`, `"gid":30001`, 1),
		strings.Replace(string(rosterFixture(30000)), `"owner":"demo-service"`, `"owner":"foreign"`, 1),
		strings.Replace(string(rosterFixture(30000)), `0123456789abcdef0123456789abcdef`, `0123456789ABCDEF0123456789abcdef`, 1),
		strings.Replace(string(rosterFixture(30000)), `"uid":30000`, `"uid":30000,"uid":30000`, 1),
		strings.Replace(string(rosterFixture(30000)), `"uid":30000`, `"uid":30000,"path":"x"`, 1),
		string(rosterFixture(30000)) + `{}`,
	} {
		if _, e := parseRoster([]byte(b), "demo-service"); !errors.Is(e, ErrConfig) {
			t.Fatal(b, e)
		}
	}
	for _, b := range [][]byte{append(marshal(cfgFixture()), []byte(`{}`)...), []byte(strings.Replace(string(marshal(cfgFixture())), `"v":1`, `"v":1,"v":1`, 1))} {
		if _, e := parseServer(b); !errors.Is(e, ErrConfig) {
			t.Fatal(e)
		}
	}
}
