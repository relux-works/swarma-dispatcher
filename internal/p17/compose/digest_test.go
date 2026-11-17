package compose

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// Claim: ConfigDigest equals an independent CCJ-1 vector, distinguishes ordered inputs and single-byte replacements, and uses the server-only form when M0 is closed.
func TestConfigDigest(t *testing.T) {
	server, m0 := []byte("server fixture\n"), []byte("m0 fixture\n")
	first, second := sha256.Sum256(server), sha256.Sum256(m0)
	// These ASCII keys/hex values need no escaping; encoding/json sorts the
	// object independently of ConfigDigest's explicit canonical encoding.
	vector, err := json.Marshal(map[string]string{"m0": hex.EncodeToString(second[:]), "server": hex.EncodeToString(first[:])})
	if err != nil {
		t.Fatal(err)
	}
	outer := sha256.Sum256(vector)
	expected := hex.EncodeToString(outer[:])
	if actual := ConfigDigest(server, m0); actual != expected {
		t.Fatalf("independent vector: %s != %s", actual, expected)
	}
	if ConfigDigest(m0, server) == expected || ConfigDigest([]byte("Server fixture\n"), m0) == expected || ConfigDigest(server, []byte("M0 fixture\n")) == expected {
		t.Fatal("ordered input or one-byte replacement lost")
	}
	only, err := json.Marshal(map[string]string{"server": hex.EncodeToString(first[:])})
	if err != nil {
		t.Fatal(err)
	}
	closed := sha256.Sum256(only)
	if ConfigDigest(server, nil) != hex.EncodeToString(closed[:]) || ConfigDigest(server, nil) == expected {
		t.Fatal("closed M0 digest form")
	}
}
