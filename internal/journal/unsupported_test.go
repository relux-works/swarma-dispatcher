//go:build !linux || (!amd64 && !arm64)

package journal

import "testing"

// Unsupported targets expose no successful ownership or I/O path. Codec and
// argument tests still run independently on these targets.
func TestUnsupportedPlatform(t *testing.T) {
	s, err := Initialize(Config{}, []byte{'a'})
	wantClass(t, err, UnsupportedStorage)
	if s != nil {
		t.Fatal("unsupported initialization returned session")
	}
	s, err = OpenEstablished(Config{}, nil)
	wantClass(t, err, UnsupportedStorage)
	if s != nil {
		t.Fatal("unsupported recovery returned session")
	}
	session := &Session{}
	_, err = session.Append([]byte{'a'})
	wantClass(t, err, UnsupportedStorage)
	wantClass(t, session.CreateInput("i", nil), UnsupportedStorage)
	wantClass(t, session.ReplaceRecord("r", nil), UnsupportedStorage)
	_, err = session.ReadInput("i", [32]byte{})
	wantClass(t, err, UnsupportedStorage)
	wantClass(t, session.Close(), UnsupportedStorage)
	if state := session.State(); state.Ready || !state.Closed {
		t.Fatal("unsupported state ready")
	}
	recovery := &Recovery{}
	wantClass(t, recovery.ReplaceRecord("r", nil), UnsupportedStorage)
	_, err = recovery.ReadInput("i", [32]byte{})
	wantClass(t, err, UnsupportedStorage)
}
