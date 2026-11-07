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
				t.Fatal("configuration bounds assertion", uid, e)
			}
		} else if !errors.Is(e, ErrConfig) || a != nil {
			t.Fatal("configuration bounds assertion", uid, e)
		}
	}
	if _, e := parseServer(marshal(cfgFixture())); e != nil {
		t.Fatal("configuration bounds assertion", e)
	}
	for _, uid := range []int{0, 30000, 30019, 30020, 59999} {
		c := cfgFixture()
		c["service_uid"] = uid
		if _, e := parseServer(marshal(c)); !errors.Is(e, ErrConfig) {
			t.Fatal("configuration bounds assertion", "service", uid, e)
		}
	}
	for _, uid := range []int{29999, 60000} {
		c := cfgFixture()
		c["service_uid"] = uid
		if _, e := parseServer(marshal(c)); e != nil {
			t.Fatal("configuration bounds assertion", uid, e)
		}
	}
	for _, mutate := range []func(map[string]any){
		func(c map[string]any) { c["connect_gid"] = c["service_gid"] },
		func(c map[string]any) { c["executor"] = "/bin/sh" }, func(c map[string]any) { c["state_dir"] = "relative" }, func(c map[string]any) { c["receipt_root"] = "/a/../b" },
		func(c map[string]any) { c["service_account"] = "../x" }, func(c map[string]any) { c["runner"] = "anything" }, func(c map[string]any) { delete(c, "executor") },
		func(c map[string]any) { c["caller"].(map[string]any)["UID"] = 1000 }, func(c map[string]any) { c["caller"].(map[string]any)["uid"] = 900 },
	} {
		c := cfgFixture()
		mutate(c)
		if _, e := parseServer(marshal(c)); !errors.Is(e, ErrConfig) {
			t.Fatal("configuration bounds assertion", c, e)
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
			t.Fatal("configuration bounds assertion", b, e)
		}
	}
	for _, b := range [][]byte{append(marshal(cfgFixture()), []byte(`{}`)...), []byte(strings.Replace(string(marshal(cfgFixture())), `"v":1`, `"v":1,"v":1`, 1))} {
		if _, e := parseServer(b); !errors.Is(e, ErrConfig) {
			t.Fatal("configuration bounds assertion", e)
		}
	}
}

// Canonical neighboring encodings reject nonzero pad bits, embedded
// whitespace or omitted padding even when a permissive decoder accepts them.
func TestCanonicalPlan(t *testing.T) {
	for _, plan := range []string{"AA==", "AAA=", "AAAA", ""} {
		if _, err := parseRequest([]byte(`{"op":"demo.launch","request_id":"r","account":"demo","plan":"` + plan + `"}`)); err != nil {
			t.Fatal(plan, err)
		}
	}
	for _, plan := range []string{"AB==", "AAB=", "AA", "AA==\n", "AA== "} {
		b := marshal(map[string]string{"op": "demo.launch", "request_id": "r", "account": "demo", "plan": plan})
		if _, err := parseRequest(b); !errors.Is(err, ErrInvalid) {
			t.Fatal(plan, err)
		}
	}
}

// A complete 20-row roster is the positive bound; individual duplicated name,
// UID or generation must refuse, as must an empty or 21-row roster. The 21st
// row also necessarily exceeds the UID domain, a documented redundant guard.
func TestRosterUniquenessAndCount(t *testing.T) {
	var rows []map[string]any
	for i := 0; i < 20; i++ {
		rows = append(rows, map[string]any{"name": "demo-" + jsonNumber(i), "uid": 30000 + i, "gid": 30000 + i, "generation": strings.Repeat("0", 30) + string("0123456789abcdef"[i/16]) + string("0123456789abcdef"[i%16]), "owner": "demo-service"})
	}
	encode := func() []byte { return marshal(map[string]any{"accounts": rows}) }
	if a, err := parseRoster(encode(), "demo-service"); err != nil || len(a) != 20 {
		t.Fatal(len(a), err)
	}
	for _, key := range []string{"name", "uid", "generation"} {
		old := rows[1][key]
		rows[1][key] = rows[0][key]
		if key == "uid" {
			rows[1]["gid"] = rows[0]["uid"]
		}
		if a, err := parseRoster(encode(), "demo-service"); a != nil || !errors.Is(err, ErrConfig) {
			t.Fatal(key, a, err)
		}
		rows[1][key] = old
		rows[1]["gid"] = 30001
	}
	rows = append(rows, rows[0])
	if a, err := parseRoster(encode(), "demo-service"); a != nil || !errors.Is(err, ErrConfig) {
		t.Fatal(a, err)
	}
	rows = rows[:0]
	if a, err := parseRoster(encode(), "demo-service"); a != nil || !errors.Is(err, ErrConfig) {
		t.Fatal(a, err)
	}
}

