package m0

import "encoding/base64"

// This file exports thin wrappers for the caller side (internal/m0client). Each
// wrapper only calls the existing canonicalizer, closed-form decoder and
// spelling checks of this package; none changes daemon behavior or adds a
// second implementation of CCJ-1.

// DecodeUnsigned strictly decodes one unsigned request R (no auth member) with
// the same canonicalizer, closed forms and preimage bounds DecodeRequest
// applies to the signed wire form. Canonical and Preimage of the result are
// the exact bytes a signer must use.
func DecodeUnsigned(doc []byte) (*Request, error) {
	v, _, err := canonicalize(doc, requestLimits)
	if err != nil {
		return nil, err
	}
	return decodeUnsigned(v)
}

// KeySpelling returns the canonical association key spelling of a 32-byte
// Ed25519 public key. It refuses any value the request decoder would refuse.
func KeySpelling(pub []byte) (string, error) {
	if len(pub) != 32 {
		return "", invalid("member spelling: key")
	}
	s := keyPrefix + base64.RawURLEncoding.EncodeToString(pub)
	raw, ok := decodeKey(s)
	if !ok || !canonicalPointEncoding(raw) {
		return "", invalid("member spelling: key")
	}
	return s, nil
}

// SignatureSpelling returns the canonical spelling of a 64-byte Ed25519
// signature. It refuses any value the request decoder would refuse.
func SignatureSpelling(sig []byte) (string, error) {
	if len(sig) != 64 {
		return "", invalid("member spelling: sig")
	}
	s := base64.RawURLEncoding.EncodeToString(sig)
	if _, ok := decodeSignature(s); !ok {
		return "", invalid("member spelling: sig")
	}
	return s, nil
}

// SignedWire attaches auth={key,sig} to one unsigned request R without
// changing R and returns the CCJ-1 bytes of the signed wire request. The
// result is accepted only if DecodeRequest accepts it; the signature equation
// is not checked here (Request.Verify does that).
func SignedWire(unsigned []byte, key, sig string) ([]byte, error) {
	v, _, err := canonicalize(unsigned, requestLimits)
	if err != nil {
		return nil, err
	}
	if v.kind != kindObject || v.member("auth") != nil {
		return nil, invalid("unsigned request expected")
	}
	signed := object(append(append([]member(nil), v.members...), member{"auth", object(
		member{"key", text(key)},
		member{"sig", text(sig)},
	)})...)
	wire := signed.encode(nil)
	if _, err := DecodeRequest(wire); err != nil {
		return nil, err
	}
	return wire, nil
}
