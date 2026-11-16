package m0

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// Claim: CCJ-1 of dispatch.valid.json without auth, behind the domain and one
// LF, is byte-identical to the 904-byte published preimage specimen and hashes
// to its recorded SHA-256; the same bytes result from re-ordered members and
// added whitespace. Changing the domain or appending a final LF yields
// different bytes. This pins the canonicalizer against an independent witness.
func TestPreimageSpecimen(t *testing.T) {
	want := fixture(t, "m0-launch", "dispatch.preimage.txt")
	wantSum := strings.TrimSpace(string(fixture(t, "m0-launch", "dispatch.preimage.sha256")))
	r, err := DecodeRequest(fixture(t, "m0-launch", "dispatch.valid.json"))
	must(t, err)
	if len(want) != 904 || !bytes.Equal(r.Preimage, want) {
		t.Fatalf("PREIMAGE: %d bytes differ from the specimen", len(r.Preimage))
	}
	sum := sha256.Sum256(r.Preimage)
	if hex.EncodeToString(sum[:]) != wantSum {
		t.Fatal("PREIMAGE: digest differs from the specimen")
	}
	// Reordered members and whitespace variation produce the same preimage.
	pretty := []byte("{\n  \"worker\": {\"generation\": \"cccccccccccccccccccccccccccccccc\", \"uid\": 30002},\n  \"v\": 1,\n" +
		"  \"request_id\": \"00000000000000000000000000000001\",\n  \"receiver\": \"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee\",\n" +
		"  \"policy\": {\"revision\": 1, \"id\": \"dddddddddddddddddddddddddddddddd\"},\n  \"op\": \"dispatch\",\n" +
		"  \"plan\": {\"worker_plan\": \"sha256:1111111111111111111111111111111111111111111111111111111111111111\", \"profile\": \"sha256:2222222222222222222222222222222222222222222222222222222222222222\", \"harness\": \"sha256:3333333333333333333333333333333333333333333333333333333333333333\", \"exec_profile\": \"sha256:4444444444444444444444444444444444444444444444444444444444444444\"},\n" +
		"  \"kind\": \"swarma-dispatcher m0-request/1\",\n" +
		"  \"auth\": {\"sig\": \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\", \"key\": \"ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo\"},\n" +
		"  \"association\": {\"master_epoch\": 1, \"registration_number\": 1, \"key\": \"ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo\", \"generation\": \"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\", \"uid\": 30001, \"revision\": 1, \"id\": \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}\n}\n")
	again, err := DecodeRequest(pretty)
	must(t, err)
	if !bytes.Equal(again.Preimage, want) {
		t.Fatal("PREIMAGE: member order or whitespace changed the canonical bytes")
	}
	for name, mutant := range map[string][]byte{
		"wrong-domain": append([]byte("swarma-dispatcher request/2\n"), r.Canonical...),
		"final-lf":     append(append([]byte(nil), want...), '\n'),
	} {
		if bytes.Equal(mutant, want) || sha256.Sum256(mutant) == sum {
			t.Errorf("PREIMAGE: %s collides with the control", name)
		}
	}
}

