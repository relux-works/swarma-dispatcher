package source_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
)

// Claim (H2 step 3): the six-member admission object with three lowercase
// 64-hex IDs, mode enforcing and command_id "" passes, and surrounding
// whitespace does not change that.
func TestCheckOriginValid(t *testing.T) {
	if err := source.CheckOrigin(fakes.ValidOrigin()); err != nil {
		t.Fatal(err)
	}
	if err := source.CheckOrigin([]byte("  " + string(fakes.ValidOrigin()) + "\n")); err != nil {
		t.Fatal(err)
	}
}

// Claim (H2 step 3, "forged or Matrix-ID origin is refused"): every listed
// malformation is refused with ErrOrigin, including a Matrix event ID, a
// room ID, uppercase hex, a fourth ID, a missing or extra member, nesting,
// a duplicate member, non-string values, trailing bytes, and empty input.
func TestCheckOriginRefusals(t *testing.T) {
	valid := string(fakes.ValidOrigin())
	a := strings.Repeat("a", 64)
	cases := map[string]string{
		"empty":            "",
		"not json":         "{",
		"array":            "[]",
		"five members":     strings.Replace(valid, `,"kind":"admission"`, "", 1),
		"seven members":    strings.Replace(valid, `"kind":"admission"`, `"kind":"admission","extra":"x"`, 1),
		"mode wrong":       strings.Replace(valid, `"mode":"enforcing"`, `"mode":"advisory"`, 1),
		"mode missing":     strings.Replace(valid, `"mode":"enforcing"`, `"mode2":"enforcing"`, 1),
		"command_id set":   strings.Replace(valid, `"command_id":""`, `"command_id":"c1"`, 1),
		"command missing":  strings.Replace(valid, `"command_id":""`, `"cmd":""`, 1),
		"two ids":          strings.Replace(valid, strings.Repeat("c", 64), "short", 1),
		"four ids":         strings.Replace(valid, `"kind":"admission"`, `"kind":"`+a+`"`, 1),
		"uppercase hex":    strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"matrix event id":  strings.Replace(valid, `"kind":"admission"`, `"kind":"$abc"`, 1),
		"matrix room id":   strings.Replace(valid, `"kind":"admission"`, `"kind":"!room:x"`, 1),
		"nested":           strings.Replace(valid, `"kind":"admission"`, `"kind":{"a":"b"}`, 1),
		"number value":     strings.Replace(valid, `"kind":"admission"`, `"kind":1`, 1),
		"null value":       strings.Replace(valid, `"kind":"admission"`, `"kind":null`, 1),
		"duplicate member": strings.Replace(valid, `"kind":"admission"`, `"mode":"enforcing"`, 1),
		"renamed id":       strings.Replace(valid, `"grant_id":`, `"another_id":`, 1),
		"case variant":     strings.Replace(valid, `"kind":`, `"Kind":`, 1),
		"trailing bytes":   valid + "{}",
	}
	for name, raw := range cases {
		err := source.CheckOrigin([]byte(raw))
		if !errors.Is(err, source.ErrOrigin) {
			t.Fatalf("%s: accepted or wrong error: %v", name, err)
		}
	}
}