// Client snapshots accept only the smaller exact contract, with a valid control.
func TestClientConfig(t *testing.T) {
	cfg := cfgFixture()
	for _, k := range []string{"executor", "service_account", "receipt_root", "state_dir"} {
		delete(cfg, k)
	}
	if _, err := parseClient(marshal(cfg)); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"caller", "v", "connect_gid"} {
		old := cfg[key]
		delete(cfg, key)
		if _, err := parseClient(marshal(cfg)); !errors.Is(err, ErrConfig) {
			t.Fatal(key, err)
		}
		cfg[key] = old
	}
	cfg["executor"] = "forbidden"
	if _, err := parseClient(marshal(cfg)); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
}

// Lower and upper format neighbors exercise parsers, not only regex helpers.
// Managed caller IDs remain forbidden; these are no account-enrollment tests.
func TestFormatBoundaries(t *testing.T) {
	for _, n := range []int{31, 32, 33} {
		b := strings.Replace(string(rosterFixture(30000)), "0123456789abcdef0123456789abcdef", strings.Repeat("a", n), 1)
		_, err := parseRoster([]byte(b), "demo-service")
		if n == 32 && err != nil || n != 32 && !errors.Is(err, ErrConfig) {
			t.Fatal("format boundary assertion", "generation", n, err)
		}
	}
	for _, n := range []int{0, 1, 32, 33} {
		b := marshal(map[string]string{"op": "demo.status", "account": strings.Repeat("a", n)})
		_, err := parseRequest(b)
		if (n == 1 || n == 32) && err != nil || (n == 0 || n == 33) && !errors.Is(err, ErrInvalid) {
			t.Fatal("format boundary assertion", "name", n, err)
		}
	}
	for _, uid := range []int{29999, 30000, 59999, 60000} {
		c := cfgFixture()
		c["caller"].(map[string]any)["uid"] = uid
		_, err := parseServer(marshal(c))
		if (uid == 29999 || uid == 60000) && err != nil || (uid == 30000 || uid == 59999) && !errors.Is(err, ErrConfig) {
			t.Fatal("format boundary assertion", "caller", uid, err)
		}
	}
	for _, n := range []int{0, 1, 64, 65} {
		b := marshal(map[string]string{"op": "demo.stop", "request_id": strings.Repeat("x", n), "account": "demo"})
		_, err := parseRequest(b)
		if (n == 1 || n == 64) && err != nil || (n == 0 || n == 65) && !errors.Is(err, ErrInvalid) {
			t.Fatal("format boundary assertion", "id", n, err)
		}
	}
	for _, key := range []string{"service_gid", "connect_gid", "endpoint_dev", "endpoint_ino"} {
		for _, v := range []uint64{0, 9007199254740992} {
			c := cfgFixture()
			c[key] = v
			if _, err := parseServer(marshal(c)); !errors.Is(err, ErrConfig) {
				t.Fatal("format boundary assertion", key, v, err)
			}
		}
	}
	for _, b := range []string{`{"op":"demo.launch","account":"demo","request_id":"r"}`, `{"op":"demo.stop","account":"demo","request_id":"r","plan":""}`, `{"op":"demo.status","account":"demo","request_id":"r"}`} {
		if _, err := parseRequest([]byte(b)); !errors.Is(err, ErrInvalid) {
			t.Fatal("format boundary assertion", b, err)
		}
	}
}

// Principal precision and zero neighbors use the protected config parser.
func TestPrincipalNumberBounds(t *testing.T) {
	for _, key := range []string{"dev", "ino"} {
		for _, n := range []uint64{0, 1, 9007199254740991, 9007199254740992} {
			c := cfgFixture()
			c["caller"].(map[string]any)[key] = n
			_, err := parseServer(marshal(c))
			valid := n == 1 || n == 9007199254740991
			if valid && err != nil || !valid && !errors.Is(err, ErrConfig) {
				t.Fatal("principal boundary assertion", key, n, err)
			}
		}
	}
}
