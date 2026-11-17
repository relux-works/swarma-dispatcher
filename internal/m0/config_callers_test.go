package m0

import (
	"strings"
	"testing"
)

// Claim: M0 config /3 requires a closed, sorted 1..4 managed caller list, names every malformed list refusal, and refuses config /1 rather than silently widening it.
func TestManagedCallersConfig(t *testing.T) {
	one := `{"dev":1,"dir":"/example/caller","ino":2,"uid":30001}`
	canon := `{"callers":[` + one + `],"enabled":true,"entry":"initialize","kind":"swarma-dispatcher m0-config/3","p17":` + fixtureP17 + `,"recovery_attested":false,"state_dir":"/var/lib/example","supported_local_storage":true,"v":3}`
	cfg, err := ParseConfig([]byte(canon))
	must(t, err)
	if len(cfg.Callers) != 1 || cfg.Callers[0].UID != 30001 {
		t.Fatal("valid caller lost")
	}
	for _, tc := range []struct{ name, list string }{
		{"empty", `[]`}, {"null", `null`}, {"object", one}, {"string", `"callers"`},
		{"oversized", `[` + strings.TrimSuffix(strings.Repeat(one+",", 5), ",") + `]`},
		{"duplicate-uid", `[` + one + `,` + one + `]`},
		{"unsorted", `[` + strings.Replace(one, "30001", "30002", 1) + `,` + one + `]`},
		{"below-range", `[` + strings.Replace(one, "30001", "29999", 1) + `]`},
		{"above-range", `[` + strings.Replace(one, "30001", "60000", 1) + `]`},
		{"uid-string", `[` + strings.Replace(one, `30001`, `"30001"`, 1) + `]`},
		{"uid-wrap", `[` + strings.Replace(one, `30001`, `4294997297`, 1) + `]`},
		{"extra-member", `[` + strings.Replace(one, `"uid":30001`, `"uid":30001,"x":1`, 1) + `]`},
		{"missing-member", `[` + strings.Replace(one, `"dev":1,`, ``, 1) + `]`},
		{"case-alias", `[` + strings.Replace(one, `"dev"`, `"Dev"`, 1) + `]`},
		{"zero-dev", `[` + strings.Replace(one, `"dev":1`, `"dev":0`, 1) + `]`},
		{"negative-ino", `[` + strings.Replace(one, `"ino":2`, `"ino":-1`, 1) + `]`},
		{"root-dir", `[` + strings.Replace(one, `/example/caller`, `/`, 1) + `]`},
		{"relative-dir", `[` + strings.Replace(one, `/example/caller`, `example/caller`, 1) + `]`},
		{"unclean-dir", `[` + strings.Replace(one, `/example/caller`, `/example/../caller`, 1) + `]`},
		{"long-dir", `[` + strings.Replace(one, `/example/caller`, `/`+strings.Repeat("a", 512), 1) + `]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := strings.Replace(canon, `[`+one+`]`, tc.list, 1)
			ext, err := Attach("/example/m0.json", DefaultSeams(), func(string) ([]byte, error) { return []byte(bad), nil })
			if ext != nil || CodeOf(err) != CodeRequestInvalid || !strings.Contains(err.Error(), "callers:") {
				t.Fatalf("unnamed caller refusal: %v", err)
			}
			_, err = ParseConfig([]byte(canon))
			must(t, err)
		})
	}
	for _, tc := range []struct{ name, doc string }{
		{"missing", strings.Replace(canon, `"callers":[`+one+`],`, "", 1)},
		{"old-version", strings.Replace(strings.Replace(strings.Replace(canon, `"callers":[`+one+`],`, "", 1), "m0-config/3", "m0-config/1", 1), `"v":3`, `"v":1`, 1)},
		{"old-version-with-callers", strings.Replace(canon, `"v":3`, `"v":1`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Attach("/example/m0.json", DefaultSeams(), func(string) ([]byte, error) { return []byte(tc.doc), nil })
			if CodeOf(err) != CodeRequestInvalid || (!strings.Contains(err.Error(), "callers:") && !strings.Contains(err.Error(), "configuration version")) {
				t.Fatalf("old/missing list accepted: %v", err)
			}
		})
	}
	four := make([]string, 4)
	for i, uid := range []string{"30000", "30001", "30002", "59999"} {
		four[i] = strings.Replace(one, "30001", uid, 1)
	}
	cfg, err = ParseConfig([]byte(strings.Replace(canon, `[`+one+`]`, `[`+strings.Join(four, ",")+`]`, 1)))
	must(t, err)
	if len(cfg.Callers) != 4 {
		t.Fatal("four-caller boundary refused")
	}
}
