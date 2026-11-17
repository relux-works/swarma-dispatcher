package transport

import "sync"

// ServerSnapshot retains the exact protected bytes and parsed configuration.
// Only LoadServer can construct one; starting its daemon consumes it once.
type ServerSnapshot struct{ state *serverSnapshotState }

// The consumption token is shared even if a caller copies the opaque value.
type serverSnapshotState struct {
	mu       sync.Mutex
	bytes    []byte
	config   serverConfig
	consumed bool
}

// Bytes returns a copy, even after consumption. It never rereads a path.
func (s *ServerSnapshot) Bytes() []byte {
	if s == nil || s.state == nil {
		return nil
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	return append([]byte(nil), s.state.bytes...)
}

func (s *ServerSnapshot) take() (serverConfig, error) {
	if s == nil || s.state == nil {
		return serverConfig{}, ErrConfig
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.consumed {
		return serverConfig{}, ErrConfig
	}
	s.state.consumed = true
	return s.state.config, nil
}
