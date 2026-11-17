// Package digest implements the pure startup transcript shared by command
// orchestration and transport's replacement-race control. It grants no authority.
package digest

import (
	"crypto/sha256"
	"fmt"
	"io"
)

// ConfigDigest hashes the CCJ-1 object of the exact protected snapshot hashes.
// An absent M0 snapshot uses the server-only form; no LF or domain is hashed.
func ConfigDigest(serverBytes, m0Bytes []byte) string {
	server := sha256.Sum256(serverBytes)
	var canonical string
	if len(m0Bytes) == 0 {
		canonical = fmt.Sprintf(`{"server":"%x"}`, server)
	} else {
		m0 := sha256.Sum256(m0Bytes)
		canonical = fmt.Sprintf(`{"m0":"%x","server":"%x"}`, m0, server)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
}

// AnnounceAndServe emits the authoritative snapshot line before consuming the
// snapshot. Its caller supplies a closure over that same loaded instance.
func AnnounceAndServe(serverBytes, m0Bytes []byte, stderr io.Writer, serve func() error) error {
	if _, err := fmt.Fprintln(stderr, "config-digest", ConfigDigest(serverBytes, m0Bytes)); err != nil {
		return err
	}
	return serve()
}
