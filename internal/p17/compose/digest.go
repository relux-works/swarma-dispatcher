package compose

import "github.com/relux-works/swarma-dispatcher/internal/p17/compose/digest"

// ConfigDigest hashes the closed canonical object of protected snapshot hashes.
// A nil/empty M0 snapshot denotes the capacity-only server form.
func ConfigDigest(serverBytes, m0Bytes []byte) string {
	return digest.ConfigDigest(serverBytes, m0Bytes)
}
