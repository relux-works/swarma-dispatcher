package source

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// OriginMembers is the size of the admission object (H2 step 3).
const OriginMembers = 6

// ErrOrigin is the root of every origin shape refusal.
var ErrOrigin = errors.New("source: origin malformed")

func originErr(what string) error { return errors.Join(ErrOrigin, errors.New("source: origin: "+what)) }

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// CheckOrigin checks the shape of the origin bytes validate returned, without
// changing them: one JSON object of exactly six string members, no nesting,
// no duplicate member, exactly three lowercase 64-hex IDs, mode "enforcing",
// command_id "", no value that is a Matrix event or room ID (leading "$" or
// "!"), and nothing after the object. L never builds, edits or defaults an
// origin; this check only decides refusal.
func CheckOrigin(raw []byte) error {
	if len(raw) == 0 {
		return originErr("missing")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return originErr("not JSON")
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return originErr("not an object")
	}
	members := make(map[string]string, OriginMembers)
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return originErr("truncated")
		}
		key, ok := kt.(string)
		if !ok {
			return originErr("member name")
		}
		if _, dup := members[key]; dup {
			return originErr("duplicate member " + key)
		}
		vt, err := dec.Token()
		if err != nil {
			return originErr("truncated value")
		}
		val, ok := vt.(string)
		if !ok {
			if _, nested := vt.(json.Delim); nested {
				return originErr("nested value in " + key)
			}
			return originErr("non-string value in " + key)
		}
		members[key] = val
		if len(members) > OriginMembers {
			return originErr("more than six members")
		}
	}
	end, err := dec.Token()
	if err != nil {
		return originErr("unterminated object")
	}
	if d, ok := end.(json.Delim); !ok || d != '}' {
		return originErr("object end")
	}
	if _, err = dec.Token(); err != io.EOF {
		return originErr("trailing bytes")
	}
	if len(members) != OriginMembers {
		return originErr("member count")
	}
	mode, ok := members["mode"]
	if !ok || mode != "enforcing" {
		return originErr("mode")
	}
	cmd, ok := members["command_id"]
	if !ok || cmd != "" {
		return originErr("command_id")
	}
	ids := 0
	for k, v := range members {
		if strings.HasPrefix(v, "$") || strings.HasPrefix(v, "!") {
			return originErr("Matrix identifier in " + k)
		}
		if k != "mode" && k != "command_id" && isHex64(v) {
			ids++
		}
	}
	if ids != 3 {
		return originErr("id count")
	}
	return nil
}
