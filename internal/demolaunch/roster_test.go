package demolaunch

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// These literal schema counterexamples cover helper roster members and protected
// configuration bindings. No fixture claims installation or runtime qualification.
func TestRosterSchema(t *testing.T) {
	type change struct {
		section, key string
		value        any
	}
	cases := []change{
		{"", "format", "other"}, {"", "source_revision", strings.Repeat("A", 40)}, {"", "source_revision", strings.Repeat("a", 39)},
		{"", "boot_id", "12345678-1234-1234-1234-123456789abC"},
		{"operator", "name", "Bad"}, {"operator", "uid", true}, {"operator", "uid", 1000.5}, {"operator", "gid", true}, {"operator", "gid", 1000.5}, {"operator", "uid", "1000"}, {"operator", "gid", "1000"},
		{"operator", "name", "swarma-demo-dispatcher"}, {"operator", "uid", 1101}, {"operator", "uid", 30000},
		{"dispatcher", "name", "other"}, {"dispatcher", "principal", "other"}, {"dispatcher", "uid", 1102}, {"dispatcher", "gid", 1102}, {"dispatcher", "generation", strings.Repeat("0", 32)}, {"dispatcher", "generation", strings.Repeat("A", 32)},
		{"executor", "id", "other"}, {"executor", "path", "/other"}, {"executor", "sha256", strings.Repeat("A", 64)},
		{"runtime", "admitted", 1}, {"runtime", "inventory_qualified", "true"}, {"runtime", "creation_exclusion_qualified", 0}, {"runtime", "interpreter_qualified", []any{}},
		{"account", "name", "other"}, {"account", "uid", 29999}, {"account", "uid", 30020}, {"account", "gid", 30001},
		{"account", "generation", strings.Repeat("0", 32)}, {"account", "generation", strings.Repeat("d", 32)},
		{"account", "owning_dispatcher", "other"}, {"account", "home", "/other"}, {"account", "workspace", "/other"}, {"account", "copy_digest", strings.Repeat("A", 64)},
	}
	section := func(d map[string]any, s string) map[string]any {
		if s == "" {
			return d
		}
		if s == "account" {
			return d["accounts"].([]map[string]any)[0]
		}
		return d[s].(map[string]any)
	}
	check := func(t *testing.T, d map[string]any, c serverConfig, valid bool) {
		t.Helper()
		b := marshal(d)
		before := append([]byte(nil), b...)
		a, e := parseRoster(b, c)
		if valid {
			if e != nil || len(a) != 3 {
				t.Fatalf("roster control refused: %v %v", a, e)
			}
		} else if a != nil || !errors.Is(e, ErrConfig) {
			t.Fatalf("roster schema accepted: %v %v", a, e)
		}
		if !bytes.Equal(before, b) {
			t.Fatal("roster input changed")
		}
	}
	check(t, rosterDocument(30000), rosterConfig(), true)
	for _, tc := range cases {
		t.Run(tc.section+"/"+tc.key+"/"+string(marshal(tc.value)), func(t *testing.T) {
			d := rosterDocument(30000)
			section(d, tc.section)[tc.key] = tc.value
			c := rosterConfig()
			// Keep dependent fields consistent to isolate the frozen identity
			// and disjointness predicates, even for otherwise-invalid configs.
			if tc.section == "operator" && tc.key == "uid" {
				if n, ok := tc.value.(int); ok {
					c.Caller.UID = uint32(n)
				}
			}
			if tc.section == "dispatcher" && tc.key == "uid" {
				c.ServiceUID = uint32(tc.value.(int))
			}
			if tc.section == "dispatcher" && tc.key == "principal" {
				c.ServiceAccount = tc.value.(string)
				for _, a := range d["accounts"].([]map[string]any) {
					a["owning_dispatcher"] = tc.value
				}
			}
			if tc.section == "account" && tc.key == "name" {
				section(d, "account")["home"] = "/srv/swarma-demo/home/other"
				section(d, "account")["workspace"] = "/srv/swarma-demo/work/other"
			}
			if tc.section == "account" && tc.key == "uid" {
				section(d, "account")["gid"] = tc.value
			}
			if tc.section == "executor" && tc.key == "id" {
				c.Executor = tc.value.(string)
				section(d, "executor")["path"] = ""
			}
			check(t, d, c, false)
		})
	}
	// Unknown and missing members at every object level, including case aliases.
	for _, s := range []string{"", "operator", "dispatcher", "executor", "runtime", "account"} {
		d := rosterDocument(30000)
		for k := range section(d, s) {
			t.Run("closed/"+s+"/"+k, func(t *testing.T) {
				d := rosterDocument(30000)
				m := section(d, s)
				v := m[k]
				delete(m, k)
				check(t, d, rosterConfig(), false)
				m[strings.ToUpper(k)] = v
				check(t, d, rosterConfig(), false)
			})
		}
		d = rosterDocument(30000)
		section(d, s)["extra"] = true
		check(t, d, rosterConfig(), false)
	}
	// The dispatcher accepts unpinned revisions, both frozen executors, false
	// runtime flags, and integer operator GIDs without imposing helper constants.
	for _, id := range []string{"swarma-demo-executor", "swarma-demo-codex-executor"} {
		d := rosterDocument(30000)
		d["source_revision"] = strings.Repeat("f", 40)
		d["operator"].(map[string]any)["gid"] = -1
		e := d["executor"].(map[string]any)
		e["id"] = id
		e["path"] = "/usr/local/libexec/swarma/" + id
		c := rosterConfig()
		c.Executor = id
		check(t, d, c, true)
	}
	// Whitespace at the exact helper cap is valid; one more byte is not.
	b := rosterFixture(30000)
	b = append(b, bytes.Repeat([]byte{' '}, 49152-len(b))...)
	if a, e := parseRoster(b, rosterConfig()); e != nil || len(a) != 3 {
		t.Fatalf("roster cap control refused: %v", e)
	}
	if a, e := parseRoster(append(b, ' '), rosterConfig()); a != nil || !errors.Is(e, ErrConfig) {
		t.Fatalf("roster cap accepted: %v", e)
	}
}

