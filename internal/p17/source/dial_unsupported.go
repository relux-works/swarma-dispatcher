//go:build !linux

package source

import (
	"errors"
	"time"
)

// UnixDialer refuses on platforms without SO_PEERCRED support in this build.
type UnixDialer struct {
	Timeout time.Duration
}

func (UnixDialer) Dial(string) (Conn, error) {
	return nil, errors.New("source: peer credentials unsupported on this platform")
}