// Claim: the canonicalizer refuses every lexical form section 2 forbids, each
// beside an accepted twin that differs only in that form.
func TestCanonicalizeLexicalRefusals(t *testing.T) {
	lim := limits{maxBytes: 4096, maxDepth: 16, maxTokens: 1024}
	for _, tc := range []struct {
		name, ok, bad string
	}{
		{"duplicate", `{"a":1,"b":2}`, `{"a":1,"a":2}`},
		{"escaped-alias", `{"a":1,"b":2}`, `{"a":1,"a":2}`},
		{"nested-duplicate", `{"o":{"a":1}}`, `{"o":{"a":1,"a":1}}`},
		{"bom", `{"a":1}`, "\xEF\xBB\xBF{\"a\":1}"},
		{"invalid-utf8", `{"a":"é"}`, "{\"a\":\"\xC3\"}"},
		{"lone-high-surrogate", `{"a":"😀"}`, `{"a":"\ud83d"}`},
		{"lone-low-surrogate", `{"a":"😀"}`, `{"a":"\ude00"}`},
		{"fraction", `{"a":1}`, `{"a":1.0}`},
		{"exponent", `{"a":1}`, `{"a":1e0}`},
		{"negative-zero", `{"a":0}`, `{"a":-0}`},
		{"unsafe-integer", `{"a":9007199254740991}`, `{"a":9007199254740992}`},
		{"negative-unsafe", `{"a":-9007199254740991}`, `{"a":-9007199254740992}`},
		{"leading-zero", `{"a":10}`, `{"a":010}`},
		{"trailing-bytes", `{"a":1}`, `{"a":1} x`},
		{"second-document", `{"a":1}`, `{"a":1}{"a":1}`},
		{"control-byte", `{"a":"x"}`, "{\"a\":\"\x01\"}"},
		{"unknown-escape", `{"a":"\n"}`, `{"a":"\x"}`},
		{"unterminated", `{"a":"x"}`, `{"a":"x`},
		{"bad-literal", `{"a":true}`, `{"a":tru}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := canonicalize([]byte(tc.ok), lim); err != nil {
				t.Fatalf("LEXICAL: control refused: %v", err)
			}
			if _, _, err := canonicalize([]byte(tc.bad), lim); CodeOf(err) != CodeRequestInvalid {
				t.Fatalf("LEXICAL: mutant accepted or wrong code: %v", err)
			}
		})
	}
}

// Claim: depth 16 is accepted and depth 17 refused; the token ceiling refuses
// the first excess token; the byte ceiling refuses before parsing; each with
// its accepted neighbor.
func TestCanonicalizeBounds(t *testing.T) {
	nest := func(n int) string { return strings.Repeat(`{"a":`, n) + "1" + strings.Repeat("}", n) }
	lim := limits{maxBytes: 4096, maxDepth: 16, maxTokens: 1024}
	if _, _, err := canonicalize([]byte(nest(16)), lim); err != nil {
		t.Fatal("BOUNDS: depth 16 refused", err)
	}
	if _, _, err := canonicalize([]byte(nest(17)), lim); CodeOf(err) != CodeRequestInvalid {
		t.Fatal("BOUNDS: depth 17 accepted", err)
	}
	small := limits{maxBytes: 4096, maxDepth: 16, maxTokens: 5}
	if _, _, err := canonicalize([]byte(`{"a":1}`), small); err != nil { // { "a" : 1 } = 5 tokens
		t.Fatal("BOUNDS: five tokens refused", err)
	}
	if _, _, err := canonicalize([]byte(`{"a":1,"b":2}`), small); CodeOf(err) != CodeRequestInvalid {
		t.Fatal("BOUNDS: token excess accepted", err)
	}
	tiny := limits{maxBytes: 7, maxDepth: 16, maxTokens: 1024}
	if _, _, err := canonicalize([]byte(`{"a":1}`), tiny); err != nil {
		t.Fatal("BOUNDS: exact byte bound refused", err)
	}
	if _, _, err := canonicalize([]byte(`{"a":10}`), tiny); CodeOf(err) != CodeRequestTooLarge {
		t.Fatal("BOUNDS: byte excess accepted", err)
	}
	if _, _, err := canonicalize(nil, lim); CodeOf(err) != CodeRequestInvalid {
		t.Fatal("BOUNDS: empty document accepted", err)
	}
}

// Claim: the encoder sorts names by Unicode scalar (UTF-8 byte order), keeps
// array order, emits shortest integers and exactly the prescribed escapes,
// and leaves '/' and non-ASCII literal.
func TestCanonicalEncoding(t *testing.T) {
	lim := limits{maxBytes: 4096, maxDepth: 16, maxTokens: 1024}
	for _, tc := range []struct{ in, want string }{
		{`{"b":[3,1,2],"a":{"z":true,"y":null}}`, `{"a":{"y":null,"z":true},"b":[3,1,2]}`},
		{`{"é":1,"z":2,"😀":3}`, `{"z":2,"é":1,"😀":3}`},
		{`{"s":"a\/b\"c\\d\u0001\n\t\u007f"}`, `{"s":"a/b\"c\\d\u0001\n\t` + "\x7f" + `"}`},
		{` { "n" : -17 , "m" : 0 } `, `{"m":0,"n":-17}`},
	} {
		_, got, err := canonicalize([]byte(tc.in), lim)
		if err != nil || string(got) != tc.want {
			t.Errorf("ENCODING: %q -> %q (%v), want %q", tc.in, got, err, tc.want)
		}
	}
}