// Requests and durable records share the helper IDENT grammar. Helper result
// code/state validation remains separate. These checks exercise parsers only.
func TestRequestIDGrammar(t *testing.T) {
	for _, tc := range []struct {
		id    string
		valid bool
	}{
		{"a", true}, {"Z0._-", true}, {strings.Repeat("a", 64), true}, {strings.Repeat("a", 65), false}, {".a", false}, {"-a", false}, {"_a", false}, {"a:b", false}, {"", false},
	} {
		for _, op := range []string{"demo.launch", "demo.stop"} {
			m := map[string]any{"op": op, "request_id": tc.id, "account": "demo"}
			if op == "demo.launch" {
				m["plan"] = ""
			}
			_, e := parseRequest(marshal(m))
			if (e == nil) != tc.valid || (!tc.valid && !errors.Is(e, ErrInvalid)) {
				t.Fatalf("request ID grammar accepted: %q %s %v", tc.id, op, e)
			}
		}
	}
}

// The reserved uint32 sentinel is refused for every configured UID/GID field;
// a neighboring representable ID remains valid under the existing config rules.
func TestConfigIDUpperBound(t *testing.T) {
	for _, key := range []string{"service_uid", "service_gid", "connect_gid", "caller"} {
		for _, n := range []uint64{4294967294, 4294967295} {
			c := cfgFixture()
			if key == "caller" {
				c[key].(map[string]any)["uid"] = n
			} else {
				c[key] = n
			}
			_, e := parseServer(marshal(c))
			if n == 4294967294 {
				if e != nil {
					t.Fatalf("ID upper control refused: %v", e)
				}
			} else if !errors.Is(e, ErrConfig) {
				t.Fatalf("ID upper bound accepted: %s %v", key, e)
			}
		}
	}
}

// Only the two published executor IDs may reach roster loading. A generic
// identifier is deliberately insufficient, even if syntactically well formed.
func TestConfigExecutorIDs(t *testing.T) {
	for _, id := range []string{"swarma-demo-executor", "swarma-demo-codex-executor", "other", "", "/bin/sh"} {
		c := cfgFixture()
		c["executor"] = id
		_, err := parseServer(marshal(c))
		valid := id == "swarma-demo-executor" || id == "swarma-demo-codex-executor"
		if valid && err != nil || !valid && !errors.Is(err, ErrConfig) {
			t.Fatalf("executor config accepted: %s %v", id, err)
		}
	}
}
