//go:build linux && (amd64 || arm64)

package m0hook

// OnEffectWait installs a test-only observer before a contended effect lock.
func OnEffectWait(h *Hooked, f func()) { h.lockWait = f }

// AfterClaim installs a test-only barrier between claim and the Start lock.
func AfterClaim(h *Hooked, f func()) { h.afterClaim = f }

// BeforeStep installs a test-only barrier before an event or inner-start check.
func BeforeStep(h *Hooked, f func(string)) { h.beforeStep = f }
