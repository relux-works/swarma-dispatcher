package m0

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

// Claim: every fixture under fixtures/m0-launch is accepted or refused by the
// strict decoder exactly as its README says: the four *.valid.json shapes
// decode (dispatch/cancel as requests, policy, signing plan) and each
// *.invalid.json refuses with request_invalid under its named decoder.
func TestFixturesDecodeAsDocumented(t *testing.T) {
	for _, name := range []string{"dispatch.valid.json", "cancel.valid.json"} {
		if _, err := DecodeRequest(fixture(t, "m0-launch", name)); err != nil {
			t.Errorf("FIXTURE: %s refused: %v", name, err)
		}
	}
	if _, err := DecodePolicy(fixture(t, "m0-launch", "policy.valid.json")); err != nil {
		t.Errorf("FIXTURE: policy.valid.json refused: %v", err)
	}
	if _, err := DecodeSigningPlan(fixture(t, "m0-launch", "signing-plan.valid.json")); err != nil {
		t.Errorf("FIXTURE: signing-plan.valid.json refused: %v", err)
	}
	for _, stem := range []string{"request-extra", "nested-extra", "grant-fallback", "unsupported-op", "dispatch-cancel-member", "missing-cancel-target", "generation-lf", "hash-case", "signature-pad", "signature-tail-bits", "native-frame"} {
		if _, err := DecodeRequest(fixture(t, "m0-launch", stem+".invalid.json")); CodeOf(err) != CodeRequestInvalid {
			t.Errorf("FIXTURE: %s accepted or wrong code: %v", stem, err)
		}
	}
	for _, stem := range []string{"policy-two-starts", "policy-stop-bound", "policy-extra"} {
		if _, err := DecodePolicy(fixture(t, "m0-launch", stem+".invalid.json")); CodeOf(err) != CodeRequestInvalid {
			t.Errorf("FIXTURE: %s accepted or wrong code: %v", stem, err)
		}
	}
	for _, stem := range []string{"signing-opaque", "signing-auth"} {
		if _, err := DecodeSigningPlan(fixture(t, "m0-launch", stem+".invalid.json")); CodeOf(err) != CodeRequestInvalid {
			t.Errorf("FIXTURE: %s accepted or wrong code: %v", stem, err)
		}
	}
}

// Claim: the fixture placeholder signature is schema-valid but does not
// verify; a real Ed25519 signature over the exact preimage under the
// association key verifies; a signature by another key, over a changed body,
// over the preimage with a final LF or with a different domain, and an auth
// key that differs from the association key each refuse with the named code.
func TestSignatureVerification(t *testing.T) {
	r, err := DecodeRequest(fixture(t, "m0-launch", "dispatch.valid.json"))
	must(t, err)
	wantCode(t, r.Verify(), CodeSignatureInvalid)
	k := newKey(t)
	doc := parseDoc(t, fixture(t, "m0-launch", "dispatch.valid.json")).without("auth")
	setPath(t, doc, "association.key", text(k.spelling))
	signed, err := DecodeRequest(signRequest(t, doc, k))
	must(t, err)
	if err := signed.Verify(); err != nil {
		t.Fatalf("SIGNATURE: valid control refused: %v", err)
	}
	other := newKey(t)
	sigOf := func(priv ed25519.PrivateKey, msg []byte) *value {
		return text(base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg)))
	}
	signedDoc := parseDoc(t, signRequest(t, doc, k))
	for _, tc := range []struct {
		name string
		edit func(v *value)
		code Code
	}{
		{"other-key-signs", func(v *value) { setPath(t, v, "auth.sig", sigOf(other.priv, signed.Preimage)) }, CodeSignatureInvalid},
		{"changed-body", func(v *value) { setPath(t, v, "worker.uid", integer(30003)) }, CodeSignatureInvalid},
		{"final-lf", func(v *value) {
			setPath(t, v, "auth.sig", sigOf(k.priv, append(append([]byte(nil), signed.Preimage...), '\n')))
		}, CodeSignatureInvalid},
		{"other-domain", func(v *value) {
			setPath(t, v, "auth.sig", sigOf(k.priv, append([]byte("swarma-dispatcher request/2\n"), signed.Canonical...)))
		}, CodeSignatureInvalid},
		{"auth-key-not-association", func(v *value) {
			setPath(t, v, "auth.key", text(other.spelling))
			setPath(t, v, "auth.sig", sigOf(other.priv, signed.Preimage))
		}, CodeKeyNotAssociated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := parseDoc(t, signedDoc.encode(nil))
			tc.edit(v)
			req, err := DecodeRequest(v.encode(nil))
			must(t, err)
			wantCode(t, req.Verify(), tc.code)
		})
	}
}

