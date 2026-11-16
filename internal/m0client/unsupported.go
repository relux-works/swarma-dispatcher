//go:build !linux

package m0client

import (
	"errors"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

// ErrUnsupported is the refusal of every operation outside Linux.
var ErrUnsupported = errors.New("m0client: Linux is required")

type host struct{}

func loadConfig(string) (*Config, error) { return nil, ErrUnsupported }
func submit(*Config, m0.Op) Outcome      { return local("unsupported", ErrUnsupported) }
func resend(*Config, string) Outcome     { return local("unsupported", ErrUnsupported) }
