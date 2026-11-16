package source

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/relux-works/a2a-swarm/runorigin"
)

// OriginMembers is the size of the admission object (H2 step 3).
const OriginMembers = 6

// ErrOrigin is the root of every origin shape refusal.
var ErrOrigin = errors.New("source: origin malformed")

func originErr(what string) error { return errors.Join(ErrOrigin, errors.New("source: origin: "+what)) }

// CheckOrigin checks the shape of the origin bytes validate returned, without
// changing them: one JSON object of exactly six string members, no nesting,
// no duplicate member, exactly three lowercase 64-hex IDs, mode "enforcing",
// command_id "", no value that is a Matrix event or room ID (leading "$" or
// "!"), and nothing after the object. L never builds, edits or defaults an
// origin; this check only decides refusal.
func CheckOrigin(raw []byte) error {
	if len(raw) == 0 || !utf8.Valid(raw) {
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
	for _, name := range []string{"kind", "event_id", "grant_id", "tip_hash", "mode", "command_id"} {
		if _, ok := members[name]; !ok {
			return originErr("member set")
		}
	}
	o := runorigin.Origin{Kind: members["kind"], EventID: members["event_id"], GrantID: members["grant_id"], TipHash: members["tip_hash"], Mode: members["mode"], CommandID: members["command_id"]}
	if !o.Valid() {
		return originErr("admission values")
	}
	return nil
}