// Claim: the closed-form preimage maxima hold: four 16-digit serials give
// exactly 964 bytes for dispatch and 1019 for cancel and are accepted; the
// byte bound of the wire body refuses one byte past MaxRequestWire before
// decoding; and a non-canonical key point encoding refuses.
func TestRequestBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{{"dispatch.valid.json", MaxDispatchPreimage}, {"cancel.valid.json", MaxCancelPreimage}} {
		doc := parseDoc(t, fixture(t, "m0-launch", tc.name))
		for _, p := range []string{"policy.revision", "association.revision", "association.registration_number", "association.master_epoch"} {
			setPath(t, doc, p, integer(maxSafeInteger))
		}
		r, err := DecodeRequest(doc.encode(nil))
		must(t, err)
		if len(r.Preimage) != tc.want {
			t.Errorf("BOUNDS: %s maximal preimage %d bytes, want %d", tc.name, len(r.Preimage), tc.want)
		}
	}
	oversize := append([]byte(nil), fixture(t, "m0-launch", "dispatch.valid.json")...)
	oversize = append(oversize, bytes.Repeat([]byte(" "), MaxRequestWire-len(oversize)+1)...)
	if _, err := DecodeRequest(oversize); CodeOf(err) != CodeRequestTooLarge {
		t.Errorf("BOUNDS: oversize wire body accepted: %v", err)
	}
	exact := append([]byte(nil), fixture(t, "m0-launch", "dispatch.valid.json")...)
	exact = append(exact, bytes.Repeat([]byte(" "), MaxRequestWire-len(exact))...)
	if _, err := DecodeRequest(exact); err != nil {
		t.Errorf("BOUNDS: exact wire bound refused: %v", err)
	}
	// A y coordinate at the field prime is a non-canonical point encoding.
	nonCanonical := make([]byte, 32)
	nonCanonical[0] = 0xed
	for i := 1; i < 31; i++ {
		nonCanonical[i] = 0xff
	}
	nonCanonical[31] = 0x7f
	if canonicalPointEncoding(nonCanonical) {
		t.Error("BOUNDS: non-canonical point accepted")
	}
	k := newKey(t)
	if !canonicalPointEncoding(k.pub) {
		t.Error("BOUNDS: generated key rejected")
	}
	spelled := keyPrefix + base64.RawURLEncoding.EncodeToString(nonCanonical)
	doc := parseDoc(t, fixture(t, "m0-launch", "dispatch.valid.json"))
	setPath(t, doc, "association.key", text(spelled))
	setPath(t, doc, "auth.key", text(spelled))
	if _, err := DecodeRequest(doc.encode(nil)); CodeOf(err) != CodeRequestInvalid {
		t.Errorf("BOUNDS: non-canonical key spelling accepted: %v", err)
	}
}

// Claim: boundary spellings refuse one step past each closed form and accept
// the boundary itself: UID 29999/60000, serial 0/2^53, 31/33-hex IDs, uppercase
// hex, a cancel target on dispatch and a dispatch without op.
func TestRequestFieldBoundaries(t *testing.T) {
	base := parseDoc(t, fixture(t, "m0-launch", "dispatch.valid.json"))
	accept := func(name, path string, nv *value) {
		t.Helper()
		v := parseDoc(t, base.encode(nil))
		setPath(t, v, path, nv)
		if _, err := DecodeRequest(v.encode(nil)); err != nil {
			t.Errorf("FIELDS: %s boundary refused: %v", name, err)
		}
	}
	refuse := func(name, path string, nv *value) {
		t.Helper()
		v := parseDoc(t, base.encode(nil))
		setPath(t, v, path, nv)
		if _, err := DecodeRequest(v.encode(nil)); CodeOf(err) != CodeRequestInvalid {
			t.Errorf("FIELDS: %s accepted: %v", name, err)
		}
	}
	accept("uid-min", "worker.uid", integer(30000))
	accept("uid-max", "worker.uid", integer(59999))
	refuse("uid-below", "worker.uid", integer(29999))
	refuse("uid-above", "worker.uid", integer(60000))
	accept("serial-min", "policy.revision", integer(1))
	refuse("serial-zero", "policy.revision", integer(0))
	refuse("serial-string", "policy.revision", text("1"))
	refuse("id-short", "receiver", text(strings.Repeat("e", 31)))
	refuse("id-long", "receiver", text(strings.Repeat("e", 33)))
	refuse("id-upper", "receiver", text(strings.Repeat("E", 32)))
	refuse("digest-no-prefix", "plan.profile", text(strings.Repeat("2", 64)))
	refuse("kind", "kind", text("swarma-dispatcher m0-request/2"))
	refuse("version", "v", integer(2))
	refuse("op-null", "op", &value{kind: kindNull})
	refuse("cancel-target-on-dispatch", "dispatch_request_id", text(strings.Repeat("f", 32)))
	refuse("sig-85", "auth.sig", text(strings.Repeat("A", 85)))
	refuse("sig-87", "auth.sig", text(strings.Repeat("A", 87)))
	refuse("key-42", "auth.key", text(keyPrefix+strings.Repeat("A", 42)))
}
