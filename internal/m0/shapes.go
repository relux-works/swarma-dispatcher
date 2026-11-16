package m0

import (
	"encoding/base64"
	"math/big"
	"strings"
)

// Proposed finite bounds of spec/m0-launch.md section 2. These are joint
// acceptance candidates, not owner-decided facts.
const (
	idLength      = 32
	digestPrefix  = "sha256:"
	digestHexLen  = 64
	keyPrefix     = "ed25519:"
	keyBase64Len  = 43
	sigBase64Len  = 86
	uidMin        = 30000
	uidMax        = 59999
	maxUnixSecond = 253402300799
	maxWindow     = 3600
	maxRuntime    = 3600
	maxDetect     = 30
	maxStop       = 270
	maxEnvelope   = 300
)

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func isID(s string) bool { return isLowerHex(s, idLength) }
func isDigest(s string) bool {
	return strings.HasPrefix(s, digestPrefix) && isLowerHex(s[len(digestPrefix):], digestHexLen)
}
func isSerial(n int64) bool { return n >= 1 && n <= maxSafeInteger }
func isUID(n int64) bool    { return n >= uidMin && n <= uidMax }

// decodeBase64Canonical accepts only canonical unpadded base64url of exactly
// want bytes: strict decoding refuses non-zero unused tail bits and padding,
// and re-encoding must reproduce the spelling.
func decodeBase64Canonical(s string, want int) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(raw) != want || base64.RawURLEncoding.EncodeToString(raw) != s {
		return nil, false
	}
	return raw, true
}

func decodeKey(s string) ([]byte, bool) {
	if !strings.HasPrefix(s, keyPrefix) || len(s) != len(keyPrefix)+keyBase64Len {
		return nil, false
	}
	return decodeBase64Canonical(s[len(keyPrefix):], 32)
}

func decodeSignature(s string) ([]byte, bool) {
	if len(s) != sigBase64Len {
		return nil, false
	}
	return decodeBase64Canonical(s, 64)
}

var fieldPrime = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))

// canonicalPointEncoding reports whether a 32-byte Ed25519 public key uses the
// canonical point encoding: the y coordinate is below the field prime, and the
// sign bit is clear when x is zero (y equal to 1 or p-1). It does not reject
// small-order points; curve membership is checked by the verifier.
func canonicalPointEncoding(raw []byte) bool {
	if len(raw) != 32 {
		return false
	}
	le := make([]byte, 32)
	copy(le, raw)
	sign := le[31] >> 7
	le[31] &= 0x7f
	be := make([]byte, 32)
	for i := range le {
		be[31-i] = le[i]
	}
	y := new(big.Int).SetBytes(be)
	if y.Cmp(fieldPrime) >= 0 {
		return false
	}
	if sign == 1 {
		pMinusOne := new(big.Int).Sub(fieldPrime, big.NewInt(1))
		if y.Cmp(big.NewInt(1)) == 0 || y.Cmp(pMinusOne) == 0 {
			return false
		}
	}
	return true
}

// Member accessors for closed forms. Each refuses with the member name only.
func idMember(v *value, key string) (string, error) {
	s, ok := v.member(key).text()
	if !ok || !isID(s) {
		return "", invalid("member spelling: " + key)
	}
	return s, nil
}
func digestMember(v *value, key string) (string, error) {
	s, ok := v.member(key).text()
	if !ok || !isDigest(s) {
		return "", invalid("member spelling: " + key)
	}
	return s, nil
}
func serialMember(v *value, key string) (uint64, error) {
	n, ok := v.member(key).number()
	if !ok || !isSerial(n) {
		return 0, invalid("member range: " + key)
	}
	return uint64(n), nil
}
func uidMember(v *value, key string) (uint32, error) {
	n, ok := v.member(key).number()
	if !ok || !isUID(n) {
		return 0, invalid("member range: " + key)
	}
	return uint32(n), nil
}
func constMember(v *value, key, want string) error {
	s, ok := v.member(key).text()
	if !ok || s != want {
		return invalid("member constant: " + key)
	}
	return nil
}
func versionMember(v *value) error {
	n, ok := v.member("v").number()
	if !ok || n != 1 {
		return invalid("member constant: v")
	}
	return nil
}
func keyMember(v *value, key string) (string, error) {
	s, ok := v.member(key).text()
	if !ok {
		return "", invalid("member spelling: " + key)
	}
	raw, ok := decodeKey(s)
	if !ok || !canonicalPointEncoding(raw) {
		return "", invalid("member spelling: " + key)
	}
	return s, nil
}
