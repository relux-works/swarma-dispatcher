//go:build linux && arm64

package journal

import "syscall"

const sysGetrandom = syscall.SYS_GETRANDOM
