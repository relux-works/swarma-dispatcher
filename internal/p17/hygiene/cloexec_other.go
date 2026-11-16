//go:build !linux

package hygiene

import "errors"

func cloexec(int) (bool, error) {
	return false, errors.New("descriptor flag inspection unsupported on this platform")
}
