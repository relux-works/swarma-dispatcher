//go:build linux && amd64

package journal

// Linux amd64 getrandom is absent from the frozen syscall constants.
const sysGetrandom = 318
