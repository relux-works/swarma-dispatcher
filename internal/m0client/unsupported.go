//go:build !linux

package m0client

import (
	"errors"
	"io"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

// ErrUnsupported is the refusal of every operation outside Linux.
var ErrUnsupported = errors.New("m0client: Linux is required")

func submit(string, m0.Op, string, io.Reader) Outcome { return local("unsupported", ErrUnsupported) }
func resend(string, string) Outcome                   { return local("unsupported", ErrUnsupported) }
