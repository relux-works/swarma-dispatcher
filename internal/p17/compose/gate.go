package compose

import "github.com/relux-works/swarma-dispatcher/internal/m0"

const (
	ClassStart = "start"
	ClassEnd   = "end"
)

// StaticGate is the future statement verifier. End omits only the start window.
type StaticGate interface{ Verify(class string) error }

// UnpinnedStaticGate refuses until a published statement verifier is supplied.
type UnpinnedStaticGate struct{}

func (UnpinnedStaticGate) Verify(string) error {
	return m0.Unpinned("static gate: statement verifier is unpinned")
}
